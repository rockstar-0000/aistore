// Package etl provides utilities to initialize and use transformation pods.
/*
 * Copyright (c) 2018-2024, NVIDIA CORPORATION. All rights reserved.
 */
package etl

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"time"

	"github.com/NVIDIA/aistore/api/apc"
	"github.com/NVIDIA/aistore/cmn"
	"github.com/NVIDIA/aistore/cmn/cos"
	"github.com/NVIDIA/aistore/cmn/debug"
	"github.com/NVIDIA/aistore/cmn/nlog"
	"github.com/NVIDIA/aistore/core"
	"github.com/NVIDIA/aistore/core/meta"
	"github.com/NVIDIA/aistore/memsys"
)

type (
	CommStats interface {
		ObjCount() int64
		InBytes() int64
		OutBytes() int64
	}

	// Communicator is responsible for managing communications with local ETL container.
	// It listens to cluster membership changes and terminates ETL container, if need be.
	Communicator interface {
		meta.Slistener

		Name() string
		Xact() core.Xact
		PodName() string
		SvcName() string

		String() string

		// InlineTransform uses one of the two ETL container endpoints:
		//  - Method "PUT", Path "/"
		//  - Method "GET", Path "/bucket/object"
		InlineTransform(w http.ResponseWriter, r *http.Request, bck *meta.Bck, objName string) error

		// OfflineTransform interface implementations realize offline ETL.
		// OfflineTransform is driven by `OfflineDP` - not to confuse
		// with GET requests from users (such as training models and apps)
		// to perform on-the-fly transformation.
		OfflineTransform(bck *meta.Bck, objName string, timeout time.Duration) (cos.ReadCloseSizer, error)
		Stop()

		CommStats
	}

	baseComm struct {
		listener meta.Slistener
		boot     *etlBootstrapper
	}
	pushComm struct {
		baseComm
		command []string
	}
	redirectComm struct {
		baseComm
	}
	revProxyComm struct {
		baseComm
		rp *httputil.ReverseProxy
	}

	// TODO: Generalize and move to `cos` package
	cbWriter struct {
		w       io.Writer
		writeCb func(int)
	}
)

// interface guard
var (
	_ Communicator = (*pushComm)(nil)
	_ Communicator = (*redirectComm)(nil)
	_ Communicator = (*revProxyComm)(nil)

	_ io.Writer = (*cbWriter)(nil)
)

//////////////
// baseComm //
//////////////

func newCommunicator(listener meta.Slistener, boot *etlBootstrapper) Communicator {
	switch boot.msg.CommTypeX {
	case Hpush, HpushStdin:
		pc := &pushComm{}
		pc.listener, pc.boot = listener, boot
		if boot.msg.CommTypeX == HpushStdin { // io://
			pc.command = boot.originalCommand
		}
		return pc
	case Hpull:
		rc := &redirectComm{}
		rc.listener, rc.boot = listener, boot
		return rc
	case Hrev:
		rp := &revProxyComm{}
		rp.listener, rp.boot = listener, boot

		transformerURL, err := url.Parse(boot.uri)
		debug.AssertNoErr(err)
		revProxy := &httputil.ReverseProxy{
			Director: func(req *http.Request) {
				// Replacing the `req.URL` host with ETL container host
				req.URL.Scheme = transformerURL.Scheme
				req.URL.Host = transformerURL.Host
				req.URL.RawQuery = pruneQuery(req.URL.RawQuery)
				if _, ok := req.Header["User-Agent"]; !ok {
					// Explicitly disable `User-Agent` so it's not set to default value.
					req.Header.Set("User-Agent", "")
				}
			},
		}
		rp.rp = revProxy
		return rp
	}

	debug.Assert(false, "unknown comm-type '"+boot.msg.CommTypeX+"'")
	return nil
}

func (c *baseComm) Name() string    { return c.boot.originalPodName }
func (c *baseComm) PodName() string { return c.boot.pod.Name }
func (c *baseComm) SvcName() string { return c.boot.pod.Name /*same as pod name*/ }

func (c *baseComm) ListenSmapChanged() { c.listener.ListenSmapChanged() }

func (c *baseComm) String() string {
	return fmt.Sprintf("%s[%s]-%s", c.boot.originalPodName, c.boot.xctn.ID(), c.boot.msg.CommTypeX)
}

func (c *baseComm) Xact() core.Xact { return c.boot.xctn }
func (c *baseComm) ObjCount() int64 { return c.boot.xctn.Objs() }
func (c *baseComm) InBytes() int64  { return c.boot.xctn.InBytes() }
func (c *baseComm) OutBytes() int64 { return c.boot.xctn.OutBytes() }

func (c *baseComm) Stop() { c.boot.xctn.Finish() }

func (c *baseComm) getWithTimeout(url string, size int64, timeout time.Duration) (r cos.ReadCloseSizer, err error) {
	if err := c.boot.xctn.AbortErr(); err != nil {
		return nil, err
	}

	var (
		req    *http.Request
		resp   *http.Response
		cancel func()
	)
	if timeout != 0 {
		var ctx context.Context
		ctx, cancel = context.WithTimeout(context.Background(), timeout)
		req, err = http.NewRequestWithContext(ctx, http.MethodGet, url, http.NoBody)
	} else {
		req, err = http.NewRequest(http.MethodGet, url, http.NoBody)
	}
	if err == nil {
		resp, err = core.T.DataClient().Do(req) //nolint:bodyclose // Closed by the caller.
	}
	if err != nil {
		if cancel != nil {
			cancel()
		}
		return nil, err
	}

	return cos.NewReaderWithArgs(cos.ReaderArgs{
		R:      resp.Body,
		Size:   resp.ContentLength,
		ReadCb: func(n int, err error) { c.boot.xctn.InObjsAdd(0, int64(n)) },
		DeferCb: func() {
			if cancel != nil {
				cancel()
			}
			c.boot.xctn.InObjsAdd(1, 0)
			c.boot.xctn.OutObjsAdd(1, size) // see also: `coi.objsAdd`
		},
	}), nil
}

//////////////
// pushComm: implements (Hpush | HpushStdin)
//////////////

func (pc *pushComm) doRequest(bck *meta.Bck, lom *core.LOM, timeout time.Duration) (r cos.ReadCloseSizer, err error) {
	var errCode int
	if err := lom.InitBck(bck.Bucket()); err != nil {
		return nil, err
	}

	lom.Lock(false)
	r, errCode, err = pc.do(lom, timeout)
	lom.Unlock(false)

	if err != nil && cos.IsNotExist(err, errCode) && bck.IsRemote() {
		_, err = core.T.GetCold(context.Background(), lom, cmn.OwtGetLock)
		if err != nil {
			return nil, err
		}
		lom.Lock(false)
		r, _, err = pc.do(lom, timeout)
		lom.Unlock(false)
	}
	return
}

func (pc *pushComm) do(lom *core.LOM, timeout time.Duration) (_ cos.ReadCloseSizer, errCode int, err error) {
	var (
		body   io.ReadCloser
		cancel func()
		req    *http.Request
		resp   *http.Response
		u      string
	)
	if err := pc.boot.xctn.AbortErr(); err != nil {
		return nil, 0, err
	}
	if err := lom.Load(false /*cache it*/, true /*locked*/); err != nil {
		return nil, 0, err
	}
	size := lom.SizeBytes()

	switch pc.boot.msg.ArgTypeX {
	case ArgTypeDefault, ArgTypeURL:
		// to remove the following assert (and the corresponding limitation):
		// - container must be ready to receive complete bucket name including namespace
		// - see `bck.AddToQuery` and api/bucket.go for numerous examples
		debug.Assertf(lom.Bck().Ns.IsGlobal(), lom.Bck().Cname("")+" - bucket with namespace")
		u = pc.boot.uri + "/" + lom.Bck().Name + "/" + lom.ObjName

		fh, err := cos.NewFileHandle(lom.FQN)
		if err != nil {
			return nil, 0, err
		}
		body = fh
	case ArgTypeFQN:
		body = http.NoBody
		u = cos.JoinPath(pc.boot.uri, url.PathEscape(lom.FQN)) // compare w/ rc.redirectURL()
	default:
		debug.Assert(false, "unexpected msg type:", pc.boot.msg.ArgTypeX) // is validated at construction time
	}

	if timeout != 0 {
		var ctx context.Context
		ctx, cancel = context.WithTimeout(context.Background(), timeout)
		req, err = http.NewRequestWithContext(ctx, http.MethodPut, u, body)
	} else {
		req, err = http.NewRequest(http.MethodPut, u, body)
	}
	if err != nil {
		cos.Close(body)
		goto finish
	}

	if len(pc.command) != 0 {
		// HpushStdin case
		q := req.URL.Query()
		q["command"] = []string{"bash", "-c", strings.Join(pc.command, " ")}
		req.URL.RawQuery = q.Encode()
	}
	req.ContentLength = size
	req.Header.Set(cos.HdrContentType, cos.ContentBinary)

	//
	// Do it
	//
	resp, err = core.T.DataClient().Do(req) //nolint:bodyclose // Closed by the caller.

finish:
	if err != nil {
		if cancel != nil {
			cancel()
		}
		if resp != nil {
			errCode = resp.StatusCode
		}
		return nil, errCode, err
	}
	args := cos.ReaderArgs{
		R:      resp.Body,
		Size:   resp.ContentLength,
		ReadCb: func(n int, err error) { pc.boot.xctn.InObjsAdd(0, int64(n)) },
		DeferCb: func() {
			if cancel != nil {
				cancel()
			}
			pc.boot.xctn.InObjsAdd(1, 0)
			pc.boot.xctn.OutObjsAdd(1, size) // see also: `coi.objsAdd`
		},
	}
	return cos.NewReaderWithArgs(args), 0, nil
}

func (pc *pushComm) InlineTransform(w http.ResponseWriter, _ *http.Request, bck *meta.Bck, objName string) error {
	lom := core.AllocLOM(objName)
	r, err := pc.doRequest(bck, lom, 0 /*timeout*/)
	core.FreeLOM(lom)
	if err != nil {
		return err
	}
	if cmn.Rom.FastV(5, cos.SmoduleETL) {
		nlog.Infoln(Hpush, lom.Cname(), err)
	}

	size := r.Size()
	if size < 0 {
		size = memsys.DefaultBufSize // TODO: track an average
	}
	buf, slab := core.T.PageMM().AllocSize(size)
	_, err = io.CopyBuffer(w, r, buf)

	slab.Free(buf)
	r.Close()
	return err
}

func (pc *pushComm) OfflineTransform(bck *meta.Bck, objName string, timeout time.Duration) (r cos.ReadCloseSizer, err error) {
	lom := core.AllocLOM(objName)
	r, err = pc.doRequest(bck, lom, timeout)
	if err == nil && cmn.Rom.FastV(5, cos.SmoduleETL) {
		nlog.Infoln(Hpush, lom.Cname(), err)
	}
	core.FreeLOM(lom)
	return
}

//////////////////
// redirectComm: implements Hpull
//////////////////

func (rc *redirectComm) InlineTransform(w http.ResponseWriter, r *http.Request, bck *meta.Bck, objName string) error {
	if err := rc.boot.xctn.AbortErr(); err != nil {
		return err
	}

	lom := core.AllocLOM(objName)
	size, err := lomLoad(lom, bck)
	if err != nil {
		core.FreeLOM(lom)
		return err
	}
	if size > 0 {
		rc.boot.xctn.OutObjsAdd(1, size)
	}

	http.Redirect(w, r, rc.redirectURL(lom), http.StatusTemporaryRedirect)

	if cmn.Rom.FastV(5, cos.SmoduleETL) {
		nlog.Infoln(Hpull, lom.Cname())
	}
	core.FreeLOM(lom)
	return nil
}

func (rc *redirectComm) redirectURL(lom *core.LOM) string {
	switch rc.boot.msg.ArgTypeX {
	case ArgTypeDefault, ArgTypeURL:
		return cos.JoinPath(rc.boot.uri, transformerPath(lom.Bck(), lom.ObjName))
	case ArgTypeFQN:
		return cos.JoinPath(rc.boot.uri, url.PathEscape(lom.FQN))
	}
	cos.Assert(false) // is validated at construction time
	return ""
}

func (rc *redirectComm) OfflineTransform(bck *meta.Bck, objName string, timeout time.Duration) (cos.ReadCloseSizer, error) {
	lom := core.AllocLOM(objName)
	size, errV := lomLoad(lom, bck)
	if errV != nil {
		core.FreeLOM(lom)
		return nil, errV
	}

	etlURL := rc.redirectURL(lom)
	r, err := rc.getWithTimeout(etlURL, size, timeout)

	if cmn.Rom.FastV(5, cos.SmoduleETL) {
		nlog.Infoln(Hpull, lom.Cname(), err)
	}
	core.FreeLOM(lom)
	return r, err
}

//////////////////
// revProxyComm: implements Hrev
//////////////////

func (rp *revProxyComm) InlineTransform(w http.ResponseWriter, r *http.Request, bck *meta.Bck, objName string) error {
	lom := core.AllocLOM(objName)
	size, err := lomLoad(lom, bck)
	if err != nil {
		core.FreeLOM(lom)
		return err
	}
	if size > 0 {
		rp.boot.xctn.OutObjsAdd(1, size)
	}
	path := transformerPath(bck, objName)
	core.FreeLOM(lom)

	r.URL.Path, _ = url.PathUnescape(path) // `Path` must be unescaped otherwise it will be escaped again.
	r.URL.RawPath = path                   // `RawPath` should be escaped version of `Path`.
	rp.rp.ServeHTTP(w, r)

	return nil
}

func (rp *revProxyComm) OfflineTransform(bck *meta.Bck, objName string, timeout time.Duration) (cos.ReadCloseSizer, error) {
	lom := core.AllocLOM(objName)
	size, errV := lomLoad(lom, bck)
	if errV != nil {
		core.FreeLOM(lom)
		return nil, errV
	}
	etlURL := cos.JoinPath(rp.boot.uri, transformerPath(bck, objName))
	r, err := rp.getWithTimeout(etlURL, size, timeout)

	if cmn.Rom.FastV(5, cos.SmoduleETL) {
		nlog.Infoln(Hrev, lom.Cname(), err)
	}
	core.FreeLOM(lom)
	return r, err
}

//////////////
// cbWriter //
//////////////

func (cw *cbWriter) Write(b []byte) (n int, err error) {
	n, err = cw.w.Write(b)
	cw.writeCb(n)
	return
}

//
// utils
//

// prune query (received from AIS proxy) prior to reverse-proxying the request to/from container -
// not removing apc.QparamETLName, for instance, would cause infinite loop.
func pruneQuery(rawQuery string) string {
	vals, err := url.ParseQuery(rawQuery)
	if err != nil {
		nlog.Errorf("failed to parse raw query %q, err: %v", rawQuery, err)
		return ""
	}
	for _, filtered := range []string{apc.QparamETLName, apc.QparamProxyID, apc.QparamUnixTime} {
		vals.Del(filtered)
	}
	return vals.Encode()
}

// TODO -- FIXME: unify the way we encode bucket/object:
// - url.PathEscape(uname) - see below - versus
// - Bck().Name + "/" + lom.ObjName - see pushComm above - versus
// - bck.AddToQuery() elsewhere
func transformerPath(bck *meta.Bck, objName string) string {
	return "/" + url.PathEscape(bck.MakeUname(objName))
}

func lomLoad(lom *core.LOM, bck *meta.Bck) (size int64, err error) {
	if err = lom.InitBck(bck.Bucket()); err != nil {
		return
	}
	if err = lom.Load(true /*cacheIt*/, false /*locked*/); err != nil {
		if cos.IsNotExist(err, 0) && bck.IsRemote() {
			err = nil // NOTE: size == 0
		}
	} else {
		size = lom.SizeBytes()
	}
	return
}
