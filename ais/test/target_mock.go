// Package integration contains AIS integration tests.
/*
 * Copyright (c) 2018-2023, NVIDIA CORPORATION. All rights reserved.
 */
package integration

import (
	"context"
	"fmt"
	"math"
	"net/http"
	"sync"
	"testing"

	"github.com/NVIDIA/aistore/ais"
	"github.com/NVIDIA/aistore/api"
	"github.com/NVIDIA/aistore/api/apc"
	"github.com/NVIDIA/aistore/cluster/meta"
	"github.com/NVIDIA/aistore/cmn/cos"
	"github.com/NVIDIA/aistore/reb"
	"github.com/NVIDIA/aistore/tools"
	"github.com/NVIDIA/aistore/tools/tlog"
	jsoniter "github.com/json-iterator/go"
)

const (
	mockTargetPort = "8079"
)

type targetMocker interface {
	filehdlr(w http.ResponseWriter, r *http.Request)
	daemonhdlr(w http.ResponseWriter, r *http.Request)
	votehdlr(w http.ResponseWriter, r *http.Request)
	healthdlr(w http.ResponseWriter, r *http.Request)
}

type MockRegRequest struct {
	SI *meta.Snode `json:"si"`
}

func runMockTarget(t *testing.T, proxyURL string, mocktgt targetMocker, stopch chan struct{}, smap *meta.Smap, wg *sync.WaitGroup) {
	defer wg.Done()
	mux := http.NewServeMux()

	mux.HandleFunc(apc.URLPathBuckets.S, mocktgt.filehdlr)
	mux.HandleFunc(apc.URLPathObjects.S, mocktgt.filehdlr)
	mux.HandleFunc(apc.URLPathDae.S, mocktgt.daemonhdlr)
	mux.HandleFunc(apc.URLPathVote.S, mocktgt.votehdlr)
	mux.HandleFunc(apc.URLPathHealth.S, mocktgt.healthdlr)

	target, _ := smap.GetRandTarget()
	ip := target.PubNet.Hostname

	s := &http.Server{Addr: ip + ":" + mockTargetPort, Handler: mux}
	go s.ListenAndServe()

	err := registerMockTarget(proxyURL, smap)
	if err != nil {
		t.Errorf("failed to start http server for mock target: %v", err)
		return
	}
	tlog.Logf("t[%s] is up\n", tools.MockDaemonID)

	<-stopch

	tlog.Logf("started unsafe removal of t[%s]\n", tools.MockDaemonID)
	err = tools.RemoveNodeUnsafe(proxyURL, tools.MockDaemonID)
	if err != nil {
		tlog.Logf("Error: failed to unsafely remove t[%s]: %v\n", tools.MockDaemonID, err)
	}
	s.Shutdown(context.Background())
}

func registerMockTarget(proxyURL string, smap *meta.Smap) error {
	var (
		jsonDaemonInfo []byte
		err            error
	)

	// borrow a random target's ip but using a different port to register the mock target
	for _, v := range smap.Tmap {
		v.DaeID = tools.MockDaemonID
		v.PubNet = meta.NetInfo{
			Hostname: v.PubNet.Hostname,
			Port:     mockTargetPort,
			URL:      "http://" + v.PubNet.Hostname + ":" + mockTargetPort,
		}
		v.ControlNet = v.PubNet
		v.DataNet = v.PubNet
		regReq := MockRegRequest{SI: v}
		jsonDaemonInfo, err = jsoniter.Marshal(regReq)
		if err != nil {
			return err
		}
		break
	}
	baseParams := tools.BaseAPIParams(proxyURL)
	baseParams.Method = http.MethodPost
	reqParams := &api.ReqParams{
		BaseParams: baseParams,
		Path:       apc.URLPathCluAutoReg.S,
		Body:       jsonDaemonInfo,
		Header:     http.Header{cos.HdrContentType: []string{cos.ContentJSON}},
	}
	return reqParams.DoRequest()
}

type voteRetryMockTarget struct {
	voteInProgress bool
	errCh          chan error
}

type cluMetaRedux struct {
	Smap           *meta.Smap
	VoteInProgress bool `json:"voting"`
}

func newVoteMsg(inp bool) cluMetaRedux {
	return cluMetaRedux{VoteInProgress: inp, Smap: &meta.Smap{Version: 1}}
}

func (*voteRetryMockTarget) filehdlr(http.ResponseWriter, *http.Request) {
	// Ignore all file requests
}

func (p *voteRetryMockTarget) daemonhdlr(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		msg := newVoteMsg(p.voteInProgress) // treat all Get requests as requests for a VoteMsg
		jsbytes, err := jsoniter.Marshal(msg)
		if err == nil {
			_, err = w.Write(jsbytes)
		}
		if err != nil {
			p.errCh <- fmt.Errorf("error writing vote message: %v", err)
		}
	default:
	}
}

func (*voteRetryMockTarget) votehdlr(w http.ResponseWriter, _ *http.Request) {
	// Always vote yes.
	w.Write([]byte(ais.VoteYes))
}

func (p *voteRetryMockTarget) healthdlr(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()
	getRebStatus := cos.IsParseBool(query.Get(apc.QparamRebStatus))
	if getRebStatus {
		status := &reb.Status{}
		status.RebID = math.MaxInt64 // to abort t[MOCK] join triggered rebalance
		body := cos.MustMarshal(status)
		_, err := w.Write(body)
		if err != nil {
			p.errCh <- fmt.Errorf("error writing reb-status: %v", err)
		}
	}
}
