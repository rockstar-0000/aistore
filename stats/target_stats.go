// Package stats provides methods and functionality to register, track, log,
// and StatsD-notify statistics that, for the most part, include "counter" and "latency" kinds.
/*
 * Copyright (c) 2018-2023, NVIDIA CORPORATION. All rights reserved.
 */
package stats

import (
	"fmt"
	"strconv"
	"strings"
	"time"
	"unsafe"

	"github.com/NVIDIA/aistore/cluster"
	"github.com/NVIDIA/aistore/cluster/meta"
	"github.com/NVIDIA/aistore/cmn"
	"github.com/NVIDIA/aistore/cmn/atomic"
	"github.com/NVIDIA/aistore/cmn/cos"
	"github.com/NVIDIA/aistore/cmn/debug"
	"github.com/NVIDIA/aistore/cmn/nlog"
	"github.com/NVIDIA/aistore/fs"
	"github.com/NVIDIA/aistore/ios"
	"github.com/NVIDIA/aistore/memsys"
	"github.com/NVIDIA/aistore/sys"
	"github.com/NVIDIA/aistore/transport"
)

// Naming Convention:
//
//	-> "*.n" - counter
//	-> "*.ns" - latency (nanoseconds)
//	-> "*.size" - size (bytes)
//	-> "*.bps" - throughput (in byte/s)
//	-> "*.id" - ID
const (
	// KindCounter & KindSize - always incremented
	GetColdCount = "get.cold.n"
	GetColdSize  = "get.cold.size"

	LruEvictCount = "lru.evict.n"
	LruEvictSize  = "lru.evict.size"

	CleanupStoreCount = "cleanup.store.n"
	CleanupStoreSize  = "cleanup.store.size"

	VerChangeCount = "ver.change.n"
	VerChangeSize  = "ver.change.size"

	// intra-cluster transmit & receive
	StreamsOutObjCount = transport.OutObjCount
	StreamsOutObjSize  = transport.OutObjSize
	StreamsInObjCount  = transport.InObjCount
	StreamsInObjSize   = transport.InObjSize

	// errors
	ErrCksumCount    = "err.cksum.n"
	ErrCksumSize     = "err.cksum.size"
	ErrMetadataCount = "err.md.n"
	ErrIOCount       = "err.io.n"
	// special
	RestartCount = "restart.n"

	// KindLatency
	PutLatency      = "put.ns"
	AppendLatency   = "append.ns"
	GetRedirLatency = "get.redir.ns"
	PutRedirLatency = "put.redir.ns"
	DownloadLatency = "dl.ns"

	// DSort
	DSortCreationReqCount    = "dsort.creation.req.n"
	DSortCreationRespCount   = "dsort.creation.resp.n"
	DSortCreationRespLatency = "dsort.creation.resp.ns"
	DSortExtractShardDskCnt  = "dsort.extract.shard.dsk.n"
	DSortExtractShardMemCnt  = "dsort.extract.shard.mem.n"
	DSortExtractShardSize    = "dsort.extract.shard.size" // uncompressed

	// Downloader
	DownloadSize = "dl.size"

	// KindThroughput
	GetThroughput = "get.bps" // bytes per second
	PutThroughput = "put.bps" // ditto

	// same as above via `.cumulative`
	GetSize = "get.size"
	PutSize = "put.size"
)

type (
	Trunner struct {
		t         cluster.NodeMemCap
		TargetCDF fs.TargetCDF `json:"cdf"`
		disk      ios.AllDiskStats
		xln       string
		runner    // the base (compare w/ Prunner)
		lines     []string
		mem       sys.MemStat
		xallRun   cluster.AllRunningInOut
		standby   bool
	}
)

const (
	minLogDiskUtil = 10 // skip logging idle disks

	numTargetStats = 48 // approx. initial
)

/////////////
// Trunner //
/////////////

// interface guard
var _ cos.Runner = (*Trunner)(nil)

func NewTrunner(t cluster.NodeMemCap) *Trunner { return &Trunner{t: t} }

func (r *Trunner) Run() error     { return r._run(r /*as statsLogger*/) }
func (r *Trunner) Standby(v bool) { r.standby = v }

func (r *Trunner) Init(t cluster.Target) *atomic.Bool {
	r.core = &coreStats{}

	r.core.init(numTargetStats)

	r.regCommon(t.Snode())

	r.ctracker = make(copyTracker, numTargetStats) // these two are allocated once and only used in serial context
	r.lines = make([]string, 0, 16)
	r.disk = make(ios.AllDiskStats, 16)

	config := cmn.GCO.Get()
	r.core.statsTime = config.Periodic.StatsTime.D()

	r.runner.name = "targetstats"
	r.runner.daemon = t

	r.runner.stopCh = make(chan struct{}, 4)

	r.core.initMetricClient(t.Snode(), &r.runner)

	r.sorted = make([]string, 0, numTargetStats)

	r.xallRun.Running = make([]string, 16)
	r.xallRun.Idle = make([]string, 16)

	return &r.runner.startedUp
}

func (r *Trunner) InitCDF() error {
	availableMountpaths := fs.GetAvail()
	r.TargetCDF.Mountpaths = make(map[string]*fs.CDF, len(availableMountpaths))
	for mpath := range availableMountpaths {
		r.TargetCDF.Mountpaths[mpath] = &fs.CDF{}
	}

	_, err, errCap := fs.CapRefresh(nil, &r.TargetCDF)
	if err != nil {
		return err
	}
	if errCap != nil {
		nlog.Errorln(r.t.String()+":", errCap)
	}
	return nil
}

func nameRbps(disk string) string { return "disk." + disk + ".read.bps" }
func nameRavg(disk string) string { return "disk." + disk + ".avg.rsize" }
func nameWbps(disk string) string { return "disk." + disk + ".write.bps" }
func nameWavg(disk string) string { return "disk." + disk + ".avg.wsize" }
func nameUtil(disk string) string { return "disk." + disk + ".util" }

// log vs idle logic
func isDiskMetric(name string) bool {
	return strings.HasPrefix(name, "disk.")
}
func isDiskUtilMetric(name string) bool {
	return isDiskMetric(name) && strings.HasSuffix(name, ".util")
}

// target-specific metrics, in addition to common and already added via regCommon()
func (r *Trunner) RegMetrics(node *meta.Snode) {
	r.reg(node, GetColdCount, KindCounter)
	r.reg(node, GetColdSize, KindSize)

	r.reg(node, LruEvictCount, KindCounter)
	r.reg(node, LruEvictSize, KindSize)

	r.reg(node, CleanupStoreCount, KindCounter)
	r.reg(node, CleanupStoreSize, KindSize)

	r.reg(node, VerChangeCount, KindCounter)
	r.reg(node, VerChangeSize, KindSize)

	r.reg(node, PutLatency, KindLatency)
	r.reg(node, AppendLatency, KindLatency)
	r.reg(node, GetRedirLatency, KindLatency)
	r.reg(node, PutRedirLatency, KindLatency)

	// bps
	r.reg(node, GetThroughput, KindThroughput)
	r.reg(node, PutThroughput, KindThroughput)

	r.reg(node, GetSize, KindSize)
	r.reg(node, PutSize, KindSize)

	// errors
	r.reg(node, ErrCksumCount, KindCounter)
	r.reg(node, ErrCksumSize, KindSize)

	r.reg(node, ErrMetadataCount, KindCounter)
	r.reg(node, ErrIOCount, KindCounter)

	// streams
	r.reg(node, StreamsOutObjCount, KindCounter)
	r.reg(node, StreamsOutObjSize, KindSize)
	r.reg(node, StreamsInObjCount, KindCounter)
	r.reg(node, StreamsInObjSize, KindSize)

	// special
	r.reg(node, RestartCount, KindCounter)

	// download
	r.reg(node, DownloadSize, KindSize)
	r.reg(node, DownloadLatency, KindLatency)

	// dsort
	r.reg(node, DSortCreationReqCount, KindCounter)
	r.reg(node, DSortCreationRespCount, KindCounter)
	r.reg(node, DSortCreationRespLatency, KindLatency)
	r.reg(node, DSortExtractShardDskCnt, KindCounter)
	r.reg(node, DSortExtractShardMemCnt, KindCounter)
	r.reg(node, DSortExtractShardSize, KindSize)

	// Prometheus
	r.core.initProm(node)
}

func (r *Trunner) RegDiskMetrics(node *meta.Snode, disk string) {
	s, n := r.core.Tracker, nameRbps(disk)
	if _, ok := s[n]; ok { // must be config.TestingEnv()
		return
	}
	r.reg(node, n, KindComputedThroughput)
	r.reg(node, nameWbps(disk), KindComputedThroughput)

	r.reg(node, nameRavg(disk), KindGauge)
	r.reg(node, nameWavg(disk), KindGauge)
	r.reg(node, nameUtil(disk), KindGauge)
}

func (r *Trunner) GetStats() (ds *Node) {
	ds = r.runner.GetStats()
	ds.TargetCDF = r.TargetCDF
	return
}

func (r *Trunner) log(now int64, uptime time.Duration, config *cmn.Config) {
	r.lines = r.lines[:0]

	// 1. collect disk stats and populate the tracker
	s := r.core
	fs.FillDiskStats(r.disk)
	for disk, stats := range r.disk {
		v := s.Tracker[nameRbps(disk)]
		v.Value = stats.RBps
		v = s.Tracker[nameRavg(disk)]
		v.Value = stats.Ravg
		v = s.Tracker[nameWbps(disk)]
		v.Value = stats.WBps
		v = s.Tracker[nameWavg(disk)]
		v.Value = stats.Wavg
		v = s.Tracker[nameUtil(disk)]
		v.Value = stats.Util
	}

	// 2 copy stats, reset latencies, send via StatsD if configured
	s.updateUptime(uptime)
	s.promLock()
	idle := s.copyT(r.ctracker, config.Disk.DiskUtilLowWM)
	s.promUnlock()

	if now >= r.next || !idle {
		s.sgl.Reset() // sharing w/ CoreStats.copyT
		r.ctracker.write(s.sgl, r.sorted, true /*target*/, idle)
		if s.sgl.Len() > 3 { // skip '{}'
			line := string(s.sgl.Bytes())
			if line != r.prev {
				r.lines = append(r.lines, line)
				r.prev = line
			}
		}
	}

	// 3. capacity
	var (
		cs, updated, err, errCap = fs.CapPeriodic(now, config, &r.TargetCDF)
	)
	if err != nil {
		nlog.Errorln(err)
	} else if errCap != nil {
		r.t.OOS(&cs)
	} else if updated && cs.PctMax > int32(config.Space.CleanupWM) {
		debug.Assert(!cs.IsOOS(), cs.String())
		errCap = cmn.NewErrCapExceeded(cs.TotalUsed, cs.TotalAvail+cs.TotalUsed, 0, config.Space.CleanupWM, cs.PctMax, false)
		r.t.OOS(&cs)
	}
	if (updated && now >= r.next) || errCap != nil {
		for mpath, fsCapacity := range r.TargetCDF.Mountpaths {
			s := fmt.Sprintf("%s: used %d%%", mpath, fsCapacity.Capacity.PctUsed)
			r.lines = append(r.lines, s)
			if config.TestingEnv() {
				// skipping likely identical
				break
			}
		}
	}

	// 4. append disk stats to log subject to (idle) filtering
	// see related: `ignoreIdle`
	r.logDiskStats(now)

	// 5. memory pressure
	_ = r.mem.Get()
	mm := r.t.PageMM()
	if p := mm.Pressure(&r.mem); p >= memsys.PressureHigh {
		r.lines = append(r.lines, mm.Str(&r.mem))
	}

	// 6. running xactions
	verbose := config.FastV(4, cos.SmoduleStats)
	if !idle {
		var ln string
		r.xallRun.Running = r.xallRun.Running[:0]
		r.xallRun.Idle = r.xallRun.Idle[:0]
		orig := r.xallRun.Idle
		if !verbose {
			r.xallRun.Idle = nil
		}
		r.t.GetAllRunning(&r.xallRun, true /*periodic*/)
		if !verbose {
			r.xallRun.Idle = orig
		}
		if len(r.xallRun.Running) > 0 {
			ln = "running: " + strings.Join(r.xallRun.Running, " ")
			if len(r.xallRun.Idle) > 0 {
				ln += ";  idle: " + strings.Join(r.xallRun.Idle, " ")
			}
		} else if len(r.xallRun.Idle) > 0 {
			ln += "idle: " + strings.Join(r.xallRun.Idle, " ")
		}
		if ln != "" && ln != r.xln {
			r.lines = append(r.lines, ln)
		}
		r.xln = ln
	}

	// 7. and, finally
	for _, ln := range r.lines {
		nlog.Infoln(ln)
	}

	if now > r.next {
		r.next = now + maxStatsLogInterval
	}
}

// log formatted disk stats:
// [ disk: read throughput, average read size, write throughput, average write size, disk utilization ]
// e.g.: [ sda: 94MiB/s, 68KiB, 25MiB/s, 21KiB, 82% ]
func (r *Trunner) logDiskStats(now int64) {
	for disk, stats := range r.disk {
		if stats.Util < minLogDiskUtil/2 || (stats.Util < minLogDiskUtil && now < r.next) {
			continue
		}

		rbps := cos.ToSizeIEC(stats.RBps, 0)
		wbps := cos.ToSizeIEC(stats.WBps, 0)
		ravg := cos.ToSizeIEC(stats.Ravg, 0)
		wavg := cos.ToSizeIEC(stats.Wavg, 0)
		l := len(disk) + len(rbps) + len(wbps) + len(ravg) + len(wavg) + 64
		buf := make([]byte, 0, l)
		buf = append(buf, disk...)
		buf = append(buf, ": "...)
		buf = append(buf, rbps...)
		buf = append(buf, "/s, "...)
		buf = append(buf, ravg...)
		buf = append(buf, ", "...)
		buf = append(buf, wbps...)
		buf = append(buf, "/s, "...)
		buf = append(buf, wavg...)
		buf = append(buf, ", "...)
		buf = append(buf, strconv.FormatInt(stats.Util, 10)...)
		buf = append(buf, "%"...)
		r.lines = append(r.lines, *(*string)(unsafe.Pointer(&buf)))
	}
}

func (r *Trunner) statsTime(newval time.Duration) {
	r.core.statsTime = newval
}

func (r *Trunner) standingBy() bool { return r.standby }
