// Package api provides Go based AIStore API/SDK over HTTP(S)
/*
 * Copyright (c) 2018-2023, NVIDIA CORPORATION. All rights reserved.
 */
package api

import (
	"io"
	"net/http"
	"net/url"
	"strconv"

	"github.com/NVIDIA/aistore/api/apc"
	"github.com/NVIDIA/aistore/cmn"
	"github.com/NVIDIA/aistore/cmn/cos"
	"github.com/NVIDIA/aistore/core/meta"
	"github.com/NVIDIA/aistore/ios"
	"github.com/NVIDIA/aistore/stats"
)

type GetLogInput struct {
	Writer   io.Writer
	Severity string // one of: {cmn.LogInfo, ...}
	Offset   int64
	All      bool
}

// GetMountpaths given the direct public URL of the target, returns the target's mountpaths or error.
func GetMountpaths(bp BaseParams, node *meta.Snode) (mpl *apc.MountpathList, err error) {
	bp.Method = http.MethodGet
	reqParams := AllocRp()
	{
		reqParams.BaseParams = bp
		reqParams.Path = apc.URLPathReverseDae.S
		reqParams.Query = url.Values{apc.QparamWhat: []string{apc.WhatMountpaths}}
		reqParams.Header = http.Header{
			apc.HdrNodeID:  []string{node.ID()},
			apc.HdrNodeURL: []string{node.URL(cmn.NetPublic)},
		}
	}
	_, err = reqParams.DoReqAny(&mpl)
	FreeRp(reqParams)
	return mpl, err
}

// TODO: rewrite tests that come here with `force`
func AttachMountpath(bp BaseParams, node *meta.Snode, mountpath string, force bool) error {
	bp.Method = http.MethodPut
	reqParams := AllocRp()
	{
		reqParams.BaseParams = bp
		reqParams.Path = apc.URLPathReverseDae.Join(apc.Mountpaths)
		reqParams.Body = cos.MustMarshal(apc.ActMsg{Action: apc.ActMountpathAttach, Value: mountpath})
		reqParams.Header = http.Header{
			apc.HdrNodeID:      []string{node.ID()},
			apc.HdrNodeURL:     []string{node.URL(cmn.NetPublic)},
			cos.HdrContentType: []string{cos.ContentJSON},
		}
		reqParams.Query = url.Values{apc.QparamForce: []string{strconv.FormatBool(force)}}
	}
	err := reqParams.DoRequest()
	FreeRp(reqParams)
	return err
}

func EnableMountpath(bp BaseParams, node *meta.Snode, mountpath string) error {
	bp.Method = http.MethodPost
	reqParams := AllocRp()
	{
		reqParams.BaseParams = bp
		reqParams.Path = apc.URLPathReverseDae.Join(apc.Mountpaths)
		reqParams.Body = cos.MustMarshal(apc.ActMsg{Action: apc.ActMountpathEnable, Value: mountpath})
		reqParams.Header = http.Header{
			apc.HdrNodeID:      []string{node.ID()},
			apc.HdrNodeURL:     []string{node.URL(cmn.NetPublic)},
			cos.HdrContentType: []string{cos.ContentJSON},
		}
	}
	err := reqParams.DoRequest()
	FreeRp(reqParams)
	return err
}

func DetachMountpath(bp BaseParams, node *meta.Snode, mountpath string, dontResilver bool) error {
	var q url.Values
	if dontResilver {
		q = url.Values{apc.QparamDontResilver: []string{"true"}}
	}
	bp.Method = http.MethodDelete
	reqParams := AllocRp()
	{
		reqParams.BaseParams = bp
		reqParams.Path = apc.URLPathReverseDae.Join(apc.Mountpaths)
		reqParams.Body = cos.MustMarshal(apc.ActMsg{Action: apc.ActMountpathDetach, Value: mountpath})
		reqParams.Header = http.Header{
			apc.HdrNodeID:      []string{node.ID()},
			cos.HdrContentType: []string{cos.ContentJSON},
		}
		reqParams.Query = q
	}
	err := reqParams.DoRequest()
	FreeRp(reqParams)
	return err
}

func DisableMountpath(bp BaseParams, node *meta.Snode, mountpath string, dontResilver bool) error {
	var q url.Values
	if dontResilver {
		q = url.Values{apc.QparamDontResilver: []string{"true"}}
	}
	bp.Method = http.MethodPost
	reqParams := AllocRp()
	{
		reqParams.BaseParams = bp
		reqParams.Path = apc.URLPathReverseDae.Join(apc.Mountpaths)
		reqParams.Body = cos.MustMarshal(apc.ActMsg{Action: apc.ActMountpathDisable, Value: mountpath})
		reqParams.Header = http.Header{
			apc.HdrNodeID:      []string{node.ID()},
			cos.HdrContentType: []string{cos.ContentJSON},
		}
		reqParams.Query = q
	}
	err := reqParams.DoRequest()
	FreeRp(reqParams)
	return err
}

// GetDaemonConfig returns the configuration of a specific daemon in a cluster.
// (compare with `api.GetClusterConfig`)
func GetDaemonConfig(bp BaseParams, node *meta.Snode) (config *cmn.Config, err error) {
	bp.Method = http.MethodGet
	reqParams := AllocRp()
	{
		reqParams.BaseParams = bp
		reqParams.Path = apc.URLPathReverseDae.S
		reqParams.Query = url.Values{apc.QparamWhat: []string{apc.WhatNodeConfig}}
		reqParams.Header = http.Header{apc.HdrNodeID: []string{node.ID()}}
	}
	_, err = reqParams.DoReqAny(&config)
	FreeRp(reqParams)
	if err != nil {
		return nil, err
	}
	// FIXME: transform backend structures on the client side
	// - as a side effect, config.Backend validation populates non-JSON structs that client can utilize;
	// - secondly, HDFS networking, etc.
	// TODO: revise and remove
	_ = config.Backend.Validate()
	return config, nil
}

// names _and_ kinds, i.e. (name, kind) pairs
func GetMetricNames(bp BaseParams, node *meta.Snode) (kvs cos.StrKVs, err error) {
	bp.Method = http.MethodGet
	reqParams := AllocRp()
	{
		reqParams.BaseParams = bp
		reqParams.Path = apc.URLPathReverseDae.S
		reqParams.Query = url.Values{apc.QparamWhat: []string{apc.WhatMetricNames}}
		reqParams.Header = http.Header{apc.HdrNodeID: []string{node.ID()}}
	}
	_, err = reqParams.DoReqAny(&kvs)
	FreeRp(reqParams)
	return
}

// How to compute throughputs:
//
// - AIS supports several enumerated metric "kinds", including `KindThroughput`
// (for complete enumeration, see stats/api.go)
// - By convention, metrics that have `KindThroughput` kind are named with ".bps"
// ("bytes per second") suffix.
// - ".bps" metrics reported by the API are, in fact, cumulative byte numbers.
// - It is the client's responsibility to compute the actual throughputs
// as only the client knows _when_ exactly the same ".bps" metric was queried
// the previous time.
//
// See also:
// - api.GetClusterStats
// - api.GetStatsAndStatus (below)
// - stats/api.go
func GetDaemonStats(bp BaseParams, node *meta.Snode) (ds *stats.Node, err error) {
	bp.Method = http.MethodGet
	reqParams := AllocRp()
	{
		reqParams.BaseParams = bp
		reqParams.Path = apc.URLPathReverseDae.S
		reqParams.Query = url.Values{apc.QparamWhat: []string{apc.WhatNodeStats}}
		reqParams.Header = http.Header{apc.HdrNodeID: []string{node.ID()}}
	}
	_, err = reqParams.DoReqAny(&ds)
	FreeRp(reqParams)
	return ds, err
}

func GetDiskStats(bp BaseParams, tid string) (res ios.AllDiskStats, err error) {
	bp.Method = http.MethodGet
	reqParams := AllocRp()
	{
		reqParams.BaseParams = bp
		reqParams.Path = apc.URLPathReverseDae.S
		reqParams.Query = url.Values{apc.QparamWhat: []string{apc.WhatDiskStats}}
		reqParams.Header = http.Header{apc.HdrNodeID: []string{tid}}
	}
	_, err = reqParams.DoReqAny(&res)
	FreeRp(reqParams)
	return
}

// Returns both node's stats and extended status
func GetStatsAndStatus(bp BaseParams, node *meta.Snode) (daeStatus *stats.NodeStatus, err error) {
	bp.Method = http.MethodGet
	reqParams := AllocRp()
	{
		reqParams.BaseParams = bp
		reqParams.Path = apc.URLPathReverseDae.S
		reqParams.Query = url.Values{apc.QparamWhat: []string{apc.WhatNodeStatsAndStatus}}
		reqParams.Header = http.Header{apc.HdrNodeID: []string{node.ID()}}
	}
	_, err = reqParams.DoReqAny(&daeStatus)
	FreeRp(reqParams)
	return daeStatus, err
}

// Returns log of a specific node in a cluster.
func GetDaemonLog(bp BaseParams, node *meta.Snode, args GetLogInput) (int64, error) {
	w := args.Writer
	q := make(url.Values, 3)
	q.Set(apc.QparamWhat, apc.WhatLog)
	if args.Severity != "" {
		q.Set(apc.QparamLogSev, args.Severity)
	}
	if args.Offset != 0 {
		q.Set(apc.QparamLogOff, strconv.FormatInt(args.Offset, 10))
	}
	if args.All {
		q.Set(apc.QparamAllLogs, "true")
	}
	bp.Method = http.MethodGet
	reqParams := AllocRp()
	{
		reqParams.BaseParams = bp
		reqParams.Path = apc.URLPathReverseDae.S
		reqParams.Query = q
		reqParams.Header = http.Header{apc.HdrNodeID: []string{node.ID()}}
	}
	wrap, err := reqParams.doWriter(w)
	FreeRp(reqParams)
	if err == nil {
		return wrap.n, nil
	}
	return 0, err
}

// SetDaemonConfig, given key value pairs, sets the configuration accordingly for a specific node.
func SetDaemonConfig(bp BaseParams, nodeID string, nvs cos.StrKVs, transient ...bool) error {
	bp.Method = http.MethodPut
	query := url.Values{}
	for key, val := range nvs {
		query.Add(key, val)
	}
	if len(transient) > 0 {
		query.Add(apc.ActTransient, strconv.FormatBool(transient[0]))
	}
	reqParams := AllocRp()
	{
		reqParams.BaseParams = bp
		reqParams.Path = apc.URLPathReverseDae.Join(apc.ActSetConfig)
		reqParams.Query = query
		reqParams.Header = http.Header{apc.HdrNodeID: []string{nodeID}}
	}
	err := reqParams.DoRequest()
	FreeRp(reqParams)
	return err
}

// see also: ResetClusterStats
func ResetDaemonStats(bp BaseParams, node *meta.Snode, errorsOnly bool) error {
	return _putDaemon(bp, node.ID(), apc.ActMsg{Action: apc.ActResetStats, Value: errorsOnly})
}

// reset node's configuration to cluster defaults
func ResetDaemonConfig(bp BaseParams, nodeID string) error {
	return _putDaemon(bp, nodeID, apc.ActMsg{Action: apc.ActResetConfig})
}

func RotateLogs(bp BaseParams, nodeID string) error {
	return _putDaemon(bp, nodeID, apc.ActMsg{Action: apc.ActRotateLogs})
}

func _putDaemon(bp BaseParams, nodeID string, msg apc.ActMsg) error {
	bp.Method = http.MethodPut
	reqParams := AllocRp()
	{
		reqParams.BaseParams = bp
		reqParams.Path = apc.URLPathReverseDae.S
		reqParams.Body = cos.MustMarshal(msg)
		reqParams.Header = http.Header{
			apc.HdrNodeID:      []string{nodeID},
			cos.HdrContentType: []string{cos.ContentJSON},
		}
	}
	err := reqParams.DoRequest()
	FreeRp(reqParams)
	return err
}
