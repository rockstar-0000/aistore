// Package api provides AIStore API over HTTP(S)
/*
 * Copyright (c) 2018-2023, NVIDIA CORPORATION. All rights reserved.
 */
package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"time"

	"github.com/NVIDIA/aistore/api/apc"
	"github.com/NVIDIA/aistore/cmn"
	"github.com/NVIDIA/aistore/cmn/cos"
	"github.com/NVIDIA/aistore/cmn/debug"
	"github.com/NVIDIA/aistore/cmn/mono"
	"github.com/NVIDIA/aistore/nl"
	"github.com/NVIDIA/aistore/xact"
)

// Start xaction
func StartXaction(bp BaseParams, args xact.ArgsMsg) (xid string, err error) {
	if !xact.Table[args.Kind].Startable {
		return "", fmt.Errorf("xaction %q is not startable", args.Kind)
	}
	q := args.Bck.AddToQuery(nil)
	if args.Force {
		q.Set(apc.QparamForce, "true")
	}
	msg := apc.ActMsg{Action: apc.ActXactStart, Value: args}
	bp.Method = http.MethodPut
	reqParams := AllocRp()
	{
		reqParams.BaseParams = bp
		reqParams.Path = apc.URLPathClu.S
		reqParams.Body = cos.MustMarshal(msg)
		reqParams.Header = http.Header{cos.HdrContentType: []string{cos.ContentJSON}}
		reqParams.Query = q
	}
	_, err = reqParams.doReqStr(&xid)
	FreeRp(reqParams)
	return
}

// Abort ("stop") xactions
func AbortXaction(bp BaseParams, args xact.ArgsMsg) (err error) {
	msg := apc.ActMsg{Action: apc.ActXactStop, Value: args}
	bp.Method = http.MethodPut
	reqParams := AllocRp()
	{
		reqParams.BaseParams = bp
		reqParams.Path = apc.URLPathClu.S
		reqParams.Body = cos.MustMarshal(msg)
		reqParams.Header = http.Header{cos.HdrContentType: []string{cos.ContentJSON}}
		reqParams.Query = args.Bck.AddToQuery(nil)
	}
	err = reqParams.DoRequest()
	FreeRp(reqParams)
	return
}

//
// querying and waiting
//

// returns unique ':'-separated kind/ID pairs (strings)
// e.g.: put-copies[D-ViE6HEL_j] list[H96Y7bhR2s] copy-bck[matRQMRes] put-copies[pOibtHExY]
// TODO: return idle xactions separately
func GetAllRunningXactions(bp BaseParams, kindOrName string) (out []string, err error) {
	msg := xact.QueryMsg{Kind: kindOrName}
	bp.Method = http.MethodGet
	reqParams := AllocRp()
	{
		reqParams.BaseParams = bp
		reqParams.Path = apc.URLPathClu.S
		reqParams.Body = cos.MustMarshal(msg)
		reqParams.Header = http.Header{cos.HdrContentType: []string{cos.ContentJSON}}
		reqParams.Query = url.Values{apc.QparamWhat: []string{apc.WhatAllRunningXacts}}
	}
	_, err = reqParams.DoReqAny(&out)
	FreeRp(reqParams)
	return
}

// QueryXactionSnaps gets all xaction snaps based on the specified selection.
// NOTE: args.Kind can be either xaction kind or name - here and elsewhere
func QueryXactionSnaps(bp BaseParams, args xact.ArgsMsg) (xs xact.MultiSnap, err error) {
	msg := xact.QueryMsg{ID: args.ID, Kind: args.Kind, Bck: args.Bck}
	if args.OnlyRunning {
		msg.OnlyRunning = Bool(true)
	}
	bp.Method = http.MethodGet
	reqParams := AllocRp()
	{
		reqParams.BaseParams = bp
		reqParams.Path = apc.URLPathClu.S
		reqParams.Body = cos.MustMarshal(msg)
		reqParams.Header = http.Header{cos.HdrContentType: []string{cos.ContentJSON}}
		reqParams.Query = url.Values{apc.QparamWhat: []string{apc.WhatQueryXactStats}}
	}
	_, err = reqParams.DoReqAny(&xs)
	FreeRp(reqParams)
	return
}

// GetOneXactionStatus queries one of the IC (proxy) members for status
// of the `args`-identified xaction.
// NOTE:
// - is used internally by the WaitForXactionIC() helper function (to wait on xaction)
// - returns a single matching xaction or none;
// - when the `args` filter "covers" multiple xactions the returned status corresponds to
// any matching xaction that's currently running, or - if nothing's running -
// the one that's finished most recently,
// if exists
func GetOneXactionStatus(bp BaseParams, args xact.ArgsMsg) (status *nl.Status, err error) {
	status = &nl.Status{}
	q := url.Values{apc.QparamWhat: []string{apc.WhatOneXactStatus}}
	err = getxst(status, q, bp, args)
	return
}

// same as above, except that it returns _all_ matching xactions
func GetAllXactionStatus(bp BaseParams, args xact.ArgsMsg, force bool) (matching nl.StatusVec, err error) {
	q := url.Values{apc.QparamWhat: []string{apc.WhatAllXactStatus}}
	if force {
		// (force just-in-time)
		// for each args-selected xaction:
		// check if any of the targets delayed updating the corresponding status,
		// and query those targets directly
		q.Set(apc.QparamForce, "true")
	}
	err = getxst(&matching, q, bp, args)
	return
}

func getxst(out any, q url.Values, bp BaseParams, args xact.ArgsMsg) (err error) {
	bp.Method = http.MethodGet
	msg := xact.QueryMsg{ID: args.ID, Kind: args.Kind, Bck: args.Bck}
	if args.OnlyRunning {
		msg.OnlyRunning = Bool(true)
	}
	reqParams := AllocRp()
	{
		reqParams.BaseParams = bp
		reqParams.Path = apc.URLPathClu.S
		reqParams.Body = cos.MustMarshal(msg)
		reqParams.Header = http.Header{cos.HdrContentType: []string{cos.ContentJSON}}
		reqParams.Query = q
	}
	_, err = reqParams.DoReqAny(out)
	FreeRp(reqParams)
	return
}

// Wait for bucket summary:
//  1. The function sends the requests as is (lsmsg.UUID should be empty) to initiate
//     asynchronous task. The destination returns ID of a newly created task
//  2. Starts polling: request destination with received UUID in a loop while
//     the destination returns StatusAccepted=task is still running
//     Time between requests is dynamic: it starts at 200ms and increases
//     by half after every "not-StatusOK" request. It is limited with 10 seconds
//  3. Breaks loop on error
//  4. If the destination returns status code StatusOK, it means the response
//     contains the real data and the function returns the response to the caller
func (reqParams *ReqParams) waitBsumm(msg *apc.BsummCtrlMsg, bsumm *cmn.AllBsummResults) error {
	var (
		uuid   string
		sleep  = xact.MinPollTime
		actMsg = apc.ActMsg{Action: apc.ActSummaryBck, Value: msg}
		body   = cos.MustMarshal(actMsg)
	)
	if reqParams.Query == nil {
		reqParams.Query = url.Values{}
	}
	reqParams.Body = body
	status, err := reqParams.doReqStr(&uuid)
	if err != nil {
		return err
	}
	if status != http.StatusAccepted {
		if status == http.StatusOK {
			return errors.New("expected 202 (\"accepted\") response, got 200 (\"ok\")")
		}
		return fmt.Errorf("invalid response code: %d", status)
	}
	if msg.UUID == "" {
		msg.UUID = uuid
		body = cos.MustMarshal(actMsg)
	}

	// Poll async task for http.StatusOK completion
	for {
		reqParams.Body = body
		status, err = reqParams.DoReqAny(bsumm)
		if err != nil {
			return err
		}
		if status == http.StatusOK {
			break
		}
		time.Sleep(sleep)
		if sleep < xact.MaxProbingFreq {
			sleep += sleep / 2
		}
	}
	return err
}

//
// TODO: use `xact.IdlesBeforeFinishing` to provide a single unified wait-for API
//

type consIdle struct {
	xid string
	cnt int
}

func (ci *consIdle) check(snaps xact.MultiSnap) (done, resetProbeFreq bool) {
	found, idle := snaps.IsIdle(ci.xid)
	if idle || !found {
		ci.cnt++
		// TODO: !found may mean "hasn't started yet" unless it's a "won't start"
		// situation; resetting frequency only if found
		done, resetProbeFreq = ci.cnt >= xact.NumConsecutiveIdle, found
		return
	}
	ci.cnt = 0
	return
}

// WaitForXactionIdle waits for a given on-demand xaction to be idle.
func WaitForXactionIdle(bp BaseParams, args xact.ArgsMsg) error {
	ci := &consIdle{xid: args.ID}
	args.OnlyRunning = true
	return WaitForXactionNode(bp, args, ci.check)
}

// WaitForXactionIC waits for a given xaction to complete.
// Use it only for global xactions
// (those that execute on all targets and report their status to IC, e.g. rebalance).
func WaitForXactionIC(bp BaseParams, args xact.ArgsMsg) (status *nl.Status, err error) {
	return _waitx(bp, args, nil)
}

// WaitForXactionNode waits for a given xaction to complete.
// Use for xactions that do _not_ report their status to IC members, namely:
// - xact.IdlesBeforeFinishing()
// - x-resilver (as it usually runs on a single node)
func WaitForXactionNode(bp BaseParams, args xact.ArgsMsg, fn func(xact.MultiSnap) (bool, bool)) error {
	debug.Assert(args.Kind != "" || xact.IsValidUUID(args.ID))
	_, err := _waitx(bp, args, fn)
	return err
}

// TODO: `status` is currently always nil when we wait with a (`fn`) callback
// TODO: un-defer cancel()
func _waitx(bp BaseParams, args xact.ArgsMsg, fn func(xact.MultiSnap) (bool, bool)) (status *nl.Status, err error) {
	var (
		elapsed         time.Duration
		begin           = mono.NanoTime()
		total, maxSleep = _times(args)
		sleep           = xact.MinPollTime
		ctx, cancel     = context.WithTimeout(context.Background(), total)
	)
	defer cancel()
	for {
		var done bool
		if fn == nil {
			status, err = GetOneXactionStatus(bp, args)
			done = err == nil && status.Finished() && elapsed >= xact.MinPollTime
		} else {
			var (
				snaps          xact.MultiSnap
				resetProbeFreq bool
			)
			snaps, err = QueryXactionSnaps(bp, args)
			if err == nil {
				done, resetProbeFreq = fn(snaps)
				if resetProbeFreq {
					sleep = xact.MinPollTime
				}
			}
		}
		canRetry := err == nil || cos.IsRetriableConnErr(err) || cmn.IsStatusServiceUnavailable(err)
		if done || !canRetry /*fail*/ {
			return
		}
		time.Sleep(sleep)
		elapsed = mono.Since(begin)
		sleep = cos.MinDuration(maxSleep, sleep+sleep/2)

		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		default:
			break
		}
	}
}

func _times(args xact.ArgsMsg) (time.Duration, time.Duration) {
	total := args.Timeout
	switch {
	case args.Timeout == 0:
		total = xact.DefWaitTimeShort
	case args.Timeout < 0:
		total = xact.DefWaitTimeLong
	}
	return total, cos.MinDuration(xact.MaxProbingFreq, cos.ProbingFrequency(total))
}
