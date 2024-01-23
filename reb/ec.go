// Package reb provides global cluster-wide rebalance upon adding/removing storage nodes.
/*
 * Copyright (c) 2018-2024, NVIDIA CORPORATION. All rights reserved.
 */
package reb

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"

	"github.com/NVIDIA/aistore/api/apc"
	"github.com/NVIDIA/aistore/cmn"
	"github.com/NVIDIA/aistore/cmn/cos"
	"github.com/NVIDIA/aistore/cmn/debug"
	"github.com/NVIDIA/aistore/cmn/nlog"
	"github.com/NVIDIA/aistore/core"
	"github.com/NVIDIA/aistore/core/meta"
	"github.com/NVIDIA/aistore/ec"
	"github.com/NVIDIA/aistore/fs"
	"github.com/NVIDIA/aistore/transport"
)

// High level overview of how EC rebalance works.
// 1. EC traverses only metafile(%mt) directories. A jogger per mountpath.
// 2. A jogger skips a metafile if:
//    - its `FullReplica` is not the local target ID
//    - its `FullReplica` equals the local target ID and HRW chooses local target
// 3. Otherwise, a jogger calculates a correct target using HRW and moves CT there
// 4. A target on receiving:
// 4.1. Preparation:
//      - Update metadata: fix `Daemons` and `FullReplica` fields
// 4.1. If the target has another CT of the same object and generation:
//      - move local CT to a working directory
// 4.2. If the target contains another CT of the same object and generation:
//      - save sent CT and metafile
// 4.3 If anything was moved to working directory at step 4.1:
//      - select a target that has no valid CT of the object
//      - moves the local CT to the selected target
// 4.3. Finalization:
//      - broadcast new metadata to all targets in `Daemons` field for them to
//        update their metafiles. Targets do not overwrite their metafiles with a new
//        one. They update only `Daemons` and `FullReplica` fields.

func (reb *Reb) runECjoggers() {
	var (
		wg             = &sync.WaitGroup{}
		availablePaths = fs.GetAvail()
		cfg            = cmn.GCO.Get()
		b              = reb.xctn().Bck()
	)
	for _, mi := range availablePaths {
		bck := cmn.Bck{Provider: apc.AIS}
		if b != nil {
			bck = cmn.Bck{Name: b.Name, Provider: apc.AIS, Ns: b.Ns}
		}
		wg.Add(1)
		go reb.jogEC(mi, &bck, wg)
	}
	for _, provider := range cfg.Backend.Providers {
		for _, mi := range availablePaths {
			bck := cmn.Bck{Provider: provider.Name}
			if b != nil {
				bck = cmn.Bck{Name: bck.Name, Provider: provider.Name, Ns: bck.Ns}
			}
			wg.Add(1)
			go reb.jogEC(mi, &bck, wg)
		}
	}
	wg.Wait()
}

// mountpath walker - walks through files in /meta/ directory
func (reb *Reb) jogEC(mi *fs.Mountpath, bck *cmn.Bck, wg *sync.WaitGroup) {
	defer wg.Done()
	opts := &fs.WalkOpts{
		Mi:       mi,
		CTs:      []string{fs.ECMetaType},
		Callback: reb.walkEC,
		Sorted:   false,
	}
	opts.Bck.Copy(bck)
	if err := fs.Walk(opts); err != nil {
		xreb := reb.xctn()
		if xreb.IsAborted() || xreb.Finished() {
			nlog.Infof("aborting traversal")
		} else {
			nlog.Warningf("failed to traverse, err: %v", err)
		}
	}
}

// Sends local CT along with EC metadata to default target.
// The CT is on a local drive and not loaded into SGL. Just read and send.
func (reb *Reb) sendFromDisk(ct *core.CT, meta *ec.Metadata, target *meta.Snode, workFQN ...string) (err error) {
	var (
		lom    *core.LOM
		roc    cos.ReadOpenCloser
		fqn    = ct.FQN()
		action = uint32(rebActRebCT)
	)
	debug.Assert(meta != nil)
	if len(workFQN) != 0 {
		fqn = workFQN[0]
		action = rebActMoveCT
	}
	// TODO: unify acquiring a reader for LOM and CT
	if ct.ContentType() == fs.ObjectType {
		lom = core.AllocLOM(ct.ObjectName())
		if err = lom.InitBck(ct.Bck().Bucket()); err != nil {
			core.FreeLOM(lom)
			return
		}
		lom.Lock(false)
		if err = lom.Load(false /*cache it*/, true /*locked*/); err != nil {
			lom.Unlock(false)
			core.FreeLOM(lom)
			return
		}
	} else {
		lom = nil // sending slice; TODO: rlock
	}

	// open
	if lom != nil {
		defer core.FreeLOM(lom)
		roc, err = lom.NewDeferROC()
	} else {
		roc, err = cos.NewFileHandle(fqn)
	}
	if err != nil {
		return
	}

	// transmit
	ntfn := stageNtfn{daemonID: core.T.SID(), stage: rebStageTraverse, rebID: reb.rebID.Load(), md: meta, action: action}
	o := transport.AllocSend()
	o.Hdr = transport.ObjHdr{ObjName: ct.ObjectName(), ObjAttrs: cmn.ObjAttrs{Size: meta.Size}}
	o.Hdr.Bck.Copy(ct.Bck().Bucket())
	if lom != nil {
		o.Hdr.ObjAttrs.CopyFrom(lom.ObjAttrs(), false /*skip cksum*/)
	}
	if meta.SliceID != 0 {
		o.Hdr.ObjAttrs.Size = ec.SliceSize(meta.Size, meta.Data)
	}
	reb.onAir.Inc()
	o.Hdr.Opaque = ntfn.NewPack(rebMsgEC)
	o.Callback = reb.transportECCB
	if err = reb.dm.Send(o, roc, target); err != nil {
		err = fmt.Errorf("failed to send slices to nodes [%s..]: %v", target.ID(), err)
		return
	}
	xreb := reb.xctn()
	xreb.OutObjsAdd(1, o.Hdr.ObjAttrs.Size)
	return
}

func (reb *Reb) transportECCB(_ *transport.ObjHdr, _ io.ReadCloser, _ any, _ error) {
	reb.onAir.Dec()
}

// Saves received CT to a local drive if needed:
//  1. Full object/replica is received
//  2. A CT is received and this target is not the default target (it
//     means that the CTs came from default target after EC had been rebuilt)
func (reb *Reb) saveCTToDisk(ntfn *stageNtfn, hdr *transport.ObjHdr, data io.Reader) error {
	cos.Assert(ntfn.md != nil)
	var (
		err error
		bck = meta.CloneBck(&hdr.Bck)
	)
	if err := bck.Init(core.T.Bowner()); err != nil {
		return err
	}
	md := ntfn.md.NewPack()
	if ntfn.md.SliceID != 0 {
		args := &ec.WriteArgs{Reader: data, MD: md, Xact: reb.xctn()}
		err = ec.WriteSliceAndMeta(hdr, args)
	} else {
		var lom *core.LOM
		lom, err = core.AllocLomFromHdr(hdr)
		if err == nil {
			args := &ec.WriteArgs{Reader: data, MD: md, Cksum: hdr.ObjAttrs.Cksum, Xact: reb.xctn()}
			err = ec.WriteReplicaAndMeta(lom, args)
		}
		core.FreeLOM(lom)
	}
	return err
}

// Used when slice conflict is detected: a target receives a new slice and it already
// has a slice of the same generation with different ID
func (*Reb) renameAsWorkFile(ct *core.CT) (string, error) {
	fqn := ct.Make(fs.WorkfileType)
	// Using os.Rename is safe as both CT and Workfile on the same mountpath
	if err := os.Rename(ct.FQN(), fqn); err != nil {
		return "", err
	}
	return fqn, nil
}

// Find a target that has either an obsolete slice or no slice of the object.
// Used to resolve the conflict: this target is the "main" one (has a full
// replica) but it also stores a slice of the object. So, the existing slice
// goes to any other _free_ target.
func (reb *Reb) findEmptyTarget(md *ec.Metadata, ct *core.CT, sender string) (*meta.Snode, error) {
	var (
		sliceCnt     = md.Data + md.Parity + 2
		smap         = reb.smap.Load()
		hrwList, err = smap.HrwTargetList(ct.Bck().MakeUname(ct.ObjectName()), sliceCnt)
	)
	if err != nil {
		return nil, err
	}
	for _, tsi := range hrwList {
		if tsi.ID() == sender || tsi.ID() == core.T.SID() {
			continue
		}
		remoteMD, err := ec.RequestECMeta(ct.Bucket(), ct.ObjectName(), tsi, core.T.DataClient())
		if remoteMD != nil && remoteMD.Generation < md.Generation {
			return tsi, nil
		}
		if remoteMD != nil && remoteMD.Generation == md.Generation {
			_, ok := md.Daemons[tsi.ID()]
			if !ok {
				// ct.ObjectName()[remoteMD.SliceID] not found (new slice md.SliceID)
				return tsi, nil
			}
		}
		if err != nil && cos.IsNotExist(err, 0) {
			return tsi, nil
		}
		if err != nil {
			nlog.Errorf("Failed to read metadata from %s: %v", tsi.StringEx(), err)
		}
	}
	return nil, errors.New("no _free_ targets")
}

// Check if this target has a metadata for the received CT
func detectLocalCT(req *stageNtfn, ct *core.CT) (*ec.Metadata, error) {
	if req.action == rebActMoveCT {
		// internal CT move after slice conflict - save always
		return nil, nil
	}
	if _, ok := req.md.Daemons[core.T.SID()]; !ok {
		return nil, nil
	}
	mdCT, err := core.NewCTFromBO(ct.Bck().Bucket(), ct.ObjectName(), core.T.Bowner(), fs.ECMetaType)
	if err != nil {
		return nil, err
	}
	locMD, err := ec.LoadMetadata(mdCT.FQN())
	if err != nil && os.IsNotExist(err) {
		err = nil
	}
	return locMD, err
}

// When a target receives a slice and the target has a slice with different ID:
// - move slice to a workfile directory
// - return Snode that must receive the local slice, and workfile path
// - the caller saves received CT to local drives, and then sends workfile
func (reb *Reb) renameLocalCT(req *stageNtfn, ct *core.CT, md *ec.Metadata) (
	workFQN string, moveTo *meta.Snode, err error) {
	if md == nil || req.action == rebActMoveCT {
		return
	}
	if md.SliceID == 0 || md.SliceID == req.md.SliceID || req.md.Generation != md.Generation {
		return
	}
	if workFQN, err = reb.renameAsWorkFile(ct); err != nil {
		return
	}
	if moveTo, err = reb.findEmptyTarget(md, ct, req.daemonID); err != nil {
		if errMv := os.Rename(workFQN, ct.FQN()); errMv != nil {
			nlog.Errorf("Error restoring slice: %v", errMv)
		}
	}
	return
}

func (reb *Reb) walkEC(fqn string, de fs.DirEntry) error {
	xreb := reb.xctn()
	if err := xreb.AbortErr(); err != nil {
		// notify `dir.Walk` to stop iterations
		nlog.Infoln(xreb.Name(), "walk-ec aborted", err)
		return err
	}

	if de.IsDir() {
		return nil
	}

	ct, err := core.NewCTFromFQN(fqn, core.T.Bowner())
	if err != nil {
		return nil
	}
	// do not touch directories for buckets with EC disabled (for now)
	if !ct.Bck().Props.EC.Enabled {
		return filepath.SkipDir
	}

	md, err := ec.LoadMetadata(fqn)
	if err != nil {
		nlog.Warningf("failed to load %q metadata: %v", fqn, err)
		return nil
	}

	// Skip a CT if this target is not the 'main' one
	if md.FullReplica != core.T.SID() {
		return nil
	}

	smap := reb.smap.Load()
	hrwTarget, err := smap.HrwHash2T(ct.Digest())
	if err != nil || hrwTarget.ID() == core.T.SID() {
		return err
	}

	// check if both slice/replica and metafile exist
	isReplica := md.SliceID == 0
	var fileFQN string
	if isReplica {
		fileFQN = ct.Make(fs.ObjectType)
	} else {
		fileFQN = ct.Make(fs.ECSliceType)
	}
	if err := cos.Stat(fileFQN); err != nil {
		nlog.Warningf("%s no CT for metadata[%d]: %s", core.T, md.SliceID, fileFQN)
		return nil
	}

	ct, err = core.NewCTFromFQN(fileFQN, core.T.Bowner())
	if err != nil {
		return nil
	}
	return reb.sendFromDisk(ct, md, hrwTarget)
}
