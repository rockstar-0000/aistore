// Package s3 provides Amazon S3 compatibility layer
/*
 * Copyright (c) 2018-2022, NVIDIA CORPORATION. All rights reserved.
 */
package s3

import (
	"fmt"
	"net/http"
	"os"
	"sort"
	"strconv"
	"sync"
	"time"

	"github.com/NVIDIA/aistore/cluster"
	"github.com/NVIDIA/aistore/cmn/debug"
	"github.com/NVIDIA/aistore/cmn/nlog"
)

// NOTE: xattr stores only the (*) marked attributes
type (
	MptPart struct {
		MD5  string // MD5 of the part (*)
		FQN  string // FQN of the corresponding workfile
		Size int64  // part size in bytes (*)
		Num  int64  // part number (*)
	}
	mpt struct {
		bckName string
		objName string
		parts   []*MptPart // by part number
		ctime   time.Time  // InitUpload time
	}
	uploads map[string]*mpt // by upload ID
)

var (
	ups uploads
	mu  sync.RWMutex
)

func Init() { ups = make(uploads) }

// Start miltipart upload
func InitUpload(id, bckName, objName string) {
	mu.Lock()
	ups[id] = &mpt{
		bckName: bckName,
		objName: objName,
		parts:   make([]*MptPart, 0, iniCapParts),
		ctime:   time.Now(),
	}
	mu.Unlock()
}

// Add part to an active upload.
// Some clients may omit size and md5. Only partNum is must-have.
// md5 and fqn is filled by a target after successful saving the data to a workfile.
func AddPart(id string, npart *MptPart) (err error) {
	mu.Lock()
	mpt, ok := ups[id]
	if !ok {
		err = fmt.Errorf("upload %q not found (%s, %d)", id, npart.FQN, npart.Num)
	} else {
		mpt.parts = append(mpt.parts, npart)
	}
	mu.Unlock()
	return
}

// TODO: compare non-zero sizes (note: s3cmd sends 0) and part.ETag as well, if specified
func CheckParts(id string, parts []*PartInfo) ([]*MptPart, error) {
	mu.RLock()
	defer mu.RUnlock()
	mpt, ok := ups[id]
	if !ok {
		return nil, fmt.Errorf("upload %q not found", id)
	}
	// first, check that all parts are present
	var prev = int64(-1)
	for _, part := range parts {
		debug.Assert(part.PartNumber > prev) // must ascend
		if mpt.getPart(part.PartNumber) == nil {
			return nil, fmt.Errorf("upload %q: part %d not found", id, part.PartNumber)
		}
		prev = part.PartNumber
	}
	// copy (to work on it with no locks)
	nparts := make([]*MptPart, 0, len(parts))
	for _, part := range parts {
		nparts = append(nparts, mpt.getPart(part.PartNumber))
	}
	return nparts, nil
}

func ParsePartNum(s string) (partNum int64, err error) {
	partNum, err = strconv.ParseInt(s, 10, 16)
	if err != nil {
		err = fmt.Errorf("invalid part number %q (must be in 1-%d range): %v", s, MaxPartsPerUpload, err)
	}
	return
}

// Return a sum of upload part sizes.
// Used on upload completion to calculate the final size of the object.
func ObjSize(id string) (size int64, err error) {
	mu.RLock()
	mpt, ok := ups[id]
	if !ok {
		err = fmt.Errorf("upload %q not found", id)
	} else {
		for _, part := range mpt.parts {
			size += part.Size
		}
	}
	mu.RUnlock()
	return
}

// remove all temp files and delete from the map
// if completed (i.e., not aborted): store xattr
func FinishUpload(id, fqn string, aborted bool) (exists bool) {
	mu.Lock()
	mpt, ok := ups[id]
	if !ok {
		mu.Unlock()
		nlog.Warningf("fqn %s, id %s", fqn, id)
		return false
	}
	delete(ups, id)
	mu.Unlock()

	if !aborted {
		if err := storeMptXattr(fqn, mpt); err != nil {
			nlog.Warningf("fqn %s, id %s: %v", fqn, id, err)
		}
	}
	for _, part := range mpt.parts {
		if err := os.Remove(part.FQN); err != nil && !os.IsNotExist(err) {
			nlog.Errorln(err)
		}
	}
	return true
}

func ListUploads(bckName, idMarker string, maxUploads int) (result *ListMptUploadsResult) {
	mu.RLock()
	results := make([]UploadInfoResult, 0, len(ups))
	for id, mpt := range ups {
		results = append(results, UploadInfoResult{Key: mpt.objName, UploadID: id, Initiated: mpt.ctime})
	}
	mu.RUnlock()

	sort.Slice(results, func(i int, j int) bool {
		return results[i].Initiated.Before(results[j].Initiated)
	})

	var from int
	if idMarker != "" {
		// truncate
		for i, res := range results {
			if res.UploadID == idMarker {
				from = i + 1
				break
			}
			copy(results, results[from:])
			results = results[:len(results)-from]
		}
	}
	if maxUploads > 0 && len(results) > maxUploads {
		results = results[:maxUploads]
	}
	result = &ListMptUploadsResult{Bucket: bckName, Uploads: results, IsTruncated: from > 0}
	return
}

func ListParts(id string, lom *cluster.LOM) (parts []*PartInfo, err error, errCode int) {
	mu.RLock()
	mpt, ok := ups[id]
	if !ok {
		errCode = http.StatusNotFound
		mpt, err = loadMptXattr(lom.FQN)
		if err != nil || mpt == nil {
			mu.RUnlock()
			return
		}
		mpt.bckName, mpt.objName = lom.Bck().Name, lom.ObjName
		mpt.ctime = lom.Atime()
	}
	parts = make([]*PartInfo, 0, len(mpt.parts))
	for _, part := range mpt.parts {
		parts = append(parts, &PartInfo{ETag: part.MD5, PartNumber: part.Num, Size: part.Size})
	}
	mu.RUnlock()
	return
}
