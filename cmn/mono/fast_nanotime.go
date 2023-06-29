//go:build mono

// Package mono provides low-level monotonic time
/*
 * Copyright (c) 2018-2021, NVIDIA CORPORATION. All rights reserved.
 */
package mono

import (
	_ "unsafe" // for go:linkname
)

// https://golang.org/pkg/runtime/?m=all#nanotime
//
//go:linkname NanoTime runtime.nanotime
func NanoTime() int64
