// Package s3 provides Amazon S3 compatibility layer
/*
 * Copyright (c) 2022-2024, NVIDIA CORPORATION. All rights reserved.
 */
package s3

import (
	"encoding/xml"
	"fmt"
	"net/http"
	"strings"

	"github.com/NVIDIA/aistore/api/apc"
	"github.com/NVIDIA/aistore/cmn"
	"github.com/NVIDIA/aistore/cmn/cos"
	"github.com/NVIDIA/aistore/cmn/debug"
	"github.com/NVIDIA/aistore/core"
	"github.com/NVIDIA/aistore/memsys"
)

const ErrPrefix = "aws-error"

type Error struct {
	Code      string
	Message   string
	Resource  string
	RequestID string `xml:"RequestId"`
}

func (e *Error) mustMarshal(sgl *memsys.SGL) {
	sgl.Write([]byte(xml.Header))
	err := xml.NewEncoder(sgl).Encode(e)
	debug.AssertNoErr(err)
}

// with user-friendly tip
func WriteMptErr(w http.ResponseWriter, r *http.Request, err error, ecode int, lom *core.LOM, uploadID string) {
	// specifically, for s3cmd example
	name := strings.Replace(lom.Cname(), apc.AISScheme+apc.BckProviderSeparator, apc.S3Scheme+apc.BckProviderSeparator, 1)
	s3cmd := "s3cmd abortmp " + name + " " + uploadID
	if len(s3cmd) > 50 {
		s3cmd = "\n  " + s3cmd
	}
	e := fmt.Errorf("%v\nUse upload ID %q to cleanup, e.g.: %s", err, uploadID, s3cmd)
	if ecode == 0 {
		ecode = http.StatusInternalServerError
	}
	WriteErr(w, r, e, ecode)
}

func WriteErr(w http.ResponseWriter, r *http.Request, err error, ecode int) {
	var (
		out       Error
		in        *cmn.ErrHTTP
		ok        bool
		allocated bool
	)
	if in, ok = err.(*cmn.ErrHTTP); !ok {
		in = cmn.InitErrHTTP(r, err, ecode)
		allocated = true
	}
	out.Message = in.Message
	switch {
	case cmn.IsErrBucketAlreadyExists(err):
		out.Code = "BucketAlreadyExists"
	case cmn.IsErrBckNotFound(err):
		out.Code = "NoSuchBucket"
	case in.TypeCode != "":
		out.Code = in.TypeCode
	default:
		l := len(ErrPrefix)
		// e.g. "aws-error[NotFound: blah]" as per backend/aws.go _awsErr() formatting
		if strings.HasPrefix(out.Message, ErrPrefix) {
			if i := strings.Index(out.Message[l+1:], ":"); i > 4 {
				code := out.Message[l+1 : l+i+1]
				if cos.IsAlphaNice(code) && code[0] >= 'A' && code[0] <= 'Z' {
					out.Code = code
				}
			}
		}
	}
	sgl := memsys.PageMM().NewSGL(0)
	out.mustMarshal(sgl)

	w.Header().Set(cos.HdrContentType, cos.ContentXML)
	w.Header().Set(cos.HdrContentTypeOptions, "nosniff")

	w.WriteHeader(in.Status)
	sgl.WriteTo2(w)
	sgl.Free()
	if allocated {
		cmn.FreeHterr(in)
	}
}
