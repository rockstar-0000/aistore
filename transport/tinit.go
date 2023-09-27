// Package transport provides long-lived http/tcp connections for
// intra-cluster communications (see README for details and usage example).
/*
 * Copyright (c) 2018-2023, NVIDIA CORPORATION. All rights reserved.
 */
package transport

import (
	"container/heap"
	"os"
	"strconv"
	"time"

	"github.com/NVIDIA/aistore/cmn"
	"github.com/NVIDIA/aistore/cmn/cos"
	"github.com/NVIDIA/aistore/cmn/nlog"
	"github.com/NVIDIA/aistore/memsys"
)

// transport defaults
const (
	dfltBurstNum     = 128 // burst size (see: config.Transport.Burst)
	dfltTick         = time.Second
	dfltIdleTeardown = 4 * time.Second // (see config.Transport.IdleTeardown)
)

type global struct {
	statsTracker cos.StatsUpdater // aka stats.Trunner
	mm           *memsys.MMSA
}

var (
	g          global
	dfltMaxHdr int64 // memsys.PageSize or cluster-configurable (`config.Transport.MaxHeaderSize`)
	verbose    bool
)

func Init(st cos.StatsUpdater, config *cmn.Config) *StreamCollector {
	verbose = config.FastV(5 /*super-verbose*/, cos.SmoduleTransport)

	g.mm = memsys.PageMM()
	g.statsTracker = st

	nextSessionID.Store(100)
	for i := 0; i < numHmaps; i++ {
		hmaps[i] = make(hmap, 4)
	}

	dfltMaxHdr = dfltSizeHeader
	if config.Transport.MaxHeaderSize > 0 {
		dfltMaxHdr = int64(config.Transport.MaxHeaderSize)
	}
	// real stream collector
	gc = &collector{
		ctrlCh:  make(chan ctrl, 64),
		streams: make(map[string]*streamBase, 64),
		heap:    make([]*streamBase, 0, 64), // min-heap sorted by stream.time.ticks
	}
	gc.stopCh.Init()
	heap.Init(gc)

	sc = &StreamCollector{}
	return sc
}

func burst(config *cmn.Config) (burst int) {
	if burst = config.Transport.Burst; burst == 0 {
		burst = dfltBurstNum
	}
	if a := os.Getenv("AIS_STREAM_BURST_NUM"); a != "" {
		if burst64, err := strconv.ParseInt(a, 10, 0); err != nil {
			nlog.Errorln(err)
		} else {
			burst = int(burst64)
		}
	}
	return
}
