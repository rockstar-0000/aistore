// Package archive provides common low-level utilities for testing archives
/*
 * Copyright (c) 2018-2023, NVIDIA CORPORATION. All rights reserved.
 */
package tarch

import (
	"archive/tar"
	"bytes"
	"fmt"
	"io"
	"math/rand"
	"os"
	"strconv"
	"sync"
	"time"

	"github.com/NVIDIA/aistore/cmn/archive"
	"github.com/NVIDIA/aistore/cmn/cos"
	"github.com/NVIDIA/aistore/cmn/debug"
	"github.com/NVIDIA/aistore/ext/dsort/shard"
	"github.com/NVIDIA/aistore/tools/cryptorand"
)

var pool1m, pool128k, pool32k sync.Pool

type (
	FileContent struct {
		Name    string
		Ext     string
		Content []byte
	}
	dummyFile struct {
		name string
		size int64
	}
)

func addBufferToArch(aw archive.Writer, path string, l int, buf []byte) error {
	if buf == nil {
		buf = newBuf(l)
		defer freeBuf(buf)
		buf = buf[:l]
		_, err := cryptorand.Read(buf[:l/3])
		debug.AssertNoErr(err)
		copy(buf[2*l/3:], buf)
	}
	reader := bytes.NewBuffer(buf)
	oah := cos.SimpleOAH{Size: int64(l)}
	return aw.Write(path, oah, reader)
}

func CreateArchRandomFiles(shardName string, tarFormat tar.Format, ext string, fileCnt, fileSize int,
	dup bool, recExts, randNames []string) error {
	wfh, err := cos.CreateFile(shardName)
	if err != nil {
		return err
	}

	aw := archive.NewWriter(ext, wfh, nil, &archive.Opts{TarFormat: tarFormat})
	defer func() {
		aw.Fini()
		wfh.Close()
	}()

	var (
		prevFileName string
		dupIndex     = rand.Intn(fileCnt-1) + 1
	)
	if len(recExts) == 0 {
		recExts = []string{".txt"}
	}
	for i := 0; i < fileCnt; i++ {
		var randomName int
		if randNames == nil {
			randomName = rand.Int()
		}
		for _, ext := range recExts {
			var fileName string
			if randNames == nil {
				fileName = fmt.Sprintf("%d%s", randomName, ext) // generate random names
				if dupIndex == i && dup {
					fileName = prevFileName
				}
			} else {
				fileName = randNames[i]
			}
			if err := addBufferToArch(aw, fileName, fileSize, nil); err != nil {
				return err
			}
			prevFileName = fileName
		}
	}
	return nil
}

func CreateArchCustomFilesToW(w io.Writer, tarFormat tar.Format, ext string, fileCnt, fileSize int,
	customFileType, customFileExt string, missingKeys bool) error {
	aw := archive.NewWriter(ext, w, nil, &archive.Opts{TarFormat: tarFormat})
	defer aw.Fini()
	for i := 0; i < fileCnt; i++ {
		fileName := strconv.Itoa(rand.Int()) // generate random names
		if err := addBufferToArch(aw, fileName+".txt", fileSize, nil); err != nil {
			return err
		}
		// If missingKeys enabled we should only add keys randomly
		if !missingKeys || (missingKeys && rand.Intn(2) == 0) {
			var buf []byte
			// random content
			if err := shard.ValidateContentKeyTy(customFileType); err != nil {
				return err
			}
			switch customFileType {
			case shard.ContentKeyInt:
				buf = []byte(strconv.Itoa(rand.Int()))
			case shard.ContentKeyString:
				buf = []byte(fmt.Sprintf("%d-%d", rand.Int(), rand.Int()))
			case shard.ContentKeyFloat:
				buf = []byte(fmt.Sprintf("%d.%d", rand.Int(), rand.Int()))
			default:
				debug.Assert(false, customFileType) // validated above
			}
			if err := addBufferToArch(aw, fileName+customFileExt, len(buf), buf); err != nil {
				return err
			}
		}
	}
	return nil
}

func CreateArchCustomFiles(shardName string, tarFormat tar.Format, ext string, fileCnt, fileSize int,
	customFileType, customFileExt string, missingKeys bool) error {
	wfh, err := cos.CreateFile(shardName)
	if err != nil {
		return err
	}
	defer wfh.Close()
	return CreateArchCustomFilesToW(wfh, tarFormat, ext, fileCnt, fileSize, customFileType, customFileExt, missingKeys)
}

func newArchReader(mime string, buffer *bytes.Buffer) (ar archive.Reader, err error) {
	if mime == archive.ExtZip {
		// zip is special
		readerAt := bytes.NewReader(buffer.Bytes())
		ar, err = archive.NewReader(mime, readerAt, int64(buffer.Len()))
	} else {
		ar, err = archive.NewReader(mime, buffer)
	}
	return
}

func GetFilesFromArchBuffer(mime string, buffer bytes.Buffer, extension string) ([]FileContent, error) {
	var (
		files   = make([]FileContent, 0, 10)
		ar, err = newArchReader(mime, &buffer)
	)
	if err != nil {
		return nil, err
	}
	rcb := func(filename string, reader cos.ReadCloseSizer, hdr any) (bool, error) {
		var (
			buf bytes.Buffer
			ext = cos.Ext(filename)
		)
		defer reader.Close()
		if extension == ext {
			if _, err := io.Copy(&buf, reader); err != nil {
				return true, err
			}
		}
		files = append(files, FileContent{Name: filename, Ext: ext, Content: buf.Bytes()})
		return false, nil
	}

	_, err = ar.Range("", rcb)
	return files, err
}

func GetFileInfosFromArchBuffer(buffer bytes.Buffer, mime string) ([]os.FileInfo, error) {
	var (
		files   = make([]os.FileInfo, 0, 10)
		ar, err = newArchReader(mime, &buffer)
	)
	if err != nil {
		return nil, err
	}
	rcb := func(filename string, reader cos.ReadCloseSizer, hdr any) (bool, error) {
		files = append(files, newDummyFile(filename, reader.Size()))
		reader.Close()
		return false, nil
	}
	_, err = ar.Range("", rcb)
	return files, err
}

///////////////
// dummyFile //
///////////////

func newDummyFile(name string, size int64) *dummyFile {
	return &dummyFile{
		name: name,
		size: size,
	}
}

func (f *dummyFile) Name() string     { return f.name }
func (f *dummyFile) Size() int64      { return f.size }
func (*dummyFile) Mode() os.FileMode  { return 0 }
func (*dummyFile) ModTime() time.Time { return time.Now() }
func (*dummyFile) IsDir() bool        { return false }
func (*dummyFile) Sys() any           { return nil }

//
// assorted buf pools
//

func newBuf(l int) (buf []byte) {
	switch {
	case l > cos.MiB:
		debug.Assertf(false, "buf size exceeds 1MB: %d", l)
	case l > 128*cos.KiB:
		return newBuf1m()
	case l > 32*cos.KiB:
		return newBuf128k()
	}
	return newBuf32k()
}

func freeBuf(buf []byte) {
	c := cap(buf)
	buf = buf[:c]
	switch c {
	case cos.MiB:
		freeBuf1m(buf)
	case 128 * cos.KiB:
		freeBuf128k(buf)
	case 32 * cos.KiB:
		freeBuf32k(buf)
	default:
		debug.Assertf(false, "unexpected buf size: %d", c)
	}
}

func newBuf1m() (buf []byte) {
	if v := pool1m.Get(); v != nil {
		pbuf := v.(*[]byte)
		buf = *pbuf
	} else {
		buf = make([]byte, cos.MiB)
	}
	return
}

func freeBuf1m(buf []byte) {
	pool1m.Put(&buf)
}

func newBuf128k() (buf []byte) {
	if v := pool128k.Get(); v != nil {
		pbuf := v.(*[]byte)
		buf = *pbuf
	} else {
		buf = make([]byte, 128*cos.KiB)
	}
	return
}

func freeBuf128k(buf []byte) {
	pool128k.Put(&buf)
}

func newBuf32k() (buf []byte) {
	if v := pool32k.Get(); v != nil {
		pbuf := v.(*[]byte)
		buf = *pbuf
	} else {
		buf = make([]byte, 32*cos.KiB)
	}
	return
}

func freeBuf32k(buf []byte) {
	pool32k.Put(&buf)
}
