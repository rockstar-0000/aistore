// Package fs provides mountpath and FQN abstractions and methods to resolve/map stored content
/*
 * Copyright (c) 2018-2024, NVIDIA CORPORATION. All rights reserved.
 */
package fs

import (
	"container/heap"
	"context"

	"github.com/NVIDIA/aistore/cmn"
	"github.com/NVIDIA/aistore/cmn/debug"
	"github.com/NVIDIA/aistore/cmn/nlog"
	"golang.org/x/sync/errgroup"
)

// provides default low-level `jogger` to traverse a bucket - a poor-man's joggers of sorts
// for those (very few) clients that don't have their own custom implementation

type WalkBckOpts struct {
	ValidateCb walkFunc // should return filepath.SkipDir to skip directory without an error
	WalkOpts
}

// internals
type (
	joggerBck struct {
		workCh   chan *wbe
		mi       *Mountpath
		validate walkFunc
		ctx      context.Context
		opts     WalkOpts
	}
	wbe struct { // walk bck entry
		dirEntry DirEntry
		fqn      string
	}
	wbeInfo struct {
		dirEntry DirEntry
		fqn      string
		objName  string
		mpathIdx int
	}
	wbeHeap []wbeInfo
)

// lso and tests
func WalkBck(opts *WalkBckOpts) error {
	debug.Assert(opts.Mi == nil && opts.Sorted) // TODO: support `opts.Sorted == false`
	var (
		avail      = GetAvail()
		l          = len(avail)
		joggers    = make([]*joggerBck, l)
		group, ctx = errgroup.WithContext(context.Background())
		idx        int
	)
	for _, mi := range avail {
		workCh := make(chan *wbe, mpathQueueSize)
		jg := &joggerBck{
			workCh:   workCh,
			mi:       mi,
			validate: opts.ValidateCb,
			ctx:      ctx,
			opts:     opts.WalkOpts,
		}
		jg.opts.Callback = jg.cb // --> jg.validate --> opts.ValidateCb
		jg.opts.Mi = mi
		joggers[idx] = jg
		idx++
	}

	for i := range l {
		group.Go(joggers[i].walk)
	}
	group.Go(func() error {
		h := &wbeHeap{}
		heap.Init(h)

		for i := range l {
			if wbe, ok := <-joggers[i].workCh; ok {
				heap.Push(h, wbeInfo{mpathIdx: i, fqn: wbe.fqn, dirEntry: wbe.dirEntry})
			}
		}
		for h.Len() > 0 {
			v := heap.Pop(h)
			info := v.(wbeInfo)
			if err := opts.Callback(info.fqn, info.dirEntry); err != nil {
				return err
			}
			if wbe, ok := <-joggers[info.mpathIdx].workCh; ok {
				heap.Push(h, wbeInfo{mpathIdx: info.mpathIdx, fqn: wbe.fqn, dirEntry: wbe.dirEntry})
			}
		}
		return nil
	})

	return group.Wait()
}

///////////////
// joggerBck //
///////////////

func (j *joggerBck) walk() (err error) {
	if err = j.opts.Mi.CheckFS(); err != nil {
		nlog.Errorln(err)
		mfs.hc.FSHC(err, j.opts.Mi, "")
	} else {
		err = Walk(&j.opts)
	}
	close(j.workCh)
	return err
}

func (j *joggerBck) cb(fqn string, de DirEntry) error {
	const tag = "fs-walk-bck-mpath"
	select {
	case <-j.ctx.Done():
		return cmn.NewErrAborted(j.mi.String(), tag, nil)
	default:
		break
	}
	if j.validate != nil {
		if err := j.validate(fqn, de); err != nil {
			// If err != filepath.SkipDir, the Walk will propagate the error to group.Go.
			// Context will be canceled, which then will terminate all running goroutines.
			return err
		}
	}
	if de.IsDir() {
		return nil
	}
	select {
	case <-j.ctx.Done():
		return cmn.NewErrAborted(j.mi.String(), tag, nil)
	case j.workCh <- &wbe{de, fqn}:
		return nil
	}
}

/////////////
// wbeHeap //
/////////////

func (h wbeHeap) Len() int           { return len(h) }
func (h wbeHeap) Less(i, j int) bool { return h[i].objName < h[j].objName }
func (h wbeHeap) Swap(i, j int)      { h[i], h[j] = h[j], h[i] }

func (h *wbeHeap) Push(x any) {
	var (
		parsed ParsedFQN
		info   = x.(wbeInfo)
	)
	debug.Assert(info.objName == "")
	if err := parsed.Init(info.fqn); err != nil {
		return
	}
	info.objName = parsed.ObjName
	*h = append(*h, info)
}

func (h *wbeHeap) Pop() any {
	old := *h
	n := len(old)
	x := old[n-1]
	*h = old[0 : n-1]
	return x
}
