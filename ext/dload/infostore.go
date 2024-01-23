// Package dload implements functionality to download resources into AIS cluster from external source.
/*
 * Copyright (c) 2018-2024, NVIDIA CORPORATION. All rights reserved.
 */
package dload

import (
	"errors"
	"net/http"
	"sync"
	"time"

	"github.com/NVIDIA/aistore/cmn/cos"
	"github.com/NVIDIA/aistore/cmn/debug"
	"github.com/NVIDIA/aistore/cmn/kvdb"
	"github.com/NVIDIA/aistore/core"
	"github.com/NVIDIA/aistore/hk"
)

// TODO: stored only in memory, should be persisted at some point (powercycle)
type infoStore struct {
	*downloaderDB
	dljobs map[string]*dljob
	sync.RWMutex
}

func newInfoStore(driver kvdb.Driver) *infoStore {
	db := newDownloadDB(driver)
	is := &infoStore{
		downloaderDB: db,
		dljobs:       make(map[string]*dljob),
	}
	hk.Reg("downloader"+hk.NameSuffix, is.housekeep, hk.DayInterval)
	return is
}

func (is *infoStore) getJob(id string) (*dljob, error) {
	is.RLock()
	defer is.RUnlock()

	if ji, ok := is.dljobs[id]; ok {
		return ji, nil
	}
	return nil, errJobNotFound
}

func (is *infoStore) getList(req *request) (jobs []*dljob) {
	is.RLock()
	for _, job := range is.dljobs {
		if req.onlyActive && !_isRunning(job.finishedTime.Load()) {
			continue
		}
		if req.regex == nil || req.regex.MatchString(job.description) {
			jobs = append(jobs, job)
		}
	}
	is.RUnlock()
	return
}

func (is *infoStore) setJob(job jobif) (njob *dljob) {
	njob = &dljob{
		id:          job.ID(),
		xid:         job.XactID(),
		total:       job.Len(),
		description: job.Description(),
		startedTime: time.Now(),
	}
	is.Lock()
	is.dljobs[job.ID()] = njob
	is.Unlock()
	return
}

func (is *infoStore) incFinished(id string) {
	dljob, err := is.getJob(id)
	debug.AssertNoErr(err)
	dljob.finishedCnt.Inc()
}

func (is *infoStore) incSkipped(id string) {
	dljob, err := is.getJob(id)
	debug.AssertNoErr(err)
	dljob.skippedCnt.Inc()
	dljob.finishedCnt.Inc()
}

func (is *infoStore) incScheduled(id string) {
	dljob, err := is.getJob(id)
	debug.AssertNoErr(err)
	dljob.scheduledCnt.Inc()
}

func (is *infoStore) incErrorCnt(id string) {
	dljob, err := is.getJob(id)
	debug.AssertNoErr(err)
	dljob.errorCnt.Inc()
}

func (is *infoStore) setAllDispatched(id string, dispatched bool) {
	dljob, err := is.getJob(id)
	debug.AssertNoErr(err)
	dljob.allDispatched.Store(dispatched)
}

func (is *infoStore) markFinished(id string) (error, bool /*aborted*/) {
	dljob, err := is.getJob(id)
	if err != nil {
		debug.AssertNoErr(err)
		return err, false
	}
	dljob.finishedTime.Store(time.Now())
	return dljob.valid(), dljob.aborted.Load()
}

func (is *infoStore) setAborted(id string) {
	dljob, err := is.getJob(id)
	debug.AssertNoErr(err)
	dljob.aborted.Store(true)
	// NOTE: Don't set `FinishedTime` yet as we are not fully done.
	//       The job now can be removed but there's no guarantee
	//       that all tasks have been stopped and all resources were freed.
}

func (is *infoStore) delJob(id string) {
	delete(is.dljobs, id)
	is.downloaderDB.delete(id)
}

func (is *infoStore) housekeep() time.Duration {
	const interval = hk.DayInterval

	is.Lock()
	for id, dljob := range is.dljobs {
		if time.Since(dljob.finishedTime.Load()) > interval {
			is.delJob(id)
		}
	}
	is.Unlock()

	return interval
}

func (is *infoStore) checkExists(req *request) (dljob *dljob, err error) {
	dljob, err = is.getJob(req.id)
	if err != nil {
		debug.Assert(errors.Is(err, errJobNotFound))
		err = cos.NewErrNotFound(core.T, "download job "+req.id)
		req.errRsp(err, http.StatusNotFound)
	}
	return
}
