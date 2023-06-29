// Package mock provides a variety of mock implementations used for testing.
/*
 * Copyright (c) 2018-2023, NVIDIA CORPORATION. All rights reserved.
 */
package mock

import (
	"github.com/NVIDIA/aistore/cluster/meta"
	"github.com/NVIDIA/aistore/cmn/cos"
	"github.com/NVIDIA/aistore/stats"
)

type (
	StatsTracker struct{}
)

// interface guard
var _ stats.Tracker = (*StatsTracker)(nil)

func NewStatsTracker() stats.Tracker {
	return &StatsTracker{}
}

func (*StatsTracker) StartedUp() bool            { return true }
func (*StatsTracker) Get(string) int64           { return 0 }
func (*StatsTracker) IncErr(string)              {}
func (*StatsTracker) Inc(string)                 {}
func (*StatsTracker) Add(string, int64)          {}
func (*StatsTracker) AddMany(...cos.NamedVal64)  {}
func (*StatsTracker) RegMetrics(*meta.Snode)     {}
func (*StatsTracker) GetMetricNames() cos.StrKVs { return nil }
func (*StatsTracker) GetStats() *stats.Node      { return nil }
func (*StatsTracker) ResetStats(bool)            {}
func (*StatsTracker) IsPrometheus() bool         { return false }
