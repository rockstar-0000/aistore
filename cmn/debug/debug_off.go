//go:build !debug

// Package provides debug utilities
/*
 * Copyright (c) 2018-2021, NVIDIA CORPORATION. All rights reserved.
 */
package debug

import (
	"net/http"
	"sync"
)

func ON() bool { return false }

func Infof(_ string, _ ...any) {}

func Func(_ func()) {}

func Assert(_ bool, _ ...any)            {}
func AssertFunc(_ func() bool, _ ...any) {}
func AssertNoErr(_ error)                {}
func Assertf(_ bool, _ string, _ ...any) {}

func AssertNotPstr(any) {}
func FailTypeCast(any)  {}

func AssertMutexLocked(_ *sync.Mutex)      {}
func AssertRWMutexLocked(_ *sync.RWMutex)  {}
func AssertRWMutexRLocked(_ *sync.RWMutex) {}

func Handlers() map[string]http.HandlerFunc {
	return nil
}
