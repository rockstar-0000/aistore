// Package mirror provides local mirroring and replica management
/*
 * Copyright (c) 2018-2024, NVIDIA CORPORATION. All rights reserved.
 */
package mirror

import (
	"fmt"
	"sync"
	"time"

	"github.com/NVIDIA/aistore/api/apc"
	"github.com/NVIDIA/aistore/cmn"
	"github.com/NVIDIA/aistore/cmn/atomic"
	"github.com/NVIDIA/aistore/cmn/cos"
	"github.com/NVIDIA/aistore/cmn/debug"
	"github.com/NVIDIA/aistore/cmn/mono"
	"github.com/NVIDIA/aistore/cmn/nlog"
	"github.com/NVIDIA/aistore/core"
	"github.com/NVIDIA/aistore/core/meta"
	"github.com/NVIDIA/aistore/fs"
	"github.com/NVIDIA/aistore/fs/mpather"
	"github.com/NVIDIA/aistore/memsys"
	"github.com/NVIDIA/aistore/xact"
	"github.com/NVIDIA/aistore/xact/xreg"
)

type (
	putFactory struct {
		xreg.RenewBase
		xctn *XactPut
		lom  *core.LOM
	}
	XactPut struct {
		// implements core.Xact interface
		xact.DemandBase
		// runtime
		workers  *mpather.WorkerGroup
		workCh   chan core.LIF
		chanFull atomic.Int64
		// init
		mirror cmn.MirrorConf
		config *cmn.Config
	}
)

// interface guard
var (
	_ core.Xact      = (*XactPut)(nil)
	_ xreg.Renewable = (*putFactory)(nil)
)

////////////////
// putFactory //
////////////////

func (*putFactory) New(args xreg.Args, bck *meta.Bck) xreg.Renewable {
	p := &putFactory{RenewBase: xreg.RenewBase{Args: args, Bck: bck}, lom: args.Custom.(*core.LOM)}
	return p
}

func (p *putFactory) Start() error {
	lom := p.lom
	slab, err := core.T.PageMM().GetSlab(memsys.MaxPageSlabSize) // TODO: estimate
	debug.AssertNoErr(err)

	bck, mirror := lom.Bck(), lom.MirrorConf()
	if !mirror.Enabled {
		return fmt.Errorf("%s: mirroring disabled, nothing to do", bck)
	}
	if err = fs.ValidateNCopies(core.T.String(), int(mirror.Copies)); err != nil {
		nlog.Errorln(err)
		return err
	}
	r := &XactPut{mirror: *mirror, workCh: make(chan core.LIF, mirror.Burst)}

	//
	// target-local generation of a global UUID
	//
	div := uint64(xact.IdleDefault)
	beid, _, _ := xreg.GenBEID(div, p.Kind()+"|"+bck.MakeUname(""))
	if beid == "" {
		// is Ok (compare with x-archive, x-tco)
		beid = cos.GenUUID()
	}
	r.DemandBase.Init(beid, p.Kind(), bck, xact.IdleDefault)

	// joggers
	r.workers = mpather.NewWorkerGroup(&mpather.WorkerGroupOpts{
		Callback:  r.do,
		Slab:      slab,
		QueueSize: mirror.Burst,
	})
	p.xctn = r

	// run
	go r.Run(nil)
	return nil
}

func (*putFactory) Kind() string     { return apc.ActPutCopies }
func (p *putFactory) Get() core.Xact { return p.xctn }

func (p *putFactory) WhenPrevIsRunning(xprev xreg.Renewable) (xreg.WPR, error) {
	debug.Assertf(false, "%s vs %s", p.Str(p.Kind()), xprev) // xreg.usePrev() must've returned true
	return xreg.WprUse, nil
}

/////////////
// XactPut //
/////////////

// (one worker per mountpath)
func (r *XactPut) do(lom *core.LOM, buf []byte) {
	copies := int(lom.Bprops().Mirror.Copies)

	lom.Lock(true)
	size, err := addCopies(lom, copies, buf)
	lom.Unlock(true)

	if err != nil {
		r.AddErr(err, 5, cos.SmoduleMirror)
	} else {
		r.ObjsAdd(1, size)
	}
	r.DecPending() // (see IncPending below)
	core.FreeLOM(lom)
}

// control logic: stop and idle timer
// (LOMs get dispatched directly to workers)
func (r *XactPut) Run(*sync.WaitGroup) {
	var err error
	nlog.Infoln(r.Name())
	r.config = cmn.GCO.Get()
	r.workers.Run()
loop:
	for {
		select {
		case <-r.IdleTimer():
			r.waitPending()
			break loop
		case <-r.ChanAbort():
			break loop
		}
	}

	err = r.stop()
	if err != nil {
		r.AddErr(err)
	}
	r.Finish()
}

// main method
func (r *XactPut) Repl(lom *core.LOM) {
	debug.Assert(!r.Finished(), r.String())

	// ref-count on-demand, decrement via worker.Callback = r.do
	r.IncPending()
	chanFull, err := r.workers.PostLIF(lom)
	if err != nil {
		r.DecPending()
		r.Abort(fmt.Errorf("%s: %v", r, err))
	}
	if chanFull {
		r.chanFull.Inc()
	}
}

func (r *XactPut) waitPending() {
	const minsleep, longtime = 4 * time.Second, 30 * time.Second
	var (
		started     int64
		cnt, iniCnt int
		sleep       = max(cmn.Rom.MaxKeepalive(), minsleep)
	)
	if cnt = len(r.workCh); cnt == 0 {
		return
	}
	started, iniCnt = mono.NanoTime(), cnt
	// keep sleeping until the very end
	for cnt > 0 {
		r.IncPending()
		time.Sleep(sleep)
		r.DecPending()
		cnt = len(r.workCh)
	}
	if d := mono.Since(started); d > longtime {
		nlog.Infof("%s: took a while to finish %d pending copies: %v", r, iniCnt, d)
	}
}

func (r *XactPut) stop() (err error) {
	r.DemandBase.Stop()
	n := r.workers.Stop()
	if nn := drainWorkCh(r.workCh); nn > 0 {
		n += nn
	}
	if n > 0 {
		r.SubPending(n)
		err = fmt.Errorf("%s: dropped %d object%s", r, n, cos.Plural(n))
	}
	if cnt := r.chanFull.Load(); (cnt >= 10 && cnt <= 20) || (cnt > 0 && cmn.Rom.FastV(5, cos.SmoduleMirror)) {
		nlog.Errorln("work channel full (all mp workers)", r.String(), cnt)
	}
	return
}

func (r *XactPut) Snap() (snap *core.Snap) {
	snap = &core.Snap{}
	r.ToSnap(snap)

	snap.IdleX = r.IsIdle()
	return
}
