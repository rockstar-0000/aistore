// Package ios is a collection of interfaces to the local storage subsystem;
// the package includes OS-dependent implementations for those interfaces.
/*
 * Copyright (c) 2018-2021, NVIDIA CORPORATION. All rights reserved.
 */
package ios

import (
	"os"
	"os/exec"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

func DirSizeOnDisk(dirPath string, withNonDirPrefix bool) (uint64, error) {
	// GNU implementation of du uses -b to get apparent size with a block size of 1 and -c to show a total
	cmd := exec.Command("du", "-bc", dirPath)
	return executeDU(cmd, dirPath, withNonDirPrefix, 1)
}

func GetFSStats(path string) (blocks, bavail uint64, bsize int64, err error) {
	var fsStats unix.Statfs_t
	fsStats, err = getFSStats(path)
	if err != nil {
		return
	}
	return fsStats.Blocks, fsStats.Bavail, fsStats.Bsize, nil
}

func GetATime(osfi os.FileInfo) time.Time {
	stat := osfi.Sys().(*syscall.Stat_t)
	atime := time.Unix(stat.Atim.Sec, stat.Atim.Nsec)
	// NOTE: see https://en.wikipedia.org/wiki/Stat_(system_call)#Criticism_of_atime
	return atime
}
