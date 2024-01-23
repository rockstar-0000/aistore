// Package fs provides mountpath and FQN abstractions and methods to resolve/map stored content
/*
 * Copyright (c) 2018-2024, NVIDIA CORPORATION. All rights reserved.
 */
package fs

import (
	"github.com/NVIDIA/aistore/cmn"
	"github.com/NVIDIA/aistore/cmn/cos"
	"github.com/NVIDIA/aistore/cmn/xoshiro256"
	"github.com/OneOfOne/xxhash"
)

// A variant of consistent hash based on rendezvous algorithm by Thaler and Ravishankar,
// aka highest random weight (HRW)
// See also: core/meta/hrw.go

func Hrw(uname string) (mi *Mountpath, digest uint64, err error) {
	var (
		max   uint64
		avail = GetAvail()
	)
	digest = xxhash.Checksum64S(cos.UnsafeB(uname), cos.MLCG32)
	for _, mpathInfo := range avail {
		if mpathInfo.IsAnySet(FlagWaitingDD) {
			continue
		}
		cs := xoshiro256.Hash(mpathInfo.PathDigest ^ digest)
		if cs >= max {
			max = cs
			mi = mpathInfo
		}
	}
	if mi == nil {
		err = cmn.ErrNoMountpaths
	}
	return
}
