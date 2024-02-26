//go:build azure

// Package backend contains implementation of various backend providers.
/*
 * Copyright (c) 2018-2024, NVIDIA CORPORATION. All rights reserved.
 */
package backend

// TODO:
// - check a variety of az clients instantiated below, and alternatives
//
// - support alternative authentication methods (currently, NewSharedKeyCredential only)
//   ref: ./storage/azblob@v1.3.0/container/examples_test.go
//
// - [200224] stop using etag as obj. version - see IsImmutableStorageWithVersioningEnabled, blob.VersionID, and:
//   ref: https://learn.microsoft.com/en-us/azure/storage/blobs/versioning-overview#how-blob-versioning-works

import (
	"context"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"os"
	"regexp"
	"strings"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob"
	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob/blob"
	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob/bloberror"
	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob/blockblob"
	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob/container"
	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob/service"
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
		u     string
		creds *azblob.SharedKeyCredential
		t     core.TargetPut
	}
)

const (
	azDefaultProto = "https://"
	azHost         = ".blob.core.windows.net"

	azAccNameEnvVar = "AZURE_STORAGE_ACCOUNT"
	azAccKeyEnvVar  = "AZURE_STORAGE_KEY" // a.k.a. AZURE_STORAGE_PRIMARY_ACCOUNT_KEY or AZURE_STORAGE_SECONDARY_ACCOUNT_KEY

	// ais
	azURLEnvVar   = "AIS_AZURE_URL"
	azProtoEnvVar = "AIS_AZURE_PROTO"
)

// used to parse azure errors
const (
	azErrDesc = "Description"
	azErrCode = "Code: "

	azErrPrefix = "azure-error["
)

var (
	azCleanErrRegex = regexp.MustCompile(`[^a-zA-Z0-9 ]+`)
)

// interface guard
var _ core.BackendProvider = (*azureProvider)(nil)

func azProto() string {
	proto := os.Getenv(azProtoEnvVar)
	if proto == "" {
		proto = azDefaultProto
	}
	return proto
}

func azAccName() string { return os.Getenv(azAccNameEnvVar) }
func azAccKey() string  { return os.Getenv(azAccKeyEnvVar) }

func asEndpoint() string {
	blurl := os.Getenv(azURLEnvVar)
	switch {
	case blurl == "":
		// the default
		return azProto() + azAccName() + azHost
	case strings.HasPrefix(blurl, "http"):
		return blurl
	default:
		if !strings.HasPrefix(blurl, ".") {
			blurl = "." + blurl
		}
		return azProto() + azAccName() + blurl
	}
}

func NewAzure(t core.TargetPut) (core.BackendProvider, error) {
	blurl := asEndpoint()

	// NOTE: NewSharedKeyCredential requires account name and its primary or secondary key
	creds, err := azblob.NewSharedKeyCredential(azAccName(), azAccKey())
	if err != nil {
		return nil, cmn.NewErrFailedTo(nil, azErrPrefix+": init]", "credentials", err)
	}

	return &azureProvider{
		t:     t,
		creds: creds,
		u:     blurl,
	}, nil
}

// (compare w/ cmn/backend)
func azEncodeEtag(etag azcore.ETag) string { return cmn.UnquoteCEV(string(etag)) }

func azEncodeChecksum(v []byte) string {
	if len(v) == 0 {
		return ""
	}
	return hex.EncodeToString(v)
}

//
// format and parse errors
//

func azureErrorToAISError(azureError error, bck *cmn.Bck, objName string) (int, error) {
	if cmn.Rom.FastV(5, cos.SmoduleBackend) {
		nlog.InfoDepth(1, "begin azure error =========================")
		nlog.InfoDepth(1, azureError)
		nlog.InfoDepth(1, "end azure error ===========================")
	}

	var stgErr *azcore.ResponseError
	if !errors.As(azureError, &stgErr) {
		return http.StatusInternalServerError, azureError
	}
	if cmn.Rom.FastV(5, cos.SmoduleBackend) {
		nlog.InfoDepth(1, "ErrorCode:", stgErr.ErrorCode, "StatusCode:", stgErr.StatusCode)
	}

	// NOTE: error-codes documentation seems to be incomplete and/or outdated
	// ref: https://learn.microsoft.com/en-us/rest/api/storageservices/common-rest-api-error-codes

	switch bloberror.Code(stgErr.ErrorCode) {
	case bloberror.ContainerNotFound:
		return http.StatusNotFound, cmn.NewErrRemoteBckNotFound(bck)
	case bloberror.BlobNotFound:
		return http.StatusNotFound, errors.New(azErrPrefix + "NotFound: " + bck.Cname(objName) + "]")
	case bloberror.InvalidResourceName:
		if objName != "" {
			return http.StatusNotFound, errors.New(azErrPrefix + "NotFound: " + bck.Cname(objName) + "]")
		}
	}

	// NOTE above
	if objName == "" && bloberror.Code(stgErr.ErrorCode) == bloberror.OutOfRangeInput {
		return http.StatusNotFound, cmn.NewErrRemoteBckNotFound(bck)
	}

	// azure error is usually a sizeable multi-line text with items including:
	// request ID, authorization, variery of x-ms-* headers, server and user agent, and more

	var (
		status      = stgErr.StatusCode
		code        string
		description string
		lines       = strings.Split(azureError.Error(), "\n")
	)
	if resp := stgErr.RawResponse; resp != nil {
		resp.Body.Close()
		debug.Assertf(resp.StatusCode == stgErr.StatusCode, "%d vs %d", resp.StatusCode, stgErr.StatusCode) // checking
		status = resp.StatusCode
	}
	for _, line := range lines {
		if strings.HasPrefix(line, azErrDesc) {
			description = azCleanErrRegex.ReplaceAllString(line[len(azErrDesc):], "")
		} else if i := strings.Index(line, azErrCode); i > 0 {
			code = azCleanErrRegex.ReplaceAllString(line[i+len(azErrCode):], "")
		}
	}
	if code != "" && description != "" {
		return status, errors.New(azErrPrefix + code + ": " + description + "]")
	}
	debug.Assert(false, azureError) // expecting to parse
	return status, azureError
}

// as core.BackendProvider --------------------------------------------------------------

func (*azureProvider) Provider() string { return apc.Azure }

//
// CREATE BUCKET
//

func (*azureProvider) CreateBucket(_ *meta.Bck) (int, error) {
	return http.StatusNotImplemented, cmn.NewErrNotImpl("create", "azure:// bucket")
}

//
// HEAD BUCKET
//

func (ap *azureProvider) HeadBucket(ctx context.Context, bck *meta.Bck) (cos.StrKVs, int, error) {
	var (
		cloudBck = bck.RemoteBck()
		cntURL   = ap.u + "/" + cloudBck.Name
	)
	client, err := container.NewClientWithSharedKeyCredential(cntURL, ap.creds, nil)
	if err != nil {
		status, err := azureErrorToAISError(err, cloudBck, "")
		return nil, status, err
	}
	resp, err := client.GetProperties(ctx, nil)
	if err != nil {
		status, err := azureErrorToAISError(err, cloudBck, "")
		return nil, status, err
	}

	bckProps := make(cos.StrKVs, 2)
	bckProps[apc.HdrBackendProvider] = apc.Azure

	// TODO #200224
	if true || resp.IsImmutableStorageWithVersioningEnabled != nil && *resp.IsImmutableStorageWithVersioningEnabled {
		bckProps[apc.HdrBucketVerEnabled] = "true"
	} else {
		bckProps[apc.HdrBucketVerEnabled] = "false"
	}
	return bckProps, http.StatusOK, nil
}

//
// LIST OBJECTS
//

func (ap *azureProvider) ListObjects(bck *meta.Bck, msg *apc.LsoMsg, lst *cmn.LsoResult) (int, error) {
	msg.PageSize = calcPageSize(msg.PageSize, bck.MaxPageSize())
	var (
		cloudBck = bck.RemoteBck()
		cntURL   = ap.u + "/" + cloudBck.Name
		num      = int32(msg.PageSize)
		opts     = container.ListBlobsFlatOptions{Prefix: apc.Ptr(msg.Prefix), MaxResults: &num}
	)
	client, err := container.NewClientWithSharedKeyCredential(cntURL, ap.creds, nil)
	if err != nil {
		return azureErrorToAISError(err, cloudBck, "")
	}
	if cmn.Rom.FastV(4, cos.SmoduleBackend) {
		nlog.Infof("list_objects %s", cloudBck.Name)
	}
	if msg.ContinuationToken != "" {
		opts.Marker = apc.Ptr(msg.ContinuationToken)
	}

	pager := client.NewListBlobsFlatPager(&opts)
	resp, err := pager.NextPage(context.Background())
	if err != nil {
		return azureErrorToAISError(err, cloudBck, "")
	}

	l := len(resp.Segment.BlobItems)
	for i := len(lst.Entries); i < l; i++ {
		lst.Entries = append(lst.Entries, &cmn.LsoEntry{}) // add missing empty
	}
	for idx := range resp.Segment.BlobItems {
		var (
			custom = cos.StrKVs{}
			blob   = resp.Segment.BlobItems[idx]
			entry  = lst.Entries[idx]
		)
		entry.Name = *blob.Name
		entry.Size = *blob.Properties.ContentLength
		if msg.IsFlagSet(apc.LsNameOnly) || msg.IsFlagSet(apc.LsNameSize) {
			continue
		}

		entry.Checksum = azEncodeChecksum(blob.Properties.ContentMD5)

		etag := azEncodeEtag(*blob.Properties.ETag)
		entry.Version = etag // (TODO a the top)

		// custom
		if msg.WantProp(apc.GetPropsCustom) {
			custom[cmn.ETag] = etag
			if !blob.Properties.LastModified.IsZero() {
				custom[cmn.LastModified] = fmtTime(*blob.Properties.LastModified)
			}
			if blob.Properties.ContentType != nil {
				custom[cos.HdrContentType] = *blob.Properties.ContentType
			}
			if blob.VersionID != nil {
				custom[cmn.VersionObjMD] = *blob.VersionID
			}
			entry.Custom = cmn.CustomMD2S(custom)
		}
	}
	lst.Entries = lst.Entries[:l]

	if resp.NextMarker != nil {
		lst.ContinuationToken = *resp.NextMarker
	}
	if cmn.Rom.FastV(4, cos.SmoduleBackend) {
		nlog.Infof("[list_objects] count %d(marker: %s)", len(lst.Entries), lst.ContinuationToken)
	}
	return 0, nil
}

//
// LIST BUCKETS
//

func (ap *azureProvider) ListBuckets(cmn.QueryBcks) (bcks cmn.Bcks, _ int, _ error) {
	serviceClient, err := service.NewClientWithSharedKeyCredential(ap.u, ap.creds, nil)
	if err != nil {
		status, err := azureErrorToAISError(err, &cmn.Bck{Provider: apc.Azure}, "")
		return nil, status, err
	}
	pager := serviceClient.NewListContainersPager(&service.ListContainersOptions{})
	for pager.More() {
		resp, err := pager.NextPage(context.TODO())
		if err != nil {
			status, err := azureErrorToAISError(err, &cmn.Bck{Provider: apc.Azure}, "")
			return bcks, status, err
		}
		for _, ci := range resp.ContainerItems {
			bcks = append(bcks, cmn.Bck{
				Name:     *ci.Name,
				Provider: apc.Azure,
			})
		}
	}
	if cmn.Rom.FastV(4, cos.SmoduleBackend) {
		nlog.Infof("[list_buckets] count %d", len(bcks))
	}
	return bcks, 0, nil
}

//
// HEAD OBJECT
//

func (ap *azureProvider) HeadObj(ctx context.Context, lom *core.LOM) (*cmn.ObjAttrs, int, error) {
	var (
		cloudBck = lom.Bucket().RemoteBck()
		blURL    = ap.u + "/" + cloudBck.Name + "/" + lom.ObjName
	)
	client, err := blockblob.NewClientWithSharedKeyCredential(blURL, ap.creds, nil)
	if err != nil {
		status, err := azureErrorToAISError(err, cloudBck, lom.ObjName)
		return nil, status, err
	}
	resp, err := client.GetProperties(ctx, nil)
	if err != nil {
		status, err := azureErrorToAISError(err, cloudBck, lom.ObjName)
		return nil, status, err
	}

	debug.Assert(resp.IsCurrentVersion == nil || *resp.IsCurrentVersion, "expecting current/latest/the-only ver")

	oa := &cmn.ObjAttrs{}
	oa.CustomMD = make(cos.StrKVs, 6)
	oa.SetCustomKey(cmn.SourceObjMD, apc.Azure)
	oa.Size = *resp.ContentLength

	etag := azEncodeEtag(*resp.ETag)
	oa.SetCustomKey(cmn.ETag, etag)

	oa.Ver = etag // TODO #200224

	if md5 := azEncodeChecksum(resp.ContentMD5); md5 != "" {
		oa.SetCustomKey(cmn.MD5ObjMD, md5)
	}
	if v := resp.LastModified; v != nil {
		oa.SetCustomKey(cmn.LastModified, fmtTime(*v))
	}
	if v := resp.ContentType; v != nil {
		// unlike other custom attrs, "Content-Type" is not getting stored w/ LOM
		// - only shown via list-objects and HEAD when not present
		oa.SetCustomKey(cos.HdrContentType, *v)
	}
	if cmn.Rom.FastV(5, cos.SmoduleBackend) {
		nlog.Infof("[head_object] %s", lom)
	}
	return oa, 0, nil
}

//
// GET OBJECT
//

func (ap *azureProvider) GetObj(ctx context.Context, lom *core.LOM, owt cmn.OWT) (int, error) {
	res := ap.GetObjReader(ctx, lom, 0, 0)
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

func (ap *azureProvider) GetObjReader(ctx context.Context, lom *core.LOM, offset, length int64) (res core.GetReaderResult) {
	var (
		cloudBck = lom.Bucket().RemoteBck()
		blURL    = ap.u + "/" + cloudBck.Name + "/" + lom.ObjName
	)
	client, err := blockblob.NewClientWithSharedKeyCredential(blURL, ap.creds, nil)
	if err != nil {
		res.ErrCode, res.Err = azureErrorToAISError(err, cloudBck, lom.ObjName)
		return
	}

	// Get checksum
	respProps, err := client.GetProperties(ctx, nil)
	if err != nil {
		res.ErrCode, res.Err = azureErrorToAISError(err, cloudBck, lom.ObjName)
		return
	}

	// (0, 0) range indicates "whole object"
	var opts blob.DownloadStreamOptions
	opts.Range.Count = length
	opts.Range.Offset = offset
	resp, err := client.DownloadStream(ctx, &opts)
	if err != nil {
		res.ErrCode, res.Err = azureErrorToAISError(err, cloudBck, lom.ObjName)
		if res.ErrCode == http.StatusRequestedRangeNotSatisfiable {
			res.Err = cmn.NewErrRangeNotSatisfiable(res.Err, nil, 0)
		}
		return res
	}

	debug.Assert(resp.IsCurrentVersion == nil || *resp.IsCurrentVersion, "expecting current/latest/the-only ver")
	res.Size = *resp.ContentLength

	if length == 0 {
		// custom metadata
		lom.SetCustomKey(cmn.SourceObjMD, apc.Azure)
		etag := azEncodeEtag(*respProps.ETag)
		lom.SetCustomKey(cmn.ETag, etag)

		lom.SetVersion(etag) // TODO #200224

		if md5 := azEncodeChecksum(respProps.ContentMD5); md5 != "" {
			lom.SetCustomKey(cmn.MD5ObjMD, md5)
			res.ExpCksum = cos.NewCksum(cos.ChecksumMD5, md5)
		}
	}

	res.R = resp.Body
	return res
}

//
// PUT OBJECT
//

func (ap *azureProvider) PutObj(r io.ReadCloser, lom *core.LOM, _ *core.ExtraArgsPut) (int, error) {
	defer cos.Close(r)

	client, err := azblob.NewClientWithSharedKeyCredential(ap.u, ap.creds, nil)
	if err != nil {
		return azureErrorToAISError(err, &cmn.Bck{Provider: apc.Azure}, "")
	}
	cloudBck := lom.Bck().RemoteBck()

	opts := azblob.UploadStreamOptions{}
	if size := lom.SizeBytes(true); size > cos.MiB {
		opts.Concurrency = int(min((size+cos.MiB-1)/cos.MiB, 8))
	}

	resp, err := client.UploadStream(context.Background(), cloudBck.Name, lom.ObjName, r, &opts)
	if err != nil {
		return azureErrorToAISError(err, cloudBck, lom.ObjName)
	}

	etag := azEncodeEtag(*resp.ETag)
	lom.SetCustomKey(cmn.ETag, etag)

	lom.SetVersion(etag) // TODO #200224

	if v := resp.LastModified; v != nil {
		lom.SetCustomKey(cmn.LastModified, fmtTime(*v))
	}
	if cmn.Rom.FastV(5, cos.SmoduleBackend) {
		nlog.Infof("[put_object] %s", lom)
	}
	return http.StatusOK, nil
}

//
// DELETE OBJECT
//

func (ap *azureProvider) DeleteObj(lom *core.LOM) (int, error) {
	client, err := azblob.NewClientWithSharedKeyCredential(ap.u, ap.creds, nil)
	if err != nil {
		return azureErrorToAISError(err, &cmn.Bck{Provider: apc.Azure}, "")
	}
	cloudBck := lom.Bck().RemoteBck()

	_, err = client.DeleteBlob(context.Background(), cloudBck.Name, lom.ObjName, nil)
	if err != nil {
		return azureErrorToAISError(err, cloudBck, lom.ObjName)
	}
	return http.StatusOK, nil
}
