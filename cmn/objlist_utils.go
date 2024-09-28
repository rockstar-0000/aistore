// Package cmn provides common constants, types, and utilities for AIS clients
// and AIStore.
/*
 * Copyright (c) 2018-2024, NVIDIA CORPORATION. All rights reserved.
 */
package cmn

import (
	"path/filepath"
	"sort"
	"strings"

	"github.com/NVIDIA/aistore/api/apc"
	"github.com/NVIDIA/aistore/cmn/cos"
	"github.com/NVIDIA/aistore/cmn/debug"
)

var nilEntry LsoEnt

////////////////
// LsoEntries //
////////////////

func (entries LsoEntries) cmp(i, j int) bool {
	eni, enj := entries[i], entries[j]
	return eni.less(enj)
}

func appSorted(entries LsoEntries, ne *LsoEnt) LsoEntries {
	for i, eni := range entries {
		if eni.IsDir() != ne.IsDir() {
			if eni.IsDir() {
				continue
			}
		} else if ne.Name > eni.Name {
			continue
		}
		// dedup
		if ne.Name == eni.Name {
			if ne.Status() < eni.Status() {
				entries[i] = ne
			}
			return entries
		}
		// append or insert
		if i == len(entries)-1 {
			entries = append(entries, ne)
			entries[i], entries[i+1] = entries[i+1], entries[i]
			return entries
		}
		entries = append(entries, &nilEntry)
		copy(entries[i+1:], entries[i:]) // shift right
		entries[i] = ne
		return entries
	}

	entries = append(entries, ne)
	return entries
}

////////////
// LsoEnt //
////////////

// The terms "cached" and "present" are interchangeable:
// "object is cached" and "is present" is actually the same thing
func (be *LsoEnt) IsPresent() bool { return be.Flags&apc.EntryIsCached != 0 }
func (be *LsoEnt) SetPresent()     { be.Flags |= apc.EntryIsCached }

// see also: "latest-ver", QparamLatestVer, et al.
func (be *LsoEnt) SetVerChanged()     { be.Flags |= apc.EntryVerChanged }
func (be *LsoEnt) IsVerChanged() bool { return be.Flags&apc.EntryVerChanged != 0 }
func (be *LsoEnt) SetVerRemoved()     { be.Flags |= apc.EntryVerRemoved }
func (be *LsoEnt) IsVerRemoved() bool { return be.Flags&apc.EntryVerRemoved != 0 }

func (be *LsoEnt) IsStatusOK() bool   { return be.Status() == 0 }
func (be *LsoEnt) Status() uint16     { return be.Flags & apc.EntryStatusMask }
func (be *LsoEnt) IsDir() bool        { return be.Flags&apc.EntryIsDir != 0 }
func (be *LsoEnt) IsInsideArch() bool { return be.Flags&apc.EntryInArch != 0 }
func (be *LsoEnt) IsListedArch() bool { return be.Flags&apc.EntryIsArchive != 0 }
func (be *LsoEnt) String() string     { return "{" + be.Name + "}" }

func (be *LsoEnt) less(oe *LsoEnt) bool {
	if be.IsDir() {
		if oe.IsDir() {
			return be.Name < oe.Name
		}
		return true
	}
	if oe.IsDir() {
		return false
	}
	if be.Name == oe.Name {
		return be.Status() < oe.Status()
	}
	return be.Name < oe.Name
}

func (be *LsoEnt) CopyWithProps(propsSet cos.StrSet) (ne *LsoEnt) {
	ne = &LsoEnt{Name: be.Name}
	if propsSet.Contains(apc.GetPropsSize) {
		ne.Size = be.Size
	}
	if propsSet.Contains(apc.GetPropsChecksum) {
		ne.Checksum = be.Checksum
	}
	if propsSet.Contains(apc.GetPropsAtime) {
		ne.Atime = be.Atime
	}
	if propsSet.Contains(apc.GetPropsVersion) {
		ne.Version = be.Version
	}
	if propsSet.Contains(apc.GetPropsLocation) {
		ne.Location = be.Location
	}
	if propsSet.Contains(apc.GetPropsCustom) {
		ne.Custom = be.Custom
	}
	if propsSet.Contains(apc.GetPropsCopies) {
		ne.Copies = be.Copies
	}
	return
}

//
// sorting and merging functions
//

func SortLso(entries LsoEntries) { sort.Slice(entries, entries.cmp) }

func DedupLso(entries LsoEntries, maxSize int, noDirs bool) []*LsoEnt {
	var j int
	for _, en := range entries {
		if j > 0 && entries[j-1].Name == en.Name {
			continue
		}

		debug.Assert(!(noDirs && en.IsDir())) // expecting backends for filter out accordingly

		entries[j] = en
		j++

		if maxSize > 0 && j == maxSize {
			break
		}
	}
	clear(entries[j:])
	return entries[:j]
}

// MergeLso merges list-objects results received from targets. For the same
// object name (ie., the same object) the corresponding properties are merged.
// If maxSize is greater than 0, the resulting list is sorted and truncated.
func MergeLso(lists []*LsoRes, lsmsg *apc.LsoMsg, maxSize int) *LsoRes {
	noDirs := lsmsg.IsFlagSet(apc.LsNoDirs)
	if len(lists) == 0 {
		return &LsoRes{}
	}
	resList := lists[0]
	token := resList.ContinuationToken
	if len(lists) == 1 {
		SortLso(resList.Entries)
		resList.Entries = DedupLso(resList.Entries, maxSize, noDirs)
		resList.ContinuationToken = token
		return resList
	}

	tmp := make(map[string]*LsoEnt, len(resList.Entries)*len(lists))
	for _, l := range lists {
		resList.Flags |= l.Flags
		if token < l.ContinuationToken {
			token = l.ContinuationToken
		}
		for _, en := range l.Entries {
			// expecting backends for filter out
			debug.Assert(!(noDirs && en.IsDir()))

			// add new
			entry, exists := tmp[en.Name]
			if !exists {
				tmp[en.Name] = en
				continue
			}
			// merge existing w/ new props
			if !entry.IsPresent() && en.IsPresent() {
				en.Version = cos.Left(en.Version, entry.Version)
				tmp[en.Name] = en
			} else {
				entry.Location = cos.Left(entry.Location, en.Location)
				entry.Version = cos.Left(entry.Version, en.Version)
			}
		}
	}

	// grow cap
	for cap(resList.Entries) < len(tmp) {
		l := min(len(resList.Entries), len(tmp)-cap(resList.Entries))
		resList.Entries = append(resList.Entries, resList.Entries[:l]...)
	}

	// cleanup and sort
	clear(resList.Entries)
	resList.Entries = resList.Entries[:0]
	resList.ContinuationToken = token

	for _, entry := range tmp {
		resList.Entries = appSorted(resList.Entries, entry)
	}
	if maxSize > 0 && len(resList.Entries) > maxSize {
		clear(resList.Entries[maxSize:])
		resList.Entries = resList.Entries[:maxSize]
	}

	clear(tmp)
	return resList
}

// Returns true if the continuation token >= object's name (in other words, the object is
// already listed and must be skipped). Note that string `>=` is lexicographic.
func TokenGreaterEQ(token, objName string) bool { return token >= objName }

// Directory has to either:
// - include (or match) prefix, or
// - be contained in prefix - motivation: don't SkipDir a/b when looking for a/b/c
// An alternative name for this function could be smth. like SameBranch()
func DirHasOrIsPrefix(dirPath, prefix string) bool {
	return prefix == "" || (strings.HasPrefix(prefix, dirPath) || strings.HasPrefix(dirPath, prefix))
}

func ObjHasPrefix(objName, prefix string) bool {
	return prefix == "" || strings.HasPrefix(objName, prefix)
}

// no recursion (LsNoRecursion) helper function:
// - check the level of nesting
// - possibly, return virtual directory (to be included in LsoRes) and/or filepath.SkipDir
func HandleNoRecurs(prefix, relPath string) (*LsoEnt, error) {
	debug.Assert(relPath != "")
	if prefix == "" || prefix == cos.PathSeparator {
		return &LsoEnt{Name: relPath, Flags: apc.EntryIsDir}, filepath.SkipDir
	}

	prefix = cos.TrimLastB(prefix, '/')
	suffix := strings.TrimPrefix(relPath, prefix)
	if suffix == relPath {
		// wrong subtree (unlikely, given higher-level traversal logic)
		return nil, filepath.SkipDir
	}

	if strings.Contains(suffix, cos.PathSeparator) {
		// nesting-wise, we are deeper than allowed by the prefix
		return nil, filepath.SkipDir
	}
	return &LsoEnt{Name: relPath, Flags: apc.EntryIsDir}, nil
}
