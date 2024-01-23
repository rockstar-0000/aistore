// Package core provides core metadata and in-cluster API
/*
 * Copyright (c) 2018-2024, NVIDIA CORPORATION. All rights reserved.
 */
package core

import (
	"fmt"
	"io"
	"os"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/NVIDIA/aistore/api/apc"
	"github.com/NVIDIA/aistore/cmn"
	"github.com/NVIDIA/aistore/cmn/atomic"
	"github.com/NVIDIA/aistore/cmn/cos"
	"github.com/NVIDIA/aistore/cmn/debug"
	"github.com/NVIDIA/aistore/core/meta"
	"github.com/NVIDIA/aistore/fs"
	"github.com/NVIDIA/aistore/ios"
	"github.com/NVIDIA/aistore/memsys"
	"github.com/NVIDIA/aistore/transport"
)

// Local Object Metadata (LOM) is a locally stored object metadata comprising, in part:
// - name, version, atime, checksum, size, etc. object attributes and flags
// - runtime context including properties and configuration of the bucket
//   that contains this LOM

const (
	lomInitialVersion = "1"
)

// core stats
const (
	RemoteDeletedDelCount = "remote.deleted.del.n"

	// lcache stats
	LcacheCollisionCount = "lcache.collision.n"
	LcacheEvictedCount   = "lcache.evicted.n"
	LcacheFlushColdCount = "lcache.flush.cold.n"
)

type (
	lmeta struct {
		copies fs.MPI
		uname  string
		cmn.ObjAttrs
		atimefs uint64 // NOTE: high bit is reserved for `dirty`
		bckID   uint64
	}
	LOM struct {
		mi      *fs.Mountpath
		bck     meta.Bck
		ObjName string
		FQN     string
		HrwFQN  string // (=> main replica)
		md      lmeta  // on-disk metadata
		digest  uint64 // uname digest
	}
)

type (
	global struct {
		tstats   cos.StatsUpdater // (stats.Trunner)
		pmm, smm *memsys.MMSA
		maxLmeta atomic.Int64
		locker   nameLocker
		lchk     lchk
	}
)

var bckLocker nameLocker // common

// target only
var (
	T Target
	g global
)

// interface guard
var (
	_ cos.OAH     = (*LOM)(nil)
	_ fs.PartsFQN = (*LOM)(nil)
	_ lifUnlocker = (*LOM)(nil)
)

func Pinit() { bckLocker = newNameLocker() }

func Tinit(t Target, tstats cos.StatsUpdater, runHK bool) {
	bckLocker = newNameLocker()
	T = t
	{
		g.maxLmeta.Store(xattrMaxSize)
		g.locker = newNameLocker()
		g.tstats = tstats
		g.pmm = t.PageMM()
		g.smm = t.ByteMM()
	}
	if runHK {
		regLomCacheWithHK()
	}
}

func Term() {
	const sleep = time.Second >> 2 // total <= 2s
	for i := 0; i < 8 && !g.lchk.running.CAS(false, true); i++ {
		time.Sleep(sleep)
	}
	g.lchk.evictAll(termDuration)
}

/////////
// LOM //
/////////

func (lom *LOM) ObjAttrs() *cmn.ObjAttrs { return &lom.md.ObjAttrs }

// LOM == remote-object equality check
func (lom *LOM) Equal(rem cos.OAH) bool { return lom.ObjAttrs().Equal(rem) }

func (lom *LOM) CopyAttrs(oah cos.OAH, skipCksum bool) {
	lom.md.ObjAttrs.CopyFrom(oah, skipCksum)
}

// special a) when a new version is being created b) for usage in unit tests
func (lom *LOM) SizeBytes(special ...bool) int64 {
	debug.Assert(len(special) > 0 || lom.loaded(), lom.String())
	return lom.md.Size
}

func (lom *LOM) Version(special ...bool) string {
	debug.Assert(len(special) > 0 || lom.loaded())
	return lom.md.Ver
}

func (lom *LOM) ValidateWarmGet(qparam string /*apc.QparamLatestVer*/) bool {
	switch {
	case !lom.Bck().IsCloud() && !lom.Bck().IsRemoteAIS():
		return false
	case qparam == "":
		return lom.VersionConf().ValidateWarmGet || lom.VersionConf().Sync // bucket prop
	case qparam == "true":
		return true
	default:
		return cos.IsParseBool(qparam)
	}
}

func (lom *LOM) Uname() string  { return lom.md.uname }
func (lom *LOM) Digest() uint64 { return lom.digest }

func (lom *LOM) SetSize(size int64)    { lom.md.Size = size }
func (lom *LOM) SetVersion(ver string) { lom.md.Ver = ver }

func (lom *LOM) Checksum() *cos.Cksum          { return lom.md.Cksum }
func (lom *LOM) SetCksum(cksum *cos.Cksum)     { lom.md.Cksum = cksum }
func (lom *LOM) EqCksum(cksum *cos.Cksum) bool { return lom.md.Cksum.Equal(cksum) }

func (lom *LOM) Atime() time.Time      { return time.Unix(0, lom.md.Atime) }
func (lom *LOM) AtimeUnix() int64      { return lom.md.Atime }
func (lom *LOM) SetAtimeUnix(tu int64) { lom.md.Atime = tu }

// custom metadata
func (lom *LOM) GetCustomMD() cos.StrKVs   { return lom.md.GetCustomMD() }
func (lom *LOM) SetCustomMD(md cos.StrKVs) { lom.md.SetCustomMD(md) }

func (lom *LOM) GetCustomKey(key string) (string, bool) { return lom.md.GetCustomKey(key) }
func (lom *LOM) SetCustomKey(key, value string)         { lom.md.SetCustomKey(key, value) }

// lom <= transport.ObjHdr (NOTE: caller must call freeLOM)
func AllocLomFromHdr(hdr *transport.ObjHdr) (lom *LOM, err error) {
	lom = AllocLOM(hdr.ObjName)
	if err = lom.InitBck(&hdr.Bck); err != nil {
		return
	}
	lom.CopyAttrs(&hdr.ObjAttrs, false /*skip checksum*/)
	return
}

func (lom *LOM) ECEnabled() bool { return lom.Bprops().EC.Enabled }
func (lom *LOM) IsHRW() bool     { return lom.HrwFQN == lom.FQN } // subj to resilvering

func (lom *LOM) Bprops() *cmn.Bprops { return lom.bck.Props }

func (lom *LOM) MirrorConf() *cmn.MirrorConf  { return &lom.Bprops().Mirror }
func (lom *LOM) CksumConf() *cmn.CksumConf    { return lom.bck.CksumConf() }
func (lom *LOM) CksumType() string            { return lom.bck.CksumConf().Type }
func (lom *LOM) VersionConf() cmn.VersionConf { return lom.bck.VersionConf() }

// as fs.PartsFQN
func (lom *LOM) ObjectName() string       { return lom.ObjName }
func (lom *LOM) Bck() *meta.Bck           { return &lom.bck }
func (lom *LOM) Bucket() *cmn.Bck         { return (*cmn.Bck)(&lom.bck) }
func (lom *LOM) Mountpath() *fs.Mountpath { return lom.mi }
func (lom *LOM) Location() string         { return T.String() + apc.LocationPropSepa + lom.mi.String() }

func ParseObjLoc(loc string) (tname, mpname string) {
	i := strings.IndexByte(loc, apc.LocationPropSepa[0])
	tname, mpname = loc[:i], loc[i+1:]
	return
}

// see also: transport.ObjHdr.Cname()
func (lom *LOM) Cname() string { return lom.bck.Cname(lom.ObjName) }

func (lom *LOM) WritePolicy() (p apc.WritePolicy) {
	if bprops := lom.Bprops(); bprops == nil {
		p = apc.WriteImmediate
	} else {
		p = bprops.WritePolicy.MD
	}
	return
}

func (lom *LOM) loaded() bool { return lom.md.bckID != 0 }

func (lom *LOM) HrwTarget(smap *meta.Smap) (tsi *meta.Snode, local bool, err error) {
	tsi, err = smap.HrwHash2T(lom.digest)
	if err != nil {
		return
	}
	local = tsi.ID() == T.SID()
	return
}

func (lom *LOM) IncVersion() error {
	debug.Assert(lom.Bck().IsAIS())
	if lom.md.Ver == "" {
		lom.SetVersion(lomInitialVersion)
		return nil
	}
	ver, err := strconv.Atoi(lom.md.Ver)
	if err != nil {
		return fmt.Errorf("%s: %v", lom, err)
	}
	lom.SetVersion(strconv.Itoa(ver + 1))
	return nil
}

// Returns stored checksum (if present) and computed checksum (if requested)
// MAY compute and store a missing (xxhash) checksum.
// If xattr checksum is different than lom's metadata checksum, returns error
// and do not recompute checksum even if recompute set to true.
//
// * objects are stored in the cluster with their content checksums and in accordance
//   with their bucket configurations.
// * xxhash is the system-default checksum.
// * user can override the system default on a bucket level, by setting checksum=none.
// * bucket (re)configuration can be done at any time.
// * an object with a bad checksum cannot be retrieved (via GET) and cannot be replicated
//   or migrated.
// * GET and PUT operations support an option to validate checksums.
// * validation is done against a checksum stored with an object (GET), or a checksum
//   provided by a user (PUT).
// * replications and migrations are always protected by checksums.
// * when two objects in the cluster have identical (bucket, object) names and checksums,
//   they are considered to be full replicas of each other.
// ==============================================================================

// ValidateMetaChecksum validates whether checksum stored in lom's in-memory metadata
// matches checksum stored on disk.
// Use lom.ValidateContentChecksum() to recompute and check object's content checksum.
func (lom *LOM) ValidateMetaChecksum() error {
	var (
		md  *lmeta
		err error
	)
	if lom.CksumType() == cos.ChecksumNone {
		return nil
	}
	wmd := lom.WritePolicy()
	if wmd == apc.WriteNever || (wmd == apc.WriteDelayed && lom.md.isDirty()) {
		// cannot validate meta checksum
		return nil
	}
	md, err = lom.lmfsReload(false)
	if err != nil {
		return err
	}
	if md == nil {
		return fmt.Errorf("%s: no meta", lom)
	}
	if lom.md.Cksum == nil {
		lom.SetCksum(md.Cksum)
		return nil
	}
	// different versions may have different checksums
	if md.Ver == lom.md.Ver && !lom.EqCksum(md.Cksum) {
		err = cos.NewErrDataCksum(lom.md.Cksum, md.Cksum, lom.String())
		lom.Uncache()
	}
	return err
}

// ValidateDiskChecksum validates if checksum stored in lom's in-memory metadata
// matches object's content checksum.
// Use lom.ValidateMetaChecksum() to check lom's checksum vs on-disk metadata.
func (lom *LOM) ValidateContentChecksum() (err error) {
	var (
		cksumType = lom.CksumType()
		cksums    = struct {
			stor *cos.Cksum     // stored with LOM
			comp *cos.CksumHash // computed
		}{stor: lom.md.Cksum}
		reloaded bool
	)
recomp:
	if cksumType == cos.ChecksumNone { // as far as do-no-checksum-checking bucket rules
		return
	}
	if !lom.md.Cksum.IsEmpty() {
		cksumType = lom.md.Cksum.Ty() // takes precedence on the other hand
	}
	if cksums.comp, err = lom.ComputeCksum(cksumType); err != nil {
		return
	}
	if lom.md.Cksum.IsEmpty() { // store computed
		lom.md.Cksum = cksums.comp.Clone()
		if !lom.loaded() {
			lom.SetAtimeUnix(time.Now().UnixNano())
		}
		if err = lom.Persist(); err != nil {
			lom.md.Cksum = cksums.stor
		}
		return
	}
	if cksums.comp.Equal(lom.md.Cksum) {
		return
	}
	if reloaded {
		goto ex
	}
	// retry: load from disk and check again
	reloaded = true
	if _, err = lom.lmfsReload(true); err == nil && lom.md.Cksum != nil {
		// type changed - recompute
		if cksumType != lom.md.Cksum.Ty() {
			cksums.stor = lom.md.Cksum
			cksumType = lom.CksumType()
			goto recomp
		}
		// otherwise, check
		if cksums.comp.Equal(lom.md.Cksum) {
			return
		}
	}
ex:
	err = cos.NewErrDataCksum(&cksums.comp.Cksum, cksums.stor, lom.String())
	lom.Uncache()
	return
}

func (lom *LOM) ComputeSetCksum() (*cos.Cksum, error) {
	var (
		cksum          *cos.Cksum
		cksumHash, err = lom.ComputeCksum(lom.CksumType())
	)
	if err != nil {
		return nil, err
	}
	if cksumHash != nil {
		cksum = cksumHash.Clone()
	}
	lom.SetCksum(cksum)
	return cksum, nil
}

func (lom *LOM) ComputeCksum(cksumType string) (cksum *cos.CksumHash, err error) {
	var file *os.File
	if cksumType == cos.ChecksumNone {
		return
	}
	if file, err = os.Open(lom.FQN); err != nil {
		return
	}
	// No need to allocate `buf` as `io.Discard` has efficient `io.ReaderFrom` implementation.
	_, cksum, err = cos.CopyAndChecksum(io.Discard, file, nil, cksumType)
	cos.Close(file)
	if err != nil {
		return nil, err
	}
	return
}

// no lock is taken when locked by an immediate caller, or otherwise is known to be locked
// otherwise, try Rlock temporarily _if and only when_ reading from fs
//
// (compare w/ LoadUnsafe() below)
func (lom *LOM) Load(cacheit, locked bool) error {
	var (
		lcache, lmd = lom.fromCache()
		bmd         = T.Bowner().Get()
	)
	// fast path
	if lmd != nil {
		lom.md = *lmd
		return lom._checkBucket(bmd)
	}

	// slow path
	if !locked && lom.TryLock(false) {
		defer lom.Unlock(false)
	}
	if err := lom.FromFS(); err != nil {
		return err
	}
	bid := lom.Bprops().BID
	debug.Assert(bid != 0, lom.Cname())
	if bid == 0 {
		return nil
	}
	lom.md.bckID = bid
	if err := lom._checkBucket(bmd); err != nil {
		return err
	}
	if cacheit && lcache != nil {
		md := lom.md
		lcache.Store(lom.digest, &md)
	}
	return nil
}

func (lom *LOM) _checkBucket(bmd *meta.BMD) (err error) {
	bck, bckID := &lom.bck, lom.md.bckID
	debug.Assert(bckID != 0)
	bprops, present := bmd.Get(bck)
	if !present {
		if bck.IsRemote() {
			return cmn.NewErrRemoteBckNotFound(bck.Bucket())
		}
		return cmn.NewErrBckNotFound(bck.Bucket())
	}
	if bckID == bprops.BID {
		return nil // ok
	}
	err = cmn.NewErrObjDefunct(lom.String(), lom.md.bckID, lom.bck.Props.BID)
	return
}

// usage: fast (and unsafe) loading object metadata except atime - no locks
// compare with conventional Load() above
func (lom *LOM) LoadUnsafe() (err error) {
	var (
		_, lmd = lom.fromCache()
		bmd    = T.Bowner().Get()
	)
	// fast path
	if lmd != nil {
		lom.md = *lmd
		err = lom._checkBucket(bmd)
		return
	}
	// read and decode xattr; NOTE: fs.GetXattr* vs fs.SetXattr race possible and must be
	// either a) handled or b) benign from the caller's perspective
	if _, err = lom.lmfs(true); err != nil {
		return
	}
	// check bucket
	bid := lom.Bprops().BID
	debug.Assert(bid != 0, lom.Cname())
	if bid == 0 {
		return
	}
	lom.md.bckID = bid
	return lom._checkBucket(bmd)
}

//
// lom cache -------------------------------------------------------------
//

// store new or refresh existing
func (lom *LOM) Recache() {
	debug.Assert(!lom.IsCopy())
	md := lom.md
	bid := lom.Bprops().BID
	debug.Assert(bid != 0)
	md.bckID, lom.md.bckID = bid, bid

	lcache := lom.lcache()
	val, ok := lcache.Swap(lom.digest, &md)
	if !ok {
		return
	}
	lmd := val.(*lmeta)
	if lmd.uname != lom.md.uname {
		g.tstats.Inc(LcacheCollisionCount) // target stats
	} else {
		// updating the value that's already in the map (race extremely unlikely, benign anyway)
		md.cpAtime(lmd)
	}
}

func (lom *LOM) Uncache() {
	lcache := lom.lcache()
	md, ok := lcache.LoadAndDelete(lom.digest)
	if !ok {
		return
	}
	lmd := md.(*lmeta)
	if lmd.uname != lom.md.uname {
		g.tstats.Inc(LcacheCollisionCount) // target stats
	} else {
		lom.md.cpAtime(lmd)
	}
}

// remove from cache unless dirty
func (lom *LOM) UncacheUnless() {
	lcache, lmd := lom.fromCache()
	if lmd == nil {
		return
	}
	if !lmd.isDirty() {
		lom.md.cpAtime(lmd)
		lcache.Delete(lom.md.uname)
	}
}

func (lom *LOM) CacheIdx() int     { return fs.LcacheIdx(lom.digest) } // (lif.CacheIdx())
func (lom *LOM) lcache() *sync.Map { return lom.mi.LomCache(lom.CacheIdx()) }

func (lom *LOM) fromCache() (lcache *sync.Map, lmd *lmeta) {
	lcache = lom.lcache()
	if md, ok := lcache.Load(lom.digest); ok {
		lmd = md.(*lmeta)
		if lmd.uname != lom.md.uname {
			g.tstats.Inc(LcacheCollisionCount) // target stats
		}
	}
	return
}

func (lom *LOM) FromFS() error {
	finfo, atimefs, err := ios.FinfoAtime(lom.FQN)
	if err != nil {
		if !os.IsNotExist(err) {
			err = os.NewSyscallError("stat", err)
			T.FSHC(err, lom.FQN)
		}
		return err
	}
	if _, err = lom.lmfs(true); err != nil {
		// retry once
		if cmn.IsErrLmetaNotFound(err) {
			runtime.Gosched()
			_, err = lom.lmfs(true)
		}
	}
	if err != nil {
		if !cmn.IsErrLmetaNotFound(err) {
			T.FSHC(err, lom.FQN)
		}
		return err
	}
	// fstat & atime
	if lom.md.Size != finfo.Size() { // corruption or tampering
		return cmn.NewErrLmetaCorrupted(lom.whingeSize(finfo.Size()))
	}
	lom.md.Atime = atimefs
	lom.md.atimefs = uint64(atimefs)
	return nil
}

func (lom *LOM) whingeSize(size int64) error {
	return fmt.Errorf("errsize (%d != %d)", lom.md.Size, size)
}

func lomCaches() []*sync.Map {
	var (
		i              int
		availablePaths = fs.GetAvail()
		cachesCnt      = len(availablePaths) * cos.MultiSyncMapCount
		caches         = make([]*sync.Map, cachesCnt)
	)
	for _, mi := range availablePaths {
		for idx := 0; idx < cos.MultiSyncMapCount; idx++ {
			caches[i] = mi.LomCache(idx)
			i++
		}
	}
	return caches
}

//
// lock/unlock ------------------------------------------
//

func (lom *LOM) getLocker() *nlc { return &g.locker[lom.CacheIdx()] } // (lif.getLocker())

func (lom *LOM) IsLocked() (int /*rc*/, bool /*exclusive*/) {
	nlc := lom.getLocker()
	return nlc.IsLocked(lom.Uname())
}

func (lom *LOM) TryLock(exclusive bool) bool {
	nlc := lom.getLocker()
	return nlc.TryLock(lom.Uname(), exclusive)
}

func (lom *LOM) Lock(exclusive bool) {
	nlc := lom.getLocker()
	nlc.Lock(lom.Uname(), exclusive)
}

func (lom *LOM) UpgradeLock() (finished bool) {
	nlc := lom.getLocker()
	return nlc.UpgradeLock(lom.Uname())
}

func (lom *LOM) DowngradeLock() {
	nlc := lom.getLocker()
	nlc.DowngradeLock(lom.Uname())
}

func (lom *LOM) Unlock(exclusive bool) {
	nlc := lom.getLocker()
	nlc.Unlock(lom.Uname(), exclusive)
}
