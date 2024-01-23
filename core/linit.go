// Package core provides core metadata and in-cluster API
/*
 * Copyright (c) 2018-2023, NVIDIA CORPORATION. All rights reserved.
 */
package core

import (
	"fmt"
	"strings"

	"github.com/NVIDIA/aistore/cmn"
	"github.com/NVIDIA/aistore/cmn/debug"
	"github.com/NVIDIA/aistore/core/meta"
	"github.com/NVIDIA/aistore/fs"
)

const fmtErrLinit = "lom-init %s: %s mismatch (%q != %q)"

// Local Object Metadata (LOM) - is cached. Respectively, lifecycle of any given LOM
// instance includes the following steps:
// 1) construct LOM instance and initialize its runtime state: lom = LOM{...}.Init()
// 2) load persistent state (aka lmeta) from one of the LOM caches or the underlying
//    filesystem: lom.Load(); Load(false) also entails *not adding* LOM to caches
//    (useful when deleting or moving objects
// 3) usage: lom.Atime(), lom.Cksum(), and other accessors
//    It is illegal to check LOM's existence and, generally, do almost anything
//    with it prior to loading - see previous
// 4) update persistent state in memory: lom.Set*() methods
//    (requires subsequent re-caching via lom.Recache())
// 5) update persistent state on disk: lom.Persist()
// 6) remove a given LOM instance from cache: lom.Uncache()
// 7) evict an entire bucket-load of LOM cache: cluster.EvictCache(bucket)
// 8) periodic (lazy) eviction followed by access-time synchronization: see LomCacheRunner
// =======================================================================================

func (lom *LOM) InitFQN(fqn string, expbck *cmn.Bck) (err error) {
	var parsedFQN fs.ParsedFQN
	parsedFQN, lom.HrwFQN, err = ResolveFQN(fqn)
	if err != nil {
		return
	}
	debug.Assert(parsedFQN.ContentType == fs.ObjectType)
	lom.FQN = fqn
	lom.mi = parsedFQN.Mountpath
	lom.digest = parsedFQN.Digest
	lom.ObjName = parsedFQN.ObjName
	lom.bck = *(*meta.Bck)(&parsedFQN.Bck)

	if expbck != nil {
		if expbck.Name != parsedFQN.Bck.Name {
			return fmt.Errorf(fmtErrLinit, lom.FQN, "bucket", expbck.String(), parsedFQN.Bck)
		}
		if expbck.Provider != "" && expbck.Provider != lom.bck.Provider {
			return fmt.Errorf(fmtErrLinit, lom.FQN, "provider", lom.bck.Provider, expbck.Provider)
		}
		if !expbck.Ns.IsGlobal() && expbck.Ns != parsedFQN.Bck.Ns {
			return fmt.Errorf(fmtErrLinit, lom.FQN, "namespace", expbck.Ns, parsedFQN.Bck.Ns)
		}
	}
	if err = lom.bck.InitFast(T.Bowner()); err != nil {
		return
	}
	lom.md.uname = lom.bck.MakeUname(lom.ObjName)
	return nil
}

func (lom *LOM) InitCT(ct *CT) {
	debug.Assert(ct.contentType == fs.ObjectType)
	debug.Assert(ct.bck.Props != nil, ct.bck.String()+" must be initialized")
	lom.FQN = ct.fqn
	lom.HrwFQN = ct.hrwFQN
	lom.mi = ct.mi
	lom.digest = ct.digest
	lom.ObjName = ct.objName
	lom.bck = *ct.bck
	lom.md.uname = ct.Uname()
}

func (lom *LOM) InitBck(bck *cmn.Bck) (err error) {
	lom.bck = *(*meta.Bck)(bck)
	if err = lom.bck.InitFast(T.Bowner()); err != nil {
		return
	}
	lom.md.uname = lom.bck.MakeUname(lom.ObjName)
	lom.mi, lom.digest, err = fs.Hrw(lom.md.uname)
	if err != nil {
		return
	}
	lom.FQN = lom.mi.MakePathFQN(lom.Bucket(), fs.ObjectType, lom.ObjName)
	lom.HrwFQN = lom.FQN
	return
}

func (lom *LOM) String() string {
	sb := &strings.Builder{}
	sb.WriteString("o[")
	sb.WriteString(lom.bck.Name)
	sb.WriteByte('/')
	sb.WriteString(lom.ObjName)
	if !lom.loaded() {
		sb.WriteString("(-)")
	}
	sb.WriteByte(']')
	return sb.String()
}

// allocates and copies metadata (in particular, atime and uname)
func (lom *LOM) CloneMD(fqn string) *LOM {
	dst := AllocLOM("")
	*dst = *lom
	dst.md = lom.md
	dst.md.bckID = 0
	dst.md.copies = nil
	dst.FQN = fqn
	return dst
}
