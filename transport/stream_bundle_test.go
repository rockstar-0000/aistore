// Package transport provides streaming object-based transport over http for intra-cluster continuous
// intra-cluster communications (see README for details and usage example).
/*
 * Copyright (c) 2018-2021, NVIDIA CORPORATION. All rights reserved.
 */
package transport_test

import (
	"fmt"
	"io"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/NVIDIA/aistore/api/apc"
	"github.com/NVIDIA/aistore/cluster/meta"
	"github.com/NVIDIA/aistore/cmn"
	"github.com/NVIDIA/aistore/cmn/atomic"
	"github.com/NVIDIA/aistore/cmn/cos"
	"github.com/NVIDIA/aistore/cmn/mono"
	"github.com/NVIDIA/aistore/memsys"
	"github.com/NVIDIA/aistore/tools/tassert"
	"github.com/NVIDIA/aistore/tools/tlog"
	"github.com/NVIDIA/aistore/transport"
	"github.com/NVIDIA/aistore/transport/bundle"
)

type (
	sowner     struct{}
	slisteners struct{}
)

var (
	smap      meta.Smap
	listeners slisteners
)

func (*sowner) Get() *meta.Smap               { return &smap }
func (*sowner) Listeners() meta.SmapListeners { return &listeners }

func (*slisteners) Reg(meta.Slistener)   {}
func (*slisteners) Unreg(meta.Slistener) {}

func Test_Bundle(t *testing.T) {
	tests := []struct {
		name string
		nvs  cos.StrKVs
	}{
		{
			name: "not-compressed",
			nvs: cos.StrKVs{
				"compression": apc.CompressNever,
			},
		},
		{
			name: "not-compressed-unsized",
			nvs: cos.StrKVs{
				"compression": apc.CompressNever,
				"unsized":     "yes",
			},
		},
	}
	if !testing.Short() {
		testsLong := []struct {
			name string
			nvs  cos.StrKVs
		}{
			{
				name: "compress-block-1M",
				nvs: cos.StrKVs{
					"compression": apc.CompressAlways,
					"block":       "1MiB",
				},
			},
			{
				name: "compress-block-256K",
				nvs: cos.StrKVs{
					"compression": apc.CompressAlways,
					"block":       "256KiB",
				},
			},
			{
				name: "compress-block-256K-unsized",
				nvs: cos.StrKVs{
					"compression": apc.CompressAlways,
					"block":       "256KiB",
					"unsized":     "yes",
				},
			},
		}
		tests = append(tests, testsLong...)
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			testBundle(t, test.nvs)
			time.Sleep(time.Second)
		})
	}
}

func testBundle(t *testing.T, nvs cos.StrKVs) {
	var (
		numCompleted atomic.Int64
		mmsa, _      = memsys.NewMMSA("bundle")
		network      = cmn.NetIntraData
		trname       = "bundle" + nvs["block"]
		tss          = make([]*httptest.Server, 0, 32)
		lsnode       = meta.Snode{DaeID: "local"}
	)
	smap.Tmap = make(meta.NodeMap, 100)
	smap.Tmap[lsnode.ID()] = &lsnode
	for i := 0; i < 10; i++ {
		ts := httptest.NewServer(objmux)
		tss = append(tss, ts)
		addTarget(&smap, ts, i)
	}
	defer func() {
		for _, ts := range tss {
			ts.Close()
		}
		mmsa.Terminate(false)
	}()
	smap.Version = 1

	receive := func(hdr transport.ObjHdr, objReader io.Reader, err error) error {
		if err != nil && !cos.IsEOF(err) {
			tassert.CheckFatal(t, err)
		}
		written, _ := io.Copy(io.Discard, objReader)
		cos.Assert(written == hdr.ObjAttrs.Size || hdr.IsUnsized())
		return nil
	}
	callback := func(_ transport.ObjHdr, _ io.ReadCloser, _ any, _ error) {
		numCompleted.Inc()
	}

	err := transport.HandleObjStream(trname, receive) // URL = /v1/transport/10G
	tassert.CheckFatal(t, err)
	defer transport.Unhandle(trname)

	var (
		httpclient     = transport.NewIntraDataClient()
		sowner         = &sowner{}
		random         = newRand(mono.NanoTime())
		wbuf, slab     = mmsa.Alloc()
		extra          = &transport.Extra{Compression: nvs["compression"], MMSA: mmsa}
		size, prevsize int64
		multiplier     = int(random.Int63()%13) + 4
		num            int
		usePDU         bool
	)
	if nvs["compression"] != apc.CompressNever {
		v, _ := cos.ParseSize(nvs["block"], cos.UnitsIEC)
		cos.Assert(v == cos.MiB*4 || v == cos.MiB || v == cos.KiB*256 || v == cos.KiB*64)
		config := cmn.GCO.BeginUpdate()
		config.Transport.LZ4BlockMaxSize = cos.SizeIEC(v)
		cmn.GCO.CommitUpdate(config)
		if err := config.Transport.Validate(); err != nil {
			tassert.CheckFatal(t, err)
		}
	}
	if _, usePDU = nvs["unsized"]; usePDU {
		extra.SizePDU = memsys.DefaultBufSize
	}
	_, _ = random.Read(wbuf)
	sb := bundle.New(sowner, &lsnode, httpclient,
		bundle.Args{Net: network, Trname: trname, Multiplier: multiplier, Extra: extra})
	var numGs int64 = 6
	if testing.Short() {
		numGs = 1
	}
	for size < cos.GiB*numGs {
		var err error
		hdr := genRandomHeader(random, usePDU)
		objSize := hdr.ObjAttrs.Size
		if num%7 == 0 {
			objSize, hdr.ObjAttrs.Size = 0, 0
			err = sb.Send(&transport.Obj{Hdr: hdr, Callback: callback}, nil)
		} else {
			reader := &randReader{buf: wbuf, hdr: hdr, slab: slab, clone: true} // FIXME: multiplier reopen
			if hdr.IsUnsized() {
				reader.offEOF = int64(random.Int31()>>1) + 1
				objSize = reader.offEOF
			}
			err = sb.Send(&transport.Obj{Hdr: hdr, Callback: callback}, reader)
		}
		if err != nil {
			t.Fatalf("%s: exiting with err [%v]\n", sb, err)
		}
		num++
		size += objSize
		if size-prevsize >= cos.GiB {
			tlog.Logf("%s: %d GiB\n", sb, size/cos.GiB)
			prevsize = size
		}
	}
	sb.Close(true /* gracefully */)
	stats := sb.GetStats()

	slab.Free(wbuf)

	if nvs["compression"] != apc.CompressNever {
		for id, tstat := range stats {
			fmt.Printf("send$ %s/%s: offset=%d, num=%d(%d), compression-ratio=%.2f\n",
				id, trname, tstat.Offset.Load(), tstat.Num.Load(), num, tstat.CompressionRatio())
		}
	} else {
		for id, tstat := range stats {
			fmt.Printf("send$ %s/%s: offset=%d, num=%d(%d)\n",
				id, trname, tstat.Offset.Load(), tstat.Num.Load(), num)
		}
	}
	fmt.Printf("send$: num-sent=%d, num-completed=%d\n", num, numCompleted.Load())
}

func addTarget(smap *meta.Smap, ts *httptest.Server, i int) {
	netinfo := meta.NetInfo{URL: ts.URL}
	tid := "t_" + strconv.FormatInt(int64(i), 10)
	smap.Tmap[tid] = &meta.Snode{PubNet: netinfo, ControlNet: netinfo, DataNet: netinfo}
}
