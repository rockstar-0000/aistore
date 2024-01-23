//go:build azure

// Package backend contains implementation of various backend providers.
/*
 * Copyright (c) 2018-2024, NVIDIA CORPORATION. All rights reserved.
 */
package backend

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"

	"github.com/Azure/azure-storage-blob-go/azblob"
	"github.com/NVIDIA/aistore/api/apc"
	"github.com/NVIDIA/aistore/cmn"
	"github.com/NVIDIA/aistore/cmn/cos"
	"github.com/NVIDIA/aistore/cmn/debug"
	"github.com/NVIDIA/aistore/cmn/nlog"
	"github.com/NVIDIA/aistore/core"
	"github.com/NVIDIA/aistore/core/meta"
)

type (
	azureProvider struct {
		u string
		c *azblob.SharedKeyCredential
		t core.TargetPut
		s azblob.ServiceURL
	}
)

const (
	azureDefaultProto = "https://"
	// Azure simulator(Azurite) consts
	azureDevAccName = "devstoreaccount1"
	azureDevAccKey  = "Eby8vdM02xNOcqFlqUwJPLlmEtlCDXJ1OUzFT50uSRZ6IFsuFq2UVErCz4I6tq/K1SZFPTOtr/KBHBeksoGMGw=="
	// Azurite is always HTTP
	azureDevHost = "http://127.0.0.1:10000/" + azureDevAccName

	azureBackend = "azure-backend"

	// real Azure server constants
	azureHost = ".blob.core.windows.net"
	// AZ CLI compatible env vars
	azureAccNameEnvVar = "AZURE_STORAGE_ACCOUNT"
	azureAccKeyEnvVar  = "AZURE_STORAGE_KEY"
	// AZ AIS internal env vars
	azureURLEnvVar   = "AIS_AZURE_URL"
	azureProtoEnvVar = "AIS_AZURE_PROTO"
	// Object lease time for PUT/DEL operations, in seconds.
	// Must be within 15..60 range or -1(infinity).
	leaseTime = 60
)

var (
	// context placeholder
	azctx context.Context

	// TODO: client provided key by name and/or by value to encrypt/decrypt data.
	defaultKeyOptions azblob.ClientProvidedKeyOptions

	// interface guard
	_ core.BackendProvider = (*azureProvider)(nil)
)

func azureProto() string {
	proto := os.Getenv(azureProtoEnvVar)
	if proto == "" {
		return azureDefaultProto
	}
	return proto
}

func azureUserName() string {
	name := os.Getenv(azureAccNameEnvVar)
	if name == "" {
		return azureDevAccName
	}
	return name
}

func azureUserKey() string {
	key := os.Getenv(azureAccKeyEnvVar)
	if key == "" && azureUserName() == azureDevAccName {
		return azureDevAccKey
	}
	return key
}

func azureErrStatus(status int) error { return fmt.Errorf("http-status=%d", status) }

// Detects development mode by checking the user name. It is a standalone
// function because there can be a better way to detect developer mode
func isAzureDevMode(user string) bool {
	return user == azureDevAccName
}

// URL is empty:
// Dev -> http://127.0.0.1:1000/devstoreaccount1
// Prod -> http://<account_name>.blob.core.windows.net
//
// URL is not empty:
// URL starts with protocol -> URL
// URL does not contain protocol -> http://<account_name>URL/
func azureURL() string {
	url := os.Getenv(azureURLEnvVar)
	if url != "" {
		if !strings.HasPrefix(url, "http") {
			if !strings.HasPrefix(url, ".") {
				url = "." + url
			}
			url = azureProto() + azureUserName() + url
		}
		return url
	}
	user := azureUserName()
	if isAzureDevMode(user) {
		return azureDevHost
	}
	return azureProto() + user + azureHost
}

// Only one authentication way is supported: with Shared Credentials that
// requires Account name and key.
func NewAzure(t core.TargetPut) (core.BackendProvider, error) {
	path := azureURL()
	u, err := url.Parse(path)
	if err != nil {
		return nil, cmn.NewErrFailedTo(nil, azureBackend+": parse", "URL", err)
	}
	name := azureUserName()
	key := azureUserKey()
	creds, err := azblob.NewSharedKeyCredential(name, key)
	if err != nil {
		return nil, cmn.NewErrFailedTo(nil, azureBackend+": init", "credentials", err)
	}

	azctx = context.Background()
	p := azblob.NewPipeline(creds, azblob.PipelineOptions{})
	return &azureProvider{
		t: t,
		u: path,
		c: creds,
		s: azblob.NewServiceURL(*u, p),
	}, nil
}

func azureErrorToAISError(azureError error, bck *cmn.Bck, objName string) (int, error) {
	bckNotFound, status, err := _toErr(azureError, bck, objName)
	if bckNotFound {
		return status, err
	}
	return status, errors.New("azure-error[" + err.Error() + "]")
}

func _toErr(azureError error, bck *cmn.Bck, objName string) (bool, int, error) {
	stgErr, ok := azureError.(azblob.StorageError)
	if !ok {
		return false, http.StatusInternalServerError, azureError
	}
	switch stgErr.ServiceCode() {
	case azblob.ServiceCodeContainerNotFound:
		return true, http.StatusNotFound, cmn.NewErrRemoteBckNotFound(bck)
	case azblob.ServiceCodeBlobNotFound:
		err := fmt.Errorf("%s not found", bck.Cname(objName))
		return false, http.StatusNotFound, cmn.NewErrHTTP(nil, err, http.StatusNotFound)
	case azblob.ServiceCodeInvalidResourceName:
		err := fmt.Errorf("%s not found", bck.Cname(objName))
		return false, http.StatusNotFound, cmn.NewErrHTTP(nil, err, http.StatusNotFound)
	default:
		resp := stgErr.Response()
		if resp != nil {
			resp.Body.Close()
			return false, resp.StatusCode, azureError
		}
		return false, http.StatusInternalServerError, azureError
	}
}

func (*azureProvider) Provider() string { return apc.Azure }

// https://docs.microsoft.com/en-us/connectors/azureblob/#general-limits
func (*azureProvider) MaxPageSize() uint { return 5000 }

///////////////////
// CREATE BUCKET //
///////////////////

func (*azureProvider) CreateBucket(_ *meta.Bck) (int, error) {
	return http.StatusNotImplemented, cmn.NewErrNotImpl("create", "azure:// bucket")
}

/////////////////
// HEAD BUCKET //
/////////////////

func (ap *azureProvider) HeadBucket(ctx context.Context, bck *meta.Bck) (bckProps cos.StrKVs,
	errCode int, err error) {
	var (
		cloudBck = bck.RemoteBck()
		cntURL   = ap.s.NewContainerURL(cloudBck.Name)
	)
	resp, err := cntURL.GetProperties(ctx, azblob.LeaseAccessConditions{})
	if err != nil {
		status, err := azureErrorToAISError(err, cloudBck, "")
		return bckProps, status, err
	}
	if resp.StatusCode() >= http.StatusBadRequest {
		err := cmn.NewErrFailedTo(nil, azureBackend+": read bucket", cloudBck.Name, azureErrStatus(resp.StatusCode()))
		return bckProps, resp.StatusCode(), err
	}
	bckProps = make(cos.StrKVs, 2)
	bckProps[apc.HdrBackendProvider] = apc.Azure
	bckProps[apc.HdrBucketVerEnabled] = "true"
	return bckProps, http.StatusOK, nil
}

//////////////////
// LIST OBJECTS //
//////////////////

func (ap *azureProvider) ListObjects(bck *meta.Bck, msg *apc.LsoMsg, lst *cmn.LsoResult) (errCode int, err error) {
	msg.PageSize = calcPageSize(msg.PageSize, ap.MaxPageSize())
	var (
		h        = cmn.BackendHelpers.Azure
		cloudBck = bck.RemoteBck()
		cntURL   = ap.s.NewContainerURL(cloudBck.Name)
		marker   = azblob.Marker{}
		opts     = azblob.ListBlobsSegmentOptions{Prefix: msg.Prefix, MaxResults: int32(msg.PageSize)}
	)
	if cmn.Rom.FastV(4, cos.SmoduleBackend) {
		nlog.Infof("list_objects %s", cloudBck.Name)
	}
	if msg.ContinuationToken != "" {
		marker.Val = apc.String(msg.ContinuationToken)
	}

	resp, err := cntURL.ListBlobsFlatSegment(azctx, marker, opts)
	if err != nil {
		return azureErrorToAISError(err, cloudBck, "")
	}
	if resp.StatusCode() >= http.StatusBadRequest {
		err := cmn.NewErrFailedTo(nil, azureBackend+": list objects of", cloudBck.Name, azureErrStatus(resp.StatusCode()))
		return resp.StatusCode(), err
	}

	l := len(resp.Segment.BlobItems)
	for i := len(lst.Entries); i < l; i++ {
		lst.Entries = append(lst.Entries, &cmn.LsoEntry{}) // add missing empty
	}
	for idx := range resp.Segment.BlobItems {
		var (
			blob  = &resp.Segment.BlobItems[idx]
			entry = lst.Entries[idx]
		)
		entry.Name = blob.Name

		debug.Assert(blob.Properties.ContentLength != nil) // NOTE: always expecting the size

		if blob.Properties.ContentLength != nil {
			entry.Size = *blob.Properties.ContentLength
		}
		if msg.IsFlagSet(apc.LsNameOnly) || msg.IsFlagSet(apc.LsNameSize) {
			continue
		}
		// NOTE: here and elsewhere (below), use Etag as the version
		if v, ok := h.EncodeVersion(string(blob.Properties.Etag)); ok {
			entry.Version = v
		}
		if v, ok := h.EncodeCksum(blob.Properties.ContentMD5); ok {
			entry.Checksum = v
		}
	}
	lst.Entries = lst.Entries[:l]

	if resp.NextMarker.Val != nil {
		lst.ContinuationToken = *resp.NextMarker.Val
	}
	if cmn.Rom.FastV(4, cos.SmoduleBackend) {
		nlog.Infof("[list_objects] count %d(marker: %s)", len(lst.Entries), lst.ContinuationToken)
	}
	return
}

//////////////////
// LIST BUCKETS //
//////////////////

func (ap *azureProvider) ListBuckets(_ cmn.QueryBcks) (bcks cmn.Bcks, errCode int, err error) {
	var (
		o          azblob.ListContainersSegmentOptions
		marker     azblob.Marker
		containers *azblob.ListContainersSegmentResponse
	)
	for marker.NotDone() {
		containers, err = ap.s.ListContainersSegment(azctx, marker, o)
		if err != nil {
			errCode, err = azureErrorToAISError(err, &cmn.Bck{Provider: apc.Azure}, "")
			return
		}

		for idx := range containers.ContainerItems {
			bcks = append(bcks, cmn.Bck{
				Name:     containers.ContainerItems[idx].Name,
				Provider: apc.Azure,
			})
		}
		marker = containers.NextMarker
	}
	return
}

/////////////////
// HEAD OBJECT //
/////////////////

func (ap *azureProvider) HeadObj(ctx context.Context, lom *core.LOM) (oa *cmn.ObjAttrs, errCode int, err error) {
	var (
		resp     *azblob.BlobGetPropertiesResponse
		h        = cmn.BackendHelpers.Azure
		cloudBck = lom.Bck().RemoteBck()
		cntURL   = ap.s.NewContainerURL(cloudBck.Name)
		blobURL  = cntURL.NewBlobURL(lom.ObjName)
	)
	if resp, err = blobURL.GetProperties(ctx, azblob.BlobAccessConditions{}, defaultKeyOptions); err != nil {
		errCode, err = azureErrorToAISError(err, cloudBck, lom.ObjName)
		return
	}
	if resp.StatusCode() >= http.StatusBadRequest {
		err = cmn.NewErrFailedTo(nil, azureBackend+": get object props of", cloudBck.Name+"/"+lom.ObjName,
			azureErrStatus(resp.StatusCode()))
		errCode = resp.StatusCode()
		return
	}
	oa = &cmn.ObjAttrs{}
	oa.SetCustomKey(cmn.SourceObjMD, apc.Azure)
	oa.Size = resp.ContentLength()
	if v, ok := h.EncodeVersion(string(resp.ETag())); ok {
		oa.Ver = v // NOTE: using ETag as _the_ version
		oa.SetCustomKey(cmn.ETag, v)
	}
	if v, ok := h.EncodeCksum(resp.ContentMD5()); ok {
		oa.SetCustomKey(cmn.MD5ObjMD, v)
	}
	if cmn.Rom.FastV(5, cos.SmoduleBackend) {
		nlog.Infof("[head_object] %s", lom)
	}
	return
}

////////////////
// GET OBJECT //
////////////////

func (ap *azureProvider) GetObj(ctx context.Context, lom *core.LOM, owt cmn.OWT) (int, error) {
	res := ap.GetObjReader(ctx, lom)
	if res.Err != nil {
		return res.ErrCode, res.Err
	}
	params := allocPutParams(res, owt)
	err := ap.t.PutObject(lom, params)
	core.FreePutParams(params)
	if cmn.Rom.FastV(5, cos.SmoduleBackend) {
		nlog.Infoln("[get_object]", lom.String(), err)
	}
	return 0, err
}

////////////////////
// GET OBJ READER //
////////////////////

func (ap *azureProvider) GetObjReader(ctx context.Context, lom *core.LOM) (res core.GetReaderResult) {
	var (
		h        = cmn.BackendHelpers.Azure
		cloudBck = lom.Bck().RemoteBck()
		cntURL   = ap.s.NewContainerURL(cloudBck.Name)
		blobURL  = cntURL.NewBlobURL(lom.ObjName)
	)
	// Get checksum
	respProps, err := blobURL.GetProperties(ctx, azblob.BlobAccessConditions{}, defaultKeyOptions)
	if err != nil {
		res.ErrCode, res.Err = azureErrorToAISError(err, cloudBck, lom.ObjName)
		return
	}
	if respProps.StatusCode() >= http.StatusBadRequest {
		res.Err = cmn.NewErrFailedTo(nil, azureBackend+": get object props of", cloudBck.Name+"/"+lom.ObjName,
			azureErrStatus(respProps.StatusCode()))
		res.ErrCode = respProps.StatusCode()
		return
	}
	// 0, 0 = read range: the whole object
	resp, err := blobURL.Download(ctx, 0, 0, azblob.BlobAccessConditions{}, false, defaultKeyOptions)
	if err != nil {
		res.ErrCode, res.Err = azureErrorToAISError(err, cloudBck, lom.ObjName)
		return
	}
	if resp.StatusCode() >= http.StatusBadRequest {
		res.Err = cmn.NewErrFailedTo(nil, azureBackend+": get object", cloudBck.Name+"/"+lom.ObjName,
			azureErrStatus(respProps.StatusCode()))
		res.ErrCode = resp.StatusCode()
		return
	}

	// custom metadata
	lom.SetCustomKey(cmn.SourceObjMD, apc.Azure)
	if v, ok := h.EncodeVersion(string(respProps.ETag())); ok {
		lom.SetVersion(v)
		lom.SetCustomKey(cmn.ETag, v)
	}
	if v, ok := h.EncodeCksum(respProps.ContentMD5()); ok {
		lom.SetCustomKey(cmn.MD5ObjMD, v)
		res.ExpCksum = cos.NewCksum(cos.ChecksumMD5, v)
	}

	res.Size = resp.ContentLength()

	retryOpts := azblob.RetryReaderOptions{MaxRetryRequests: 3}
	res.R = resp.Body(retryOpts)
	return
}

////////////////
// PUT OBJECT //
////////////////

func (ap *azureProvider) PutObj(r io.ReadCloser, lom *core.LOM) (int, error) {
	defer cos.Close(r)

	var (
		leaseID  string
		h        = cmn.BackendHelpers.Azure
		cloudBck = lom.Bck().RemoteBck()
		cntURL   = ap.s.NewContainerURL(cloudBck.Name)
		blobURL  = cntURL.NewBlockBlobURL(lom.ObjName)
		cond     = azblob.ModifiedAccessConditions{}
	)
	// Try to lease: if object does not exist, leasing fails with NotFound
	acqResp, err := blobURL.AcquireLease(azctx, "", leaseTime, cond)
	if err == nil {
		leaseID = acqResp.LeaseID()
		defer blobURL.ReleaseLease(azctx, acqResp.LeaseID(), cond)
	}
	if err != nil {
		code, errLease := azureErrorToAISError(err, cloudBck, lom.ObjName)
		if code != http.StatusNotFound {
			return code, errLease
		}
	}
	// Use BlockBlob instead of PageBlob because the latter requires
	// object size to be divisible by 512.
	// Without buffer options(with 0's) UploadStreamToBlockBlob hangs up
	opts := azblob.UploadStreamToBlockBlobOptions{
		BufferSize: 64 * 1024,
		MaxBuffers: 3,
	}
	if leaseID != "" {
		opts.AccessConditions = azblob.BlobAccessConditions{
			LeaseAccessConditions: azblob.LeaseAccessConditions{LeaseID: leaseID},
		}
	}
	putResp, err := azblob.UploadStreamToBlockBlob(azctx, r, blobURL, opts)
	if err != nil {
		status, err := azureErrorToAISError(err, cloudBck, lom.ObjName)
		return status, err
	}
	resp := putResp.Response()
	resp.Body.Close()
	if resp.StatusCode >= http.StatusBadRequest {
		err := cmn.NewErrFailedTo(nil, azureBackend+": PUT", cloudBck.Name+"/"+lom.ObjName,
			azureErrStatus(resp.StatusCode))
		return resp.StatusCode, err
	}
	if v, ok := h.EncodeVersion(string(putResp.ETag())); ok {
		lom.SetCustomKey(cmn.ETag, v) // NOTE: using ETag as version
		lom.SetVersion(v)
	}
	if cmn.Rom.FastV(5, cos.SmoduleBackend) {
		nlog.Infof("[put_object] %s", lom)
	}
	return http.StatusOK, nil
}

///////////////////
// DELETE OBJECT //
///////////////////

// Delete looks complex because according to docs, it needs acquiring
// an object beforehand and releasing the lease after
func (ap *azureProvider) DeleteObj(lom *core.LOM) (int, error) {
	var (
		cloudBck = lom.Bck().RemoteBck()
		cntURL   = ap.s.NewContainerURL(lom.Bck().Name)
		blobURL  = cntURL.NewBlobURL(lom.ObjName)
		cond     = azblob.ModifiedAccessConditions{}
	)

	acqResp, err := blobURL.AcquireLease(azctx, "", leaseTime, cond)
	if err != nil {
		return azureErrorToAISError(err, cloudBck, lom.ObjName)
	}
	if acqResp.StatusCode() >= http.StatusBadRequest {
		err := cmn.NewErrFailedTo(nil, azureBackend+": acquire object", cloudBck.Name+"/"+lom.ObjName,
			azureErrStatus(acqResp.StatusCode()))
		return acqResp.StatusCode(), err
	}

	delCond := azblob.BlobAccessConditions{
		LeaseAccessConditions: azblob.LeaseAccessConditions{LeaseID: acqResp.LeaseID()},
	}
	defer blobURL.ReleaseLease(azctx, acqResp.LeaseID(), cond)
	delResp, err := blobURL.Delete(azctx, azblob.DeleteSnapshotsOptionInclude, delCond)
	if err != nil {
		return azureErrorToAISError(err, cloudBck, lom.ObjName)
	}
	if delResp.StatusCode() >= http.StatusBadRequest {
		err := cmn.NewErrFailedTo(nil, azureBackend+": delete object", cloudBck.Name+"/"+lom.ObjName,
			azureErrStatus(delResp.StatusCode()))
		return delResp.StatusCode(), err
	}
	return http.StatusOK, nil
}
