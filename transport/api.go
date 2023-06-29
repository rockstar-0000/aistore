// Package transport provides streaming object-based transport over http for intra-cluster continuous
// intra-cluster communications (see README for details and usage example).
/*
 * Copyright (c) 2018-2022, NVIDIA CORPORATION. All rights reserved.
 */
package transport

import (
	"fmt"
	"io"
	"math"
	"reflect"
	"time"
	"unsafe"

	"github.com/NVIDIA/aistore/api/apc"
	"github.com/NVIDIA/aistore/cmn"
	"github.com/NVIDIA/aistore/cmn/atomic"
	"github.com/NVIDIA/aistore/cmn/cos"
	"github.com/NVIDIA/aistore/cmn/debug"
	"github.com/NVIDIA/aistore/cmn/nlog"
	"github.com/NVIDIA/aistore/hk"
	"github.com/NVIDIA/aistore/memsys"
)

///////////////////
// object stream //
///////////////////

// range of 16 `Obj.Hdr.Opcode` and `Msg.Opcode` values
// reserved for _internal_ use
const (
	opcFin = iota + math.MaxUint16 - 16
	opcIdleTick
)

func ReservedOpcode(opc int) bool { return opc >= opcFin }

const (
	SizeUnknown = -1

	dfltSizePDU    = memsys.DefaultBufSize
	maxSizePDU     = memsys.MaxPageSlabSize
	dfltSizeHeader = memsys.PageSize
	maxSizeHeader  = memsys.MaxPageSlabSize
)

const sizeofh = int(unsafe.Sizeof(Obj{}))

type (
	// advanced usage: additional stream control
	Extra struct {
		Callback     ObjSentCB     // typical usage: to free SGLs, close files, etc.
		MMSA         *memsys.MMSA  // compression-related buffering
		Config       *cmn.Config   // (to optimize-out GCO.Get())
		Compression  string        // see CompressAlways, etc. enum
		SenderID     string        // e.g., xaction ID (optional)
		IdleTeardown time.Duration // when exceeded, causes PUT to terminate (and to renew upon the very next send)
		SizePDU      int32         // NOTE: 0(zero): no PDUs; must be below maxSizePDU; unknown size _requires_ PDUs
		MaxHdrSize   int32         // overrides `dfltMaxHdr` if specified
	}
	EndpointStats map[uint64]*Stats // all stats for a given (network, trname) endpoint indexed by session ID

	// object header
	ObjHdr struct {
		Bck      cmn.Bck
		ObjName  string
		SID      string       // sender node ID
		Opaque   []byte       // custom control (optional)
		ObjAttrs cmn.ObjAttrs // attributes/metadata of the object that's being transmitted
		Opcode   int          // (see reserved range above)
	}
	// object to transmit
	Obj struct {
		Reader   io.ReadCloser // reader (to read the object, and close when done)
		CmplArg  any           // optional context passed to the ObjSentCB callback
		Callback ObjSentCB     // called when the last byte is sent _or_ when the stream terminates (see term.reason)
		prc      *atomic.Int64 // private; if present, ref-counts so that we call ObjSentCB only once
		Hdr      ObjHdr
	}

	// object-sent callback that has the following signature can optionally be defined on a:
	// a) per-stream basis (via NewStream constructor - see Extra struct above)
	// b) for a given object that is being sent (for instance, to support a call-per-batch semantics)
	// Naturally, object callback "overrides" the per-stream one: when object callback is defined
	// (i.e., non-nil), the stream callback is ignored/skipped.
	// NOTE: if defined, the callback executes asynchronously as far as the sending part is concerned
	ObjSentCB func(ObjHdr, io.ReadCloser, any, error)

	Msg struct {
		SID    string
		Body   []byte
		Opcode int
	}

	// stream collector
	StreamCollector struct{}

	// Rx callbacks
	RecvObj func(hdr ObjHdr, object io.Reader, err error) error
	RecvMsg func(msg Msg, err error) error
)

///////////////////
// object stream //
///////////////////

func NewObjStream(client Client, dstURL, dstID string, extra *Extra) (s *Stream) {
	if extra == nil {
		extra = &Extra{Config: cmn.GCO.Get()}
	} else if extra.Config == nil {
		extra.Config = cmn.GCO.Get()
	}
	s = &Stream{streamBase: *newBase(client, dstURL, dstID, extra)}
	s.streamBase.streamer = s
	s.callback = extra.Callback
	if extra.Compressed() {
		s.initCompression(extra)
	}
	debug.Assert(s.usePDU() == extra.UsePDU())

	burst := burst(extra.Config)      // num objects the caller can post without blocking
	s.workCh = make(chan *Obj, burst) // Send Qeueue (SQ)
	s.cmplCh = make(chan cmpl, burst) // Send Completion Queue (SCQ)

	s.wg.Add(2)
	go s.sendLoop(dryrun()) // handle SQ
	go s.cmplLoop()         // handle SCQ

	gc.ctrlCh <- ctrl{&s.streamBase, true /* collect */}
	return
}

// Asynchronously send an object (transport.Obj) defined by its header and its reader.
//
// The sending pipeline is implemented as a pair (SQ, SCQ) where the former is a send
// queue realized as workCh, and the latter is a send completion queue (cmplCh).
// Together SQ and SCQ form a FIFO.
//
//   - header-only objects are supported; when there's no data to send (that is,
//     when the header's Dsize field is set to zero), the reader is not required and the
//     corresponding argument in Send() can be set to nil.
//   - object reader is *always* closed irrespectively of whether the Send() succeeds
//     or fails. On success, if send-completion (ObjSentCB) callback is provided
//     (i.e., non-nil), the closing is done by doCmpl().
//   - Optional reference counting is also done by (and in) the doCmpl, so that the
//     ObjSentCB gets called if and only when the refcount (if provided i.e., non-nil)
//     reaches zero.
//   - For every transmission of every object there's always an doCmpl() completion
//     (with its refcounting and reader-closing). This holds true in all cases including
//     network errors that may cause sudden and instant termination of the underlying
//     stream(s).
func (s *Stream) Send(obj *Obj) (err error) {
	debug.Assertf(len(obj.Hdr.Opaque) < len(s.maxhdr)-sizeofh, "(%d, %d)", len(obj.Hdr.Opaque), len(s.maxhdr))

	if err = s.startSend(obj); err != nil {
		s.doCmpl(obj, err) // take a shortcut
		return
	}

	// reader == nil iff is-header-only
	debug.Func(func() {
		if obj.Reader == nil {
			debug.Assert(obj.IsHeaderOnly())
		} else if obj.IsHeaderOnly() {
			val := reflect.ValueOf(obj.Reader)
			debug.Assert(val.IsNil(), obj.String())
		}
	})

	s.workCh <- obj
	if verbose {
		nlog.Infof("%s: send %s[sq=%d]", s, obj, len(s.workCh))
	}
	return
}

func (s *Stream) Fin() {
	_ = s.Send(&Obj{Hdr: ObjHdr{Opcode: opcFin}})
	s.wg.Wait()
}

////////////////////
// message stream //
////////////////////

func NewMsgStream(client Client, dstURL, dstID string) (s *MsgStream) {
	extra := &Extra{Config: cmn.GCO.Get()}
	s = &MsgStream{streamBase: *newBase(client, dstURL, dstID, extra)}
	s.streamBase.streamer = s

	burst := burst(extra.Config)      // num messages the caller can post without blocking
	s.workCh = make(chan *Msg, burst) // Send Qeueue or SQ

	s.wg.Add(1)
	go s.sendLoop(dryrun())

	gc.ctrlCh <- ctrl{&s.streamBase, true /* collect */}
	return
}

func (s *MsgStream) Send(msg *Msg) (err error) {
	debug.Assert(len(msg.Body) < len(s.maxhdr)-int(unsafe.Sizeof(Msg{})))
	if err = s.startSend(msg); err != nil {
		return
	}
	s.workCh <- msg
	if verbose {
		nlog.Infof("%s: send %s[sq=%d]", s, msg, len(s.workCh))
	}
	return
}

func (s *MsgStream) Fin() {
	_ = s.Send(&Msg{Opcode: opcFin})
	s.wg.Wait()
}

//////////////////////
// receive-side API //
//////////////////////

func HandleObjStream(trname string, rxObj RecvObj) error {
	h := &handler{trname: trname, rxObj: rxObj, hkName: ObjURLPath(trname)}
	return h.handle()
}

func HandleMsgStream(trname string, rxMsg RecvMsg) error {
	h := &handler{trname: trname, rxMsg: rxMsg, hkName: MsgURLPath(trname)}
	return h.handle()
}

func Unhandle(trname string) (err error) {
	mu.Lock()
	if h, ok := handlers[trname]; ok {
		delete(handlers, trname)
		mu.Unlock()
		hk.Unreg(h.hkName + hk.NameSuffix)
	} else {
		mu.Unlock()
		err = fmt.Errorf(cmn.FmtErrUnknown, "transport", "endpoint", trname)
	}
	return
}

////////////////////
// stats and misc //
////////////////////

func ObjURLPath(trname string) string { return _urlPath(apc.ObjStream, trname) }
func MsgURLPath(trname string) string { return _urlPath(apc.MsgStream, trname) }

func _urlPath(endp, trname string) string {
	if trname == "" {
		return cos.JoinWords(apc.Version, endp)
	}
	return cos.JoinWords(apc.Version, endp, trname)
}

func GetStats() (netstats map[string]EndpointStats, err error) {
	netstats = make(map[string]EndpointStats)
	mu.Lock()
	for trname, h := range handlers {
		eps := make(EndpointStats)
		f := func(key, value any) bool {
			out := &Stats{}
			uid := key.(uint64)
			in := value.(*Stats)
			out.Num.Store(in.Num.Load())
			out.Offset.Store(in.Offset.Load())
			out.Size.Store(in.Size.Load())
			eps[uid] = out
			return true
		}
		h.sessions.Range(f)
		netstats[trname] = eps
	}
	mu.Unlock()
	return
}
