// Package http2 puts a given number of files with a given size into AIStore.
// See /bench/http2/README.md for more info.
/*
 * Copyright (c) 2018-2021, NVIDIA CORPORATION. All rights reserved.
 *
 */
package main

import (
	"flag"
	"fmt"
	"sync"

	"github.com/NVIDIA/aistore/cmn"
	"github.com/NVIDIA/aistore/cmn/cos"
	"github.com/NVIDIA/aistore/tools"
	"github.com/NVIDIA/aistore/tools/readers"
)

const (
	kilobytes = uint64(1024)
	smokeDir  = "/tmp/ais/smoke"        // smoke test dir
	ProxyURL  = "http://localhost:8080" // assuming local proxy is listening on 8080
)

var (
	files    int
	workers  int
	filesize uint64
	bucket   cmn.Bck
	url      string
)

func init() {
	flag.StringVar(&url, "url", ProxyURL, "Proxy URL")
	flag.StringVar(&bucket.Name, "bucket", "local_benchmark_bucket", "AWS or GCP bucket")
	flag.StringVar(&bucket.Provider, "provider", "", "backend provider")
	flag.IntVar(&files, "files", 10, "Number of files to put")
	flag.IntVar(&workers, "workers", 10, "Number of workers")
	flag.Uint64Var(&filesize, "filesize", 1, "Size of files to put in KB")
}

func worker(jobs <-chan func()) {
	for j := range jobs {
		j()
	}
}

func main() {
	flag.Parse()
	jobs := make(chan func(), files)

	for w := 0; w < workers; w++ {
		go worker(jobs)
	}

	err := putSpecificFiles(filesize*kilobytes, files, bucket, jobs)
	if err != nil {
		fmt.Printf("%v\n", err)
		return
	}
}

func putSpecificFiles(fileSize uint64, numPuts int, bck cmn.Bck, pool chan func()) error {
	var (
		errCh = make(chan error, numPuts+1)
		wg    = &sync.WaitGroup{}
	)

	cos.CreateDir(smokeDir)

	for i := 1; i < numPuts+1; i++ {
		r, err := readers.NewRandReader(int64(fileSize), cos.ChecksumXXHash)
		if err != nil {
			return err
		}

		fname := fmt.Sprintf("l%d", i)
		wg.Add(1)
		pool <- func() {
			defer wg.Done()
			tools.Put(url, bck, "__bench/"+fname, r, errCh)
		}
	}
	close(pool)
	wg.Wait()

	select {
	case err := <-errCh:
		return err
	default:
		return nil
	}
}
