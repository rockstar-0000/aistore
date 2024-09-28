//go:build statsd

// Package stats provides methods and functionality to register, track, log,
// and StatsD-notify statistics that, for the most part, include "counter" and "latency" kinds.
/*
 * Copyright (c) 2018-2024, NVIDIA CORPORATION. All rights reserved.
 */
package stats

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	ratomic "sync/atomic"
	"time"

	"github.com/NVIDIA/aistore/cmn"
	"github.com/NVIDIA/aistore/cmn/cos"
	"github.com/NVIDIA/aistore/cmn/debug"
	"github.com/NVIDIA/aistore/cmn/nlog"
	"github.com/NVIDIA/aistore/core/meta"
	"github.com/NVIDIA/aistore/memsys"
	"github.com/NVIDIA/aistore/stats/statsd"
	jsoniter "github.com/json-iterator/go"
)

type (
	metric = statsd.Metric // type alias

	// Stats are tracked via a map of stats names (key) and statsValue (values).
	statsValue struct {
		kind  string // enum { KindCounter, ..., KindSpecial }
		label struct {
			comm string // common part of the metric label (as in: <prefix> . comm . <suffix>)
			stpr string // StatsD _or_ Prometheus label (depending on build tag)
		}
		Value      int64 `json:"v,string"`
		numSamples int64 // (average latency over stats_time)
		cumulative int64 // REST API
	}

	coreStats struct {
		Tracker   map[string]*statsValue
		statsdC   *statsd.Client
		sgl       *memsys.SGL
		statsTime time.Duration
	}
)

// interface guard
var (
	_ Tracker = (*Prunner)(nil)
	_ Tracker = (*Trunner)(nil)
)

// empty stab (Prometheus only)
func initDfltlabel(*meta.Snode) {}

///////////////
// coreStats //
///////////////

// interface guard
var (
	_ json.Marshaler   = (*coreStats)(nil)
	_ json.Unmarshaler = (*coreStats)(nil)
)

func (s *coreStats) init(size int) {
	s.Tracker = make(map[string]*statsValue, size)

	s.sgl = memsys.PageMM().NewSGL(memsys.DefaultBufSize)
}

// NOTE: nil StatsD client means that we provide metrics to Prometheus (see below)
func (s *coreStats) statsdDisabled() bool { return s.statsdC == nil }

// empty stabs
func (*coreStats) promLock()   {}
func (*coreStats) promUnlock() {}

// init StatsD (not Prometheus)
func (s *coreStats) initStatsdOrProm(snode *meta.Snode, _ *runner) {
	var (
		port  = 8125  // StatsD default port, see https://github.com/etsy/stats
		probe = false // test-probe StatsD server at init time
	)
	if portStr := os.Getenv("AIS_STATSD_PORT"); portStr != "" {
		if portNum, err := cmn.ParsePort(portStr); err != nil {
			debug.AssertNoErr(err)
			nlog.Errorln(err)
		} else {
			port = portNum
		}
	}
	if probeStr := os.Getenv("AIS_STATSD_PROBE"); probeStr != "" {
		if probeBool, err := cos.ParseBool(probeStr); err != nil {
			nlog.Errorln(err)
		} else {
			probe = probeBool
		}
	}
	id := strings.ReplaceAll(snode.ID(), ":", "_") // ":" delineates name and value for StatsD
	statsD, err := statsd.New("localhost", port, "ais"+snode.Type()+"."+id, probe)
	if err != nil {
		nlog.Errorf("Starting up without StatsD: %v", err)
	} else {
		nlog.Infoln("Using StatsD")
	}
	s.statsdC = statsD
}

func (s *coreStats) updateUptime(d time.Duration) {
	v := s.Tracker[Uptime]
	ratomic.StoreInt64(&v.Value, d.Nanoseconds())
}

func (s *coreStats) MarshalJSON() ([]byte, error) { return jsoniter.Marshal(s.Tracker) }
func (s *coreStats) UnmarshalJSON(b []byte) error { return jsoniter.Unmarshal(b, &s.Tracker) }

func (s *coreStats) get(name string) (val int64) {
	v := s.Tracker[name]
	val = ratomic.LoadInt64(&v.Value)
	return
}

func (s *coreStats) update(nv cos.NamedVal64) {
	v, ok := s.Tracker[nv.Name]
	debug.Assertf(ok, "invalid metric name %q", nv.Name)
	switch v.kind {
	case KindLatency:
		ratomic.AddInt64(&v.numSamples, 1)
		fallthrough
	case KindThroughput:
		ratomic.AddInt64(&v.Value, nv.Value)
		ratomic.AddInt64(&v.cumulative, nv.Value)
	case KindCounter, KindSize, KindTotal:
		ratomic.AddInt64(&v.Value, nv.Value)
		// - non-empty suffix forces an immediate Tx with no aggregation (see below);
		// - suffix is an arbitrary string that can be defined at runtime;
		// - e.g. usage: per-mountpath error counters.
		if !s.statsdDisabled() && nv.NameSuffix != "" {
			s.statsdC.Send(v.label.comm+"."+nv.NameSuffix,
				1, metric{Type: statsd.Counter, Name: "count", Value: nv.Value})
		}
	default:
		debug.Assert(false, v.kind)
	}
}

// usage: log and StatsD Tx
func (s *coreStats) copyT(out copyTracker, diskLowUtil ...int64) bool {
	idle := true
	intl := max(int64(s.statsTime.Seconds()), 1)
	s.sgl.Reset()
	for name, v := range s.Tracker {
		switch v.kind {
		case KindLatency:
			var lat int64
			if num := ratomic.SwapInt64(&v.numSamples, 0); num > 0 {
				lat = ratomic.SwapInt64(&v.Value, 0) / num // NOTE: log average latency (nanoseconds) over the last "periodic.stats_time" interval
				if !ignore(name) {
					idle = false
				}
			}
			out[name] = copyValue{lat}

			// NOTE: if not zero, report StatsD latency (milliseconds) over the last "periodic.stats_time" interval
			millis := cos.DivRound(lat, int64(time.Millisecond))
			if !s.statsdDisabled() && millis > 0 {
				s.statsdC.AppMetric(metric{Type: statsd.Timer, Name: v.label.stpr, Value: float64(millis)}, s.sgl)
			}
		case KindThroughput:
			var throughput int64
			if throughput = ratomic.SwapInt64(&v.Value, 0); throughput > 0 {
				throughput /= intl // NOTE: log average throughput (bps) over the last "periodic.stats_time" interval
				if !ignore(name) {
					idle = false
				}
			}
			out[name] = copyValue{throughput}
			if !s.statsdDisabled() && throughput > 0 {
				fv := roundMBs(throughput)
				s.statsdC.AppMetric(metric{Type: statsd.Gauge, Name: v.label.stpr, Value: fv}, s.sgl)
			}
		case KindComputedThroughput:
			if throughput := ratomic.SwapInt64(&v.Value, 0); throughput > 0 {
				out[name] = copyValue{throughput}
				if !s.statsdDisabled() {
					fv := roundMBs(throughput)
					s.statsdC.AppMetric(metric{Type: statsd.Gauge, Name: v.label.stpr, Value: fv}, s.sgl)
				}
			}
		case KindCounter, KindSize, KindTotal:
			var (
				val     = ratomic.LoadInt64(&v.Value)
				changed bool
			)
			if prev, ok := out[name]; !ok || prev.Value != val {
				changed = true
			}
			if val > 0 {
				out[name] = copyValue{val}
				if changed && !ignore(name) {
					idle = false
				}
			}
			// StatsD iff changed
			if !s.statsdDisabled() && changed {
				if v.kind == KindCounter {
					s.statsdC.AppMetric(metric{Type: statsd.Counter, Name: v.label.stpr, Value: val}, s.sgl)
				} else {
					// target only suffix
					metricType := statsd.Counter
					if v.label.comm == "dl" {
						metricType = statsd.PersistentCounter
					}
					s.statsdC.AppMetric(metric{Type: metricType, Name: v.label.stpr, Value: float64(val)}, s.sgl)
				}
			}
		case KindGauge:
			val := ratomic.LoadInt64(&v.Value)
			out[name] = copyValue{val}
			if !s.statsdDisabled() {
				s.statsdC.AppMetric(metric{Type: statsd.Gauge, Name: v.label.stpr, Value: float64(val)}, s.sgl)
			}
			if isDiskUtilMetric(name) && val > diskLowUtil[0] {
				idle = false
			}
		default:
			out[name] = copyValue{ratomic.LoadInt64(&v.Value)}
		}
	}
	if !s.statsdDisabled() {
		s.statsdC.SendSGL(s.sgl)
	}
	return idle
}

// REST API what=stats query
// NOTE: not reporting zero counts
func (s *coreStats) copyCumulative(ctracker copyTracker) {
	for name, v := range s.Tracker {
		switch v.kind {
		case KindLatency:
			ctracker[name] = copyValue{ratomic.LoadInt64(&v.cumulative)}
		case KindThroughput:
			val := copyValue{ratomic.LoadInt64(&v.cumulative)}
			ctracker[name] = val

			// NOTE: effectively, add same-value metric that was never added/updated
			// via `runner.Add` and friends. Is OK to replace ".bps" suffix
			// as statsValue.cumulative _is_ the total size (aka, KindSize)
			n := name[:len(name)-3] + "size"
			ctracker[n] = val
		case KindCounter, KindSize, KindTotal:
			if val := ratomic.LoadInt64(&v.Value); val > 0 {
				ctracker[name] = copyValue{val}
			}
		default: // KindSpecial, KindComputedThroughput, KindGauge
			ctracker[name] = copyValue{ratomic.LoadInt64(&v.Value)}
		}
	}
}

func (s *coreStats) reset(errorsOnly bool) {
	if errorsOnly {
		for name, v := range s.Tracker {
			if IsErrMetric(name) {
				debug.Assert(v.kind == KindCounter || v.kind == KindSize, name)
				ratomic.StoreInt64(&v.Value, 0)
			}
		}
		return
	}

	for _, v := range s.Tracker {
		switch v.kind {
		case KindLatency:
			ratomic.StoreInt64(&v.numSamples, 0)
			fallthrough
		case KindThroughput:
			ratomic.StoreInt64(&v.Value, 0)
			ratomic.StoreInt64(&v.cumulative, 0)
		case KindCounter, KindSize, KindComputedThroughput, KindGauge, KindTotal:
			ratomic.StoreInt64(&v.Value, 0)
		default: // KindSpecial - do nothing
		}
	}
}

////////////
// runner //
////////////

// naming convention: ".n" for the count and ".ns" for duration (nanoseconds)
// compare with coreStats.initProm()
func (r *runner) reg(snode *meta.Snode, name, kind string, _ *Extra) {
	v := &statsValue{kind: kind}
	f := func(units string) string {
		return fmt.Sprintf("%s.%s.%s.%s", "ais"+snode.Type(), snode.ID(), v.label.comm, units)
	}
	debug.Assert(!strings.Contains(name, ":"), name)
	switch kind {
	case KindCounter:
		debug.Assert(strings.HasSuffix(name, ".n"), name)
		v.label.comm = strings.TrimSuffix(name, ".n")
		v.label.stpr = f("count")
	case KindTotal:
		debug.Assert(strings.HasSuffix(name, ".total"), name)
		v.label.comm = strings.TrimSuffix(name, ".total")
		v.label.stpr = f("total")
	case KindSize:
		debug.Assert(strings.HasSuffix(name, ".size"), name)
		v.label.comm = strings.TrimSuffix(name, ".size")
		v.label.stpr = f("bytes")
	case KindLatency:
		debug.Assert(strings.HasSuffix(name, ".ns"), name)
		v.label.comm = strings.TrimSuffix(name, ".ns")
		v.label.comm = strings.ReplaceAll(v.label.comm, ":", "_")
		v.label.stpr = f("ms")
	case KindThroughput, KindComputedThroughput:
		debug.Assert(strings.HasSuffix(name, ".bps"), name)
		v.label.comm = strings.TrimSuffix(name, ".bps")
		v.label.stpr = f("mbps")
	default:
		debug.Assert(kind == KindGauge || kind == KindSpecial)
		v.label.comm = name
		if name == Uptime {
			v.label.comm = "uptime"
			v.label.stpr = f("seconds")
		} else {
			v.label.stpr = fmt.Sprintf("%s.%s.%s", "ais"+snode.Type(), snode.ID(), v.label.comm)
		}
	}
	r.core.Tracker[name] = v
}

func (*runner) IsPrometheus() bool { return false }

func (r *runner) Stop(err error) {
	nlog.Infof("Stopping %s, err: %v", r.Name(), err)
	r.stopCh <- struct{}{}
	r.core.statsdC.Close()
	close(r.stopCh)
}
