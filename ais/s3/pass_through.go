// Package s3 provides Amazon S3 compatibility layer
/*
 * Copyright (c) 2024, NVIDIA CORPORATION. All rights reserved.
 */
package s3

import (
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/NVIDIA/aistore/api/apc"
	"github.com/NVIDIA/aistore/cmn"
	"github.com/NVIDIA/aistore/cmn/cos"
	"github.com/NVIDIA/aistore/cmn/debug"
	"github.com/NVIDIA/aistore/core"
)

const (
	signatureV4 = "AWS4-HMAC-SHA256"
)

type (
	PassThroughSignedReq struct {
		client *http.Client
		oreq   *http.Request
		lom    *core.LOM
		body   io.ReadCloser
		query  url.Values
	}
	PassThroughSignedResp struct {
		Body       []byte
		Header     http.Header
		StatusCode int
	}
)

// FIXME: handle error cases
func parseSignatureV4(query url.Values, header http.Header) (region string) {
	if credentials := query.Get(HeaderCredentials); credentials != "" {
		region = strings.Split(credentials, "/")[2]
	} else if credentials := header.Get(apc.HdrAuthorization); strings.HasPrefix(credentials, signatureV4) {
		credentials = strings.TrimPrefix(credentials, signatureV4)
		credentials = strings.TrimSpace(credentials)
		credentials = strings.Split(credentials, ", ")[0]
		credentials = strings.TrimPrefix(credentials, "Credential=")
		region = strings.Split(credentials, "/")[2]
	}
	return region
}

func NewPassThroughSignedReq(c *http.Client, oreq *http.Request, lom *core.LOM, body io.ReadCloser, q url.Values) *PassThroughSignedReq {
	return &PassThroughSignedReq{c, oreq, lom, body, q}
}

func (pts *PassThroughSignedReq) Do() (*PassThroughSignedResp, error) {
	region := parseSignatureV4(pts.query, pts.oreq.Header)
	if region == "" {
		return nil, nil
	}

	// S3 checks every single query param
	pts.query.Del(apc.QparamProxyID)
	pts.query.Del(apc.QparamUnixTime)
	queryEncoded := pts.query.Encode()

	// produce a new request (nreq) from the old/original one (oreq)
	s3url := makeS3URL(region, pts.lom.Bck().Name, pts.lom.ObjName, queryEncoded)
	nreq, err := http.NewRequest(pts.oreq.Method, s3url, pts.body)
	if err != nil {
		return &PassThroughSignedResp{StatusCode: http.StatusInternalServerError}, err
	}
	nreq.Header = pts.oreq.Header // NOTE: _not_ cloning
	if nreq.Body != nil {
		nreq.ContentLength = pts.oreq.ContentLength
		if nreq.ContentLength == -1 {
			debug.Assert(false) // FIXME: remove, or catch in debug mode
			nreq.ContentLength = pts.lom.SizeBytes()
		}
	}

	resp, err := pts.client.Do(nreq)
	if err != nil {
		return &PassThroughSignedResp{StatusCode: http.StatusInternalServerError}, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusPartialContent {
		output, _ := io.ReadAll(resp.Body)
		return &PassThroughSignedResp{StatusCode: resp.StatusCode},
			fmt.Errorf("invalid status: %d, output: %s", resp.StatusCode, string(output))
	}

	output, err := io.ReadAll(resp.Body)
	if err != nil {
		return &PassThroughSignedResp{StatusCode: http.StatusBadRequest},
			fmt.Errorf("failed to read response body: %v", err)
	}
	return &PassThroughSignedResp{
		Body:       output,
		Header:     resp.Header,
		StatusCode: resp.StatusCode,
	}, nil
}

///////////////////////////
// PassThroughSignedResp //
///////////////////////////

// (compare w/ cmn/objattrs FromHeader)
func (resp *PassThroughSignedResp) ObjAttrs() (oa *cmn.ObjAttrs) {
	oa = &cmn.ObjAttrs{}
	oa.CustomMD = make(cos.StrKVs, 3)

	oa.SetCustomKey(cmn.SourceObjMD, apc.AWS)
	etag := cmn.UnquoteCEV(resp.Header.Get(cos.HdrETag))
	debug.Assert(etag != "")
	oa.SetCustomKey(cmn.ETag, etag)
	if !cmn.IsS3MultipartEtag(etag) {
		oa.SetCustomKey(cmn.MD5ObjMD, etag)
	}
	if sz := resp.Header.Get(cos.HdrContentLength); sz != "" {
		size, err := strconv.ParseInt(sz, 10, 64)
		debug.AssertNoErr(err)
		oa.Size = size
	}
	return oa
}
