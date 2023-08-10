// Package nlog - aistore logger, provides buffering, timestamping, writing, and
// flushing/syncing/rotating
/*
 * Copyright (c) 2023, NVIDIA CORPORATION. All rights reserved.
 */
package nlog

import (
	"flag"
	"time"

	"github.com/NVIDIA/aistore/cmn/mono"
)

var (
	MaxSize int64 = 4 * 1024 * 1024
)

func InitFlags(flset *flag.FlagSet) {
	flset.BoolVar(&toStderr, "logtostderr", false, "log to standard error instead of files")
	flset.BoolVar(&alsoToStderr, "alsologtostderr", false, "log to standard error as well as files")
}

func InfoDepth(depth int, args ...any)    { log(sevInfo, depth, "", args...) }
func Infoln(args ...any)                  { log(sevInfo, 0, "", args...) }
func Infof(format string, args ...any)    { log(sevInfo, 0, format, args...) }
func Warningln(args ...any)               { log(sevWarn, 0, "", args...) }
func Warningf(format string, args ...any) { log(sevWarn, 0, format, args...) }
func ErrorDepth(depth int, args ...any)   { log(sevErr, depth, "", args...) }
func Errorln(args ...any)                 { log(sevErr, 0, "", args...) }
func Errorf(format string, args ...any)   { log(sevErr, 0, format, args...) }

func SetLogDirRole(dir, role string) { logDir, aisrole = dir, role }
func SetTitle(s string)              { title = s }

func InfoLogName() string { return sname() + ".INFO" }
func ErrLogName() string  { return sname() + ".ERROR" }

func Flush(exit ...bool) {
	var (
		ex  = len(exit) > 0 && exit[0]
		now = mono.NanoTime()
	)
	for _, sev := range []severity{sevInfo, sevErr} {
		var (
			nlog = nlogs[sev]
			oob  bool
		)

		nlog.mw.Lock()
		if nlog.file == nil || nlog.pw.length() == 0 {
			nlog.mw.Unlock()
			continue
		}
		if ex || nlog.pw.avail() < maxLineSize || nlog.since(now) > 10*time.Second {
			nlog.toFlush = append(nlog.toFlush, nlog.pw)
			nlog.get()
		}
		oob = len(nlog.toFlush) > 0
		nlog.mw.Unlock()

		if oob {
			nlog.flush()
		}
		if ex {
			nlog.file.Sync()
			nlog.file.Close()
		}
	}
}

func Since() time.Duration {
	now := mono.NanoTime()
	a, b := nlogs[sevInfo].since(now), nlogs[sevErr].since(now)
	if a > b {
		return a
	}
	return b
}

func OOB() bool {
	return nlogs[sevInfo].oob.Load() || nlogs[sevErr].oob.Load()
}
