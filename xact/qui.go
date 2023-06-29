// Package xact provides core functionality for the AIStore eXtended Actions (xactions).
/*
 * Copyright (c) 2018-2021, NVIDIA CORPORATION. All rights reserved.
 */
package xact

import (
	"time"

	"github.com/NVIDIA/aistore/cluster"
	"github.com/NVIDIA/aistore/cmn/atomic"
)

// common ref-counted quiescence
func RefcntQuiCB(refc *atomic.Int32, maxTimeout, totalSoFar time.Duration) cluster.QuiRes {
	if refc.Load() > 0 {
		return cluster.QuiActive
	}
	if totalSoFar > maxTimeout {
		return cluster.QuiTimeout
	}
	return cluster.QuiInactiveCB
}
