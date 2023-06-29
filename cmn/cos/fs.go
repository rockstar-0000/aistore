// Package cos provides common low-level types and utilities for all aistore projects
/*
 * Copyright (c) 2021-2023, NVIDIA CORPORATION. All rights reserved.
 */
package cos

import (
	"bytes"
	"errors"
	"fmt"

	jsoniter "github.com/json-iterator/go"
)

// FsID is unified and cross-platform syscall.Fsid type which implements JSON marshaling.
type FsID [2]int32

func (d FsID) MarshalJSON() ([]byte, error) { return jsoniter.Marshal(d.String()) }
func (d FsID) String() string               { return fmt.Sprintf("%d,%d", d[0], d[1]) }
func (d *FsID) UnmarshalJSON(b []byte) error {
	v := bytes.Split(bytes.Trim(b, `"`), []byte{','})
	if len(v) != 2 {
		return errors.New("invalid fsid, expected 2 numbers separated by comma")
	}
	for i := 0; i < 2; i++ {
		if err := jsoniter.Unmarshal(v[i], &d[i]); err != nil {
			return err
		}
	}
	return nil
}
