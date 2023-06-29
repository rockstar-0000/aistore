// Package fs provides mountpath and FQN abstractions and methods to resolve/map stored content
/*
 * Copyright (c) 2021-2023, NVIDIA CORPORATION. All rights reserved.
 */
package fs

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"syscall"

	"github.com/NVIDIA/aistore/cmn/cos"
)

// fqn2FsInfo is used only at startup to store file systems for each mountpath.
func fqn2FsInfo(fqn string) (fs, fsType string, err error) {
	getFSCommand := fmt.Sprintf("df -PT '%s' | awk 'END{print $1,$2}'", fqn)
	outputBytes, err := exec.Command("sh", "-c", getFSCommand).Output()
	if err != nil || len(outputBytes) == 0 {
		return "", "", fmt.Errorf("failed to retrieve FS info from path %q, err: %v", fqn, err)
	}
	info := strings.Split(string(outputBytes), " ")
	if len(info) != 2 {
		return "", "", fmt.Errorf("failed to retrieve FS info from path %q, err: invalid format", fqn)
	}
	return strings.TrimSpace(info[0]), strings.TrimSpace(info[1]), nil
}

func makeFsInfo(mpath string) (fsInfo cos.FS, err error) {
	var fsStats syscall.Statfs_t
	if err := syscall.Statfs(mpath, &fsStats); err != nil {
		return fsInfo, fmt.Errorf("cannot statfs fspath %q, err: %w", mpath, err)
	}

	fs, fsType, err := fqn2FsInfo(mpath)
	if err != nil {
		return fsInfo, err
	}

	return cos.FS{Fs: fs, FsType: fsType, FsID: fsStats.Fsid.X__val}, nil
}

// DirectOpen opens a file with direct disk access (with OS caching disabled).
func DirectOpen(path string, flag int, perm os.FileMode) (*os.File, error) {
	return os.OpenFile(path, syscall.O_DIRECT|flag, perm)
}
