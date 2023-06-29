// Package sys provides methods to read system information
/*
 * Copyright (c) 2018-2021, NVIDIA CORPORATION. All rights reserved.
 */
package sys

import (
	"os"
	"runtime"

	"github.com/NVIDIA/aistore/cmn/nlog"
)

const maxProcsEnvVar = "GOMAXPROCS"

type LoadAvg struct {
	One, Five, Fifteen float64
}

var (
	contCPUs      int
	containerized bool
)

func init() {
	contCPUs = runtime.NumCPU()
	if containerized = isContainerized(); containerized {
		if c, err := containerNumCPU(); err == nil {
			contCPUs = c
		} else {
			nlog.Errorln(err)
		}
	}
}

func Containerized() bool { return containerized }
func NumCPU() int         { return contCPUs }

// SetMaxProcs sets GOMAXPROCS = NumCPU unless already overridden via Go environment
func SetMaxProcs() {
	if val, exists := os.LookupEnv(maxProcsEnvVar); exists {
		nlog.Warningf("GOMAXPROCS is set via Go environment %q: %q", maxProcsEnvVar, val)
		return
	}
	maxprocs := runtime.GOMAXPROCS(0)
	ncpu := NumCPU()
	if maxprocs > ncpu {
		nlog.Warningf("Reducing GOMAXPROCS (%d) to %d (num CPUs)", maxprocs, ncpu)
		runtime.GOMAXPROCS(ncpu)
	}
}
