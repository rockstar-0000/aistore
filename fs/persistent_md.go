// Package fs provides mountpath and FQN abstractions and methods to resolve/map stored content
/*
 * Copyright (c) 2018-2023, NVIDIA CORPORATION. All rights reserved.
 */
package fs

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/NVIDIA/aistore/cmn"
	"github.com/NVIDIA/aistore/cmn/cos"
	"github.com/NVIDIA/aistore/cmn/debug"
	"github.com/NVIDIA/aistore/cmn/fname"
	"github.com/NVIDIA/aistore/cmn/jsp"
	"github.com/NVIDIA/aistore/cmn/nlog"
	"github.com/NVIDIA/aistore/memsys"
)

const numMarkers = 1

// List of AIS metadata files and directories (basenames only)
var mdFilesDirs = []string{
	fname.MarkersDir,

	fname.Bmd,
	fname.BmdPrevious,

	fname.Vmd,
}

func MarkerExists(marker string) bool {
	markerPath := filepath.Join(fname.MarkersDir, marker)
	return CountPersisted(markerPath) > 0
}

func PersistMarker(marker string) (fatalErr, writeErr error) {
	var (
		cnt             int
		relname         = filepath.Join(fname.MarkersDir, marker)
		availableMpaths = GetAvail()
	)
	if len(availableMpaths) == 0 {
		fatalErr = cmn.ErrNoMountpaths
		return
	}
	for _, mi := range availableMpaths {
		fpath := filepath.Join(mi.Path, relname)
		if err := cos.Stat(fpath); err == nil {
			cnt++
			if cnt > numMarkers {
				if err := cos.RemoveFile(fpath); err != nil {
					if writeErr == nil {
						writeErr = err
					}
					nlog.Errorf("Failed to cleanup %q marker: %v", fpath, err)
				} else {
					cnt--
				}
			}
		} else if cnt < numMarkers {
			if file, err := cos.CreateFile(fpath); err == nil {
				writeErr = err
				file.Close()
				cnt++
			} else {
				nlog.Errorf("Failed to create %q marker: %v", fpath, err)
			}
		}
	}
	if cnt == 0 {
		fatalErr = fmt.Errorf("failed to persist %q marker (%d)", marker, len(availableMpaths))
	}
	return
}

func RemoveMarker(marker string) (err error) {
	var (
		availableMpaths = GetAvail()
		relname         = filepath.Join(fname.MarkersDir, marker)
	)
	for _, mi := range availableMpaths {
		if er1 := cos.RemoveFile(filepath.Join(mi.Path, relname)); er1 != nil {
			nlog.Errorf("Failed to remove %q marker from %q: %v", relname, mi.Path, er1)
			err = er1
		}
	}
	return
}

// PersistOnMpaths persists `what` on mountpaths under "mountpath.Path/path" filename.
// It does it on maximum `atMost` mountPaths. If `atMost == 0`, it does it on every mountpath.
// If `backupPath != ""`, it removes files from `backupPath` and moves files from `path` to `backupPath`.
// Returns how many times it has successfully stored a file.
func PersistOnMpaths(fname, backupName string, meta jsp.Opts, atMost int, b []byte, sgl *memsys.SGL) (cnt, availCnt int) {
	var (
		wto             io.WriterTo
		bcnt            int
		availableMpaths = GetAvail()
	)
	availCnt = len(availableMpaths)
	debug.Assert(atMost > 0)
	if atMost > availCnt {
		atMost = availCnt
	}
	for _, mi := range availableMpaths {
		if backupName != "" {
			bcnt = mi.backupAtmost(fname, backupName, bcnt, atMost)
		}
		fpath := filepath.Join(mi.Path, fname)
		os.Remove(fpath)
		if cnt >= atMost {
			continue
		}
		if b != nil {
			wto = bytes.NewBuffer(b)
		} else if sgl != nil {
			wto = sgl // not reopening - see sgl.WriteTo()
		}
		if err := jsp.SaveMeta(fpath, meta, wto); err != nil {
			nlog.Errorf("Failed to persist %q on %q, err: %v", fname, mi, err)
		} else {
			cnt++
		}
	}
	debug.Func(func() {
		expected := cos.Min(atMost, availCnt)
		debug.Assertf(cnt == expected, "expected %q to be persisted on %d mountpaths got %d instead",
			fname, expected, cnt)
	})
	return
}

func CountPersisted(fname string) (cnt int) {
	available := GetAvail()
	for mpath := range available {
		fpath := filepath.Join(mpath, fname)
		if err := cos.Stat(fpath); err == nil {
			cnt++
		}
	}
	return
}
