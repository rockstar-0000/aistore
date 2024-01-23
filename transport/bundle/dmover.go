// Package bundle provides multi-streaming transport with the functionality
// to dynamically (un)register receive endpoints, establish long-lived flows, and more.
/*
 * Copyright (c) 2018-2023, NVIDIA CORPORATION. All rights reserved.
 */
package bundle

import (
	"fmt"
	"io"
	"time"

	"github.com/NVIDIA/aistore/api/apc"
	"github.com/NVIDIA/aistore/cmn"
	"github.com/NVIDIA/aistore/cmn/atomic"
	"github.com/NVIDIA/aistore/cmn/cos"
	"github.com/NVIDIA/aistore/cmn/debug"
	"github.com/NVIDIA/aistore/cmn/nlog"
	"github.com/NVIDIA/aistore/core"
	"github.com/NVIDIA/aistore/core/meta"
	"github.com/NVIDIA/aistore/transport"
)

type (
	DataMover struct {
		data struct {
			client  transport.Client
			recv    transport.RecvObj
			streams *Streams
			trname  string
			net     string // one of cmn.KnownNetworks, empty defaults to cmn.NetIntraData
		}
		ack struct {
			client  transport.Client
			recv    transport.RecvObj
			streams *Streams
			trname  string
			net     string // one of cmn.KnownNetworks, empty defaults to cmn.NetIntraControl
		}
		xctn        core.Xact
		config      *cmn.Config
		compression string // enum { apc.CompressNever, ... }
		multiplier  int
		owt         cmn.OWT
		stage       struct {
			regred atomic.Bool
			opened atomic.Bool
			laterx atomic.Bool
		}
		sizePDU    int32
		maxHdrSize int32
	}
	// additional (and optional) params for new data mover
	Extra struct {
		RecvAck     transport.RecvObj
		Config      *cmn.Config
		Compression string
		Multiplier  int
		SizePDU     int32
		MaxHdrSize  int32
	}
)

var _ core.DM = (*DataMover)(nil) // via t.CopyObject()

// In re `owt` (below): data mover passes it to the target's `PutObject`
// to properly finalize received payload.
// For DMs that do not create new objects (e.g, rebalance) `owt` should
// be set to `OwtMigrateRepl`; all others are expected to have `OwtPut` (see e.g, CopyBucket).

func NewDataMover(trname string, recvCB transport.RecvObj, owt cmn.OWT, extra Extra) (*DataMover, error) {
	debug.Assert(extra.Config != nil)
	dm := &DataMover{config: extra.Config}
	dm.owt = owt
	dm.multiplier = extra.Multiplier
	dm.sizePDU, dm.maxHdrSize = extra.SizePDU, extra.MaxHdrSize
	switch extra.Compression {
	case "":
		dm.compression = apc.CompressNever
	case apc.CompressAlways, apc.CompressNever:
		dm.compression = extra.Compression
	default:
		return nil, fmt.Errorf("invalid compression %q", extra.Compression)
	}
	dm.data.trname, dm.data.recv = trname, recvCB
	if dm.data.net == "" {
		dm.data.net = cmn.NetIntraData
	}
	dm.data.client = transport.NewIntraDataClient()
	// ack
	if dm.ack.net == "" {
		dm.ack.net = cmn.NetIntraControl
	}
	dm.ack.recv = extra.RecvAck
	if !dm.useACKs() {
		return dm, nil
	}
	dm.ack.trname = "ack." + trname
	dm.ack.client = transport.NewIntraDataClient()
	return dm, nil
}

func (dm *DataMover) useACKs() bool { return dm.ack.recv != nil }
func (dm *DataMover) NetD() string  { return dm.data.net }
func (dm *DataMover) NetC() string  { return dm.ack.net }
func (dm *DataMover) OWT() cmn.OWT  { return dm.owt }

// xaction that drives and utilizes this data mover
func (dm *DataMover) SetXact(xctn core.Xact) { dm.xctn = xctn }
func (dm *DataMover) GetXact() core.Xact     { return dm.xctn }

// register user's receive-data (and, optionally, receive-ack) wrappers
func (dm *DataMover) RegRecv() (err error) {
	if err = transport.Handle(dm.data.trname, dm.wrapRecvData); err != nil {
		return
	}
	if dm.useACKs() {
		err = transport.Handle(dm.ack.trname, dm.wrapRecvACK)
	}
	dm.stage.regred.Store(true)
	return
}

func (dm *DataMover) Open() {
	dataArgs := Args{
		Net:    dm.data.net,
		Trname: dm.data.trname,
		Extra: &transport.Extra{
			Compression: dm.compression,
			Config:      dm.config,
			SizePDU:     dm.sizePDU,
			MaxHdrSize:  dm.maxHdrSize,
		},
		Ntype:        core.Targets,
		Multiplier:   dm.multiplier,
		ManualResync: true,
	}
	if dm.xctn != nil {
		dataArgs.Extra.SenderID = dm.xctn.ID()
	}
	dm.data.streams = New(dm.data.client, dataArgs)
	if dm.useACKs() {
		ackArgs := Args{
			Net:          dm.ack.net,
			Trname:       dm.ack.trname,
			Extra:        &transport.Extra{Config: dm.config},
			Ntype:        core.Targets,
			ManualResync: true,
		}
		if dm.xctn != nil {
			ackArgs.Extra.SenderID = dm.xctn.ID()
		}
		dm.ack.streams = New(dm.ack.client, ackArgs)
	}
	dm.stage.opened.Store(true)
}

func (dm *DataMover) String() string {
	s := "pre-or-post-"
	switch {
	case dm.stage.opened.Load():
		s = "open-"
	case dm.stage.regred.Load():
		s = "reg-" // not open yet or closed but not unreg-ed yet
	}
	if dm.data.streams.UsePDU() {
		return "dm-pdu-" + s + dm.data.streams.Trname()
	}
	return "dm-" + s + dm.data.streams.Trname()
}

// quiesce *local* Rx
func (dm *DataMover) Quiesce(d time.Duration) core.QuiRes {
	return dm.xctn.Quiesce(d, dm.quicb)
}

func (dm *DataMover) Close(err error) {
	if dm == nil {
		return
	}
	if !dm.stage.opened.CAS(true, false) {
		return
	}
	if err == nil && dm.xctn != nil && dm.xctn.IsAborted() {
		err = dm.xctn.AbortErr()
	}
	// nil: close gracefully via `fin`, otherwise abort
	dm.data.streams.Close(err == nil)
	if dm.useACKs() {
		dm.ack.streams.Close(err == nil)
	}
}

func (dm *DataMover) Abort() {
	dm.data.streams.Abort()
	if dm.useACKs() {
		dm.ack.streams.Abort()
	}
}

func (dm *DataMover) UnregRecv() {
	if dm == nil {
		return
	}
	if !dm.stage.regred.CAS(true, false) {
		return // e.g., 2PC (begin => abort) sequence with no Open
	}
	if dm.xctn != nil {
		dm.Quiesce(dm.config.Transport.QuiesceTime.D())
	}
	if err := transport.Unhandle(dm.data.trname); err != nil {
		nlog.Errorln(err)
	}
	if dm.useACKs() {
		if err := transport.Unhandle(dm.ack.trname); err != nil {
			nlog.Errorln(err)
		}
	}
}

func (dm *DataMover) Send(obj *transport.Obj, roc cos.ReadOpenCloser, tsi *meta.Snode) (err error) {
	err = dm.data.streams.Send(obj, roc, tsi)
	if err == nil && !transport.ReservedOpcode(obj.Hdr.Opcode) {
		dm.xctn.OutObjsAdd(1, obj.Size())
	}
	return
}

func (dm *DataMover) ACK(hdr *transport.ObjHdr, cb transport.ObjSentCB, tsi *meta.Snode) error {
	return dm.ack.streams.Send(&transport.Obj{Hdr: *hdr, Callback: cb}, nil, tsi)
}

func (dm *DataMover) Bcast(obj *transport.Obj, roc cos.ReadOpenCloser) error {
	return dm.data.streams.Send(obj, roc)
}

//
// private
//

func (dm *DataMover) quicb(_ time.Duration /*accum. sleep time*/) core.QuiRes {
	if dm.stage.laterx.CAS(true, false) {
		return core.QuiActive
	}
	return core.QuiInactiveCB
}

func (dm *DataMover) wrapRecvData(hdr *transport.ObjHdr, reader io.Reader, err error) error {
	if hdr.Bck.Name != "" && hdr.ObjName != "" && hdr.ObjAttrs.Size >= 0 {
		dm.xctn.InObjsAdd(1, hdr.ObjAttrs.Size)
	}
	// NOTE: in re (hdr.ObjAttrs.Size < 0) see transport.UsePDU()

	dm.stage.laterx.Store(true)
	return dm.data.recv(hdr, reader, err)
}

func (dm *DataMover) wrapRecvACK(hdr *transport.ObjHdr, reader io.Reader, err error) error {
	dm.stage.laterx.Store(true)
	return dm.ack.recv(hdr, reader, err)
}
