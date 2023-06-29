// Package transport provides streaming object-based transport over http for intra-cluster continuous
// intra-cluster communications (see README for details and usage example).
/*
 * Copyright (c) 2018-2021, NVIDIA CORPORATION. All rights reserved.
 */
package transport_test

import (
	"fmt"
	"net/http/httptest"
	"strconv"
	"sync"
	"testing"

	"github.com/NVIDIA/aistore/cmn/atomic"
	"github.com/NVIDIA/aistore/cmn/cos"
	"github.com/NVIDIA/aistore/cmn/mono"
	"github.com/NVIDIA/aistore/memsys"
	"github.com/NVIDIA/aistore/tools"
	"github.com/NVIDIA/aistore/tools/tassert"
	"github.com/NVIDIA/aistore/tools/tlog"
	"github.com/NVIDIA/aistore/transport"
)

func Example_msg() {
	receive := func(msg transport.Msg, err error) error {
		if !transport.ReservedOpcode(msg.Opcode) {
			fmt.Printf("%s...\n", string(msg.Body[:16]))
		}
		return nil
	}

	ts := httptest.NewServer(msgmux)
	defer ts.Close()

	trname := "dummy-msg"
	err := transport.HandleMsgStream(trname, receive)
	if err != nil {
		fmt.Println(err)
		return
	}
	httpclient := transport.NewIntraDataClient()
	url := ts.URL + transport.MsgURLPath(trname)
	stream := transport.NewMsgStream(httpclient, url, cos.GenTie())

	stream.Send(&transport.Msg{Body: []byte(lorem)})
	stream.Send(&transport.Msg{Body: []byte(duis)})
	stream.Send(&transport.Msg{Body: []byte(et)})
	stream.Send(&transport.Msg{Body: []byte(temporibus)})

	stream.Fin()

	// Output:
	// Lorem ipsum dolo...
	// Duis aute irure ...
	// Et harum quidem ...
	// Temporibus autem...
}

func Test_MsgDryRun(t *testing.T) {
	tools.CheckSkip(t, tools.SkipTestArgs{Long: true})
	t.Setenv("AIS_STREAM_DRY_RUN", "true")

	// fill in common shared read-only bug
	random := newRand(mono.NanoTime())
	buf, slab := memsys.PageMM().AllocSize(cos.MiB)
	defer slab.Free(buf)
	random.Read(buf)

	wg := &sync.WaitGroup{}
	num := atomic.NewInt64(0)
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			myrand := newRand(int64(idx * idx))
			tsize, prevsize, off := int64(0), int64(0), 0
			total := int64(cos.GiB * 4)
			if testing.Short() {
				total = cos.GiB
			}
			stream := transport.NewMsgStream(nil, "dry-msg"+strconv.Itoa(idx), cos.GenTie())
			for tsize < total {
				msize := myrand.Intn(memsys.PageSize - 64) // <= s.maxhdr, zero-length OK
				if off+msize > len(buf) {
					off = 0
				}
				msg := &transport.Msg{Body: buf[off : off+msize]}
				off += msize
				err := stream.Send(msg)
				tassert.CheckFatal(t, err)
				num.Inc()
				tsize += int64(msize)
				if tsize-prevsize > total/2 {
					prevsize = tsize
					tlog.Logf("%s: %s\n", stream, cos.ToSizeIEC(tsize, 0))
				}
			}
			stream.Fin()
		}(i)
	}
	wg.Wait()
	tlog.Logf("total messages: %d\n", num.Load())
}
