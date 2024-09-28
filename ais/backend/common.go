// Package backend contains implementation of various backend providers.
/*
 * Copyright (c) 2018-2024, NVIDIA CORPORATION. All rights reserved.
 */
package backend

import (
	"net/http"
	"time"

	"github.com/NVIDIA/aistore/api/apc"
	"github.com/NVIDIA/aistore/cmn"
	"github.com/NVIDIA/aistore/cmn/cos"
	"github.com/NVIDIA/aistore/cmn/debug"
	"github.com/NVIDIA/aistore/core"
	"github.com/NVIDIA/aistore/core/meta"
	"github.com/NVIDIA/aistore/fs"
	"github.com/NVIDIA/aistore/stats"
)

type base struct {
	provider string
	metrics  cos.StrKVs // this backend's metric names (below)
}

// NOTE: `stats.LatencyToCounter()` - a public helper that relies on the naming convention below
func (b *base) init(snode *meta.Snode, tr stats.Tracker) {
	prefix := b.provider
	if prefix == apc.AIS {
		prefix = apc.RemAIS
	}

	labels := cos.StrKVs{"backend": prefix}
	b.metrics = make(map[string]string, 12)

	// GET
	b.metrics[stats.GetCount] = prefix + "." + stats.GetCount
	b.metrics[stats.GetLatencyTotal] = prefix + "." + stats.GetLatencyTotal
	b.metrics[stats.GetE2ELatencyTotal] = prefix + "." + stats.GetE2ELatencyTotal
	b.metrics[stats.GetSize] = prefix + "." + stats.GetSize

	tr.RegExtMetric(snode,
		b.metrics[stats.GetCount],
		stats.KindCounter,
		&stats.Extra{
			Help:    "GET: total number of executed remote requests (cold GETs)",
			StrName: "remote_get_count",
			Labels:  labels,
		},
	)
	tr.RegExtMetric(snode,
		b.metrics[stats.GetLatencyTotal],
		stats.KindTotal,
		&stats.Extra{
			Help:    "GET: total cumulative time (nanoseconds) to execute cold GETs and store new object versions in-cluster",
			StrName: "remote_get_ns_total",
			Labels:  labels,
		},
	)
	tr.RegExtMetric(snode,
		b.metrics[stats.GetE2ELatencyTotal],
		stats.KindTotal,
		&stats.Extra{
			Help: "GET: total end-to-end time (nanoseconds) servicing remote requests; " +
				"includes: receiving request, executing cold-GET, storing new object version in-cluster, and transmitting response",
			StrName: "remote_e2e_get_ns_total",
			Labels:  labels,
		},
	)
	tr.RegExtMetric(snode,
		b.metrics[stats.GetSize],
		stats.KindSize,
		&stats.Extra{
			Help:    "GET: total cumulative size (bytes) of all cold-GET transactions",
			StrName: "remote_get_bytes_total",
			Labels:  labels},
	)

	// PUT
	b.metrics[stats.PutCount] = prefix + "." + stats.PutCount
	b.metrics[stats.PutLatencyTotal] = prefix + "." + stats.PutLatencyTotal
	b.metrics[stats.PutE2ELatencyTotal] = prefix + "." + stats.PutE2ELatencyTotal
	b.metrics[stats.PutSize] = prefix + "." + stats.PutSize

	tr.RegExtMetric(snode,
		b.metrics[stats.PutCount],
		stats.KindCounter,
		&stats.Extra{
			Help:    "PUT: total number of executed remote requests to a given backend",
			StrName: "remote_put_count",
			Labels:  labels,
		},
	)
	tr.RegExtMetric(snode,
		b.metrics[stats.PutLatencyTotal],
		stats.KindTotal,
		&stats.Extra{
			Help:    "PUT: total cumulative time (nanoseconds) to execute remote requests and store new object versions in-cluster",
			StrName: "remote_put_ns_total",
			Labels:  labels,
		},
	)
	tr.RegExtMetric(snode,
		b.metrics[stats.PutE2ELatencyTotal],
		stats.KindTotal,
		&stats.Extra{
			StrName: "remote_e2e_put_ns_total",
			Help: "PUT: total end-to-end time (nanoseconds) servicing remote requests; " +
				"includes: receiving PUT payload, storing it in-cluster, executing remote PUT, finalizing new in-cluster object",
			Labels: labels},
	)
	tr.RegExtMetric(snode,
		b.metrics[stats.PutSize],
		stats.KindSize,
		&stats.Extra{
			Help:    "PUT: total cumulative size (bytes) of all PUTs to a given remote backend",
			StrName: "remote_e2e_put_bytes_total",
			Labels:  labels,
		},
	)

	// HEAD
	b.metrics[stats.HeadCount] = prefix + "." + stats.HeadCount
	b.metrics[stats.HeadLatencyTotal] = prefix + "." + stats.HeadLatencyTotal

	tr.RegExtMetric(snode,
		b.metrics[stats.HeadCount],
		stats.KindCounter,
		&stats.Extra{
			Help:    "HEAD: total number of executed remote requests to a given backend",
			StrName: "remote_head_count",
			Labels:  labels,
		},
	)
	tr.RegExtMetric(snode,
		b.metrics[stats.HeadLatencyTotal],
		stats.KindTotal,
		&stats.Extra{
			Help:    "HEAD: total cumulative time (nanoseconds) to execute remote requests",
			StrName: "remote_head_ns_total",
			Labels:  labels,
		},
	)

	// version changed out-of-band
	b.metrics[stats.VerChangeCount] = prefix + "." + stats.VerChangeCount
	b.metrics[stats.VerChangeSize] = prefix + "." + stats.VerChangeSize

	tr.RegExtMetric(snode,
		b.metrics[stats.VerChangeCount],
		stats.KindCounter,
		&stats.Extra{
			Help:    "number of out-of-band updates (by a 3rd party performing remote PUTs outside this cluster)",
			StrName: "remote_ver_change_count",
			Labels:  labels,
		},
	)
	tr.RegExtMetric(snode,
		b.metrics[stats.VerChangeSize],
		stats.KindSize,
		&stats.Extra{
			Help:    "total cumulative size of objects that were updated out-of-band",
			StrName: "remote_ver_change_bytes_total",
			Labels:  labels,
		},
	)
}

func (b *base) Provider() string              { return b.provider }
func (b *base) MetricName(name string) string { return b.metrics[name] }

func (b *base) CreateBucket(_ *meta.Bck) (int, error) {
	return http.StatusNotImplemented, cmn.NewErrUnsupp("create", b.provider+" bucket")
}

func newErrInventory(provider string) error {
	return cmn.NewErrUnsupp("list "+provider+" backend objects via", "bucket inventory")
}

func (b *base) GetBucketInv(*meta.Bck, *core.LsoInvCtx) (int, error) {
	return 0, newErrInventory(b.provider)
}

func (b *base) ListObjectsInv(*meta.Bck, *apc.LsoMsg, *cmn.LsoRes, *core.LsoInvCtx) error {
	return newErrInventory(b.provider)
}

//
// common helpers and misc
//

func fmtTime(t time.Time) string { return t.Format(time.RFC3339) }

func calcPageSize(pageSize, maxPageSize int64) int64 {
	debug.Assert(pageSize >= 0, pageSize)
	if pageSize == 0 {
		return maxPageSize
	}
	return min(pageSize, maxPageSize)
}

func allocPutParams(res core.GetReaderResult, owt cmn.OWT) *core.PutParams {
	params := core.AllocPutParams()
	{
		params.WorkTag = fs.WorkfileColdget
		params.Reader = res.R
		params.OWT = owt
		params.Cksum = res.ExpCksum
		params.Size = res.Size
		params.Atime = time.Now()
		params.ColdGET = true
	}
	return params
}
