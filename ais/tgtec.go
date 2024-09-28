// Package ais provides core functionality for the AIStore object storage.
/*
 * Copyright (c) 2024, NVIDIA CORPORATION. All rights reserved.
 */
package ais

import (
	"errors"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"time"

	"github.com/NVIDIA/aistore/api/apc"
	"github.com/NVIDIA/aistore/cmn"
	"github.com/NVIDIA/aistore/cmn/cos"
	"github.com/NVIDIA/aistore/cmn/nlog"
	"github.com/NVIDIA/aistore/core/meta"
	"github.com/NVIDIA/aistore/ec"
	"github.com/NVIDIA/aistore/hk"
)

var errCloseStreams = errors.New("EC is currently active, cannot close streams")

func (t *target) ecHandler(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		t.httpecget(w, r)
	case http.MethodPost:
		t.httpecpost(w, r)
	default:
		cmn.WriteErr405(w, r, http.MethodGet)
	}
}

func (t *target) httpecget(w http.ResponseWriter, r *http.Request) {
	apireq := apiReqAlloc(3, apc.URLPathEC.L, false)
	apireq.bckIdx = 1
	if err := t.parseReq(w, r, apireq); err != nil {
		apiReqFree(apireq)
		return
	}
	switch apireq.items[0] {
	case ec.URLMeta:
		t.sendECMetafile(w, r, apireq.bck, apireq.items[2])
	default:
		t.writeErrURL(w, r)
	}
	apiReqFree(apireq)
}

// Returns a CT's metadata.
func (t *target) sendECMetafile(w http.ResponseWriter, r *http.Request, bck *meta.Bck, objName string) {
	if err := bck.Init(t.owner.bmd); err != nil {
		if !cmn.IsErrRemoteBckNotFound(err) { // is ais
			t.writeErr(w, r, err, Silent)
			return
		}
	}
	md, err := ec.ObjectMetadata(bck, objName)
	if err != nil {
		if os.IsNotExist(err) {
			t.writeErr(w, r, err, http.StatusNotFound, Silent)
		} else {
			t.writeErr(w, r, err, http.StatusInternalServerError, Silent)
		}
		return
	}
	b := md.NewPack()
	w.Header().Set(cos.HdrContentLength, strconv.Itoa(len(b)))
	w.Write(b)
}

func (t *target) httpecpost(w http.ResponseWriter, r *http.Request) {
	const (
		hkname   = apc.ActEcClose + hk.NameSuffix
		postpone = time.Minute
	)
	items, err := t.parseURL(w, r, apc.URLPathEC.L, 1, false)
	if err != nil {
		return
	}
	action := items[0]
	switch action {
	case apc.ActEcOpen:
		hk.UnregIf(hkname, closeEc) // just in case, a no-op most of the time
		ec.ECM.OpenStreams(false /*with refc*/)
	case apc.ActEcClose:
		if !t.ensureIntraControl(w, r, true /* from primary */) {
			return
		}
		if ec.ECM.IsActive() {
			t.writeErr(w, r, errCloseStreams)
			return
		}
		nlog.Infoln(t.String(), "hk-postpone", action)
		hk.Reg(hkname, closeEc, postpone)
	default:
		t.writeErr(w, r, errActEc(action))
	}
}

func closeEc(int64) time.Duration {
	if ec.ECM.IsActive() {
		nlog.Warningln("hk-cb:", errCloseStreams)
	} else {
		ec.ECM.CloseStreams(false /*with refc*/)
	}
	return hk.UnregInterval
}

func errActEc(act string) error {
	return fmt.Errorf(fmtErrInvaldAction, act, []string{apc.ActEcOpen, apc.ActEcClose})
}
