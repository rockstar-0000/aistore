// Package apc: API messages and constants
/*
 * Copyright (c) 2018-2024, NVIDIA CORPORATION. All rights reserved.
 */
package apc

import (
	"errors"
	"strings"

	"github.com/NVIDIA/aistore/cmn/cos"
)

// copy & (offline) transform bucket to bucket
type (
	CopyBckMsg struct {
		Prepend   string `json:"prepend"`     // destination naming, as in: dest-obj-name = Prepend + source-obj-name
		Prefix    string `json:"prefix"`      // prefix to select matching _source_ objects or virtual directories
		DryRun    bool   `json:"dry_run"`     // visit all source objects, don't make any modifications
		Force     bool   `json:"force"`       // force running in presence of "limited coexistence" type conflicts
		LatestVer bool   `json:"latest-ver"`  // see also: QparamLatestVer, 'versioning.validate_warm_get', PrefetchMsg
		Sync      bool   `json:"synchronize"` // see also: 'versioning.synchronize'
	}
	Transform struct {
		Name    string       `json:"id,omitempty"`
		Timeout cos.Duration `json:"request_timeout,omitempty"`
	}
	TCBMsg struct {
		// NOTE: objname extension ----------------------------------------------------------------------
		// - resulting object names will have this extension, if specified.
		// - if source bucket has two (or more) objects with the same base name but different extension,
		//   specifying this field might cause unintended override.
		// - this field might not be any longer required - TODO review
		Ext cos.StrKVs `json:"ext"`

		Transform
		CopyBckMsg
	}
)

////////////
// TCBMsg //
////////////

func (msg *TCBMsg) Validate(isEtl bool) (err error) {
	if isEtl && msg.Transform.Name == "" {
		err = errors.New("ETL name can't be empty")
	}
	return
}

// Replace extension and add suffix if provided.
func (msg *TCBMsg) ToName(name string) string {
	if msg.Ext != nil {
		if idx := strings.LastIndexByte(name, '.'); idx >= 0 {
			ext := name[idx+1:]
			if replacement, exists := msg.Ext[ext]; exists {
				name = name[:idx+1] + strings.TrimLeft(replacement, ".")
			}
		}
	}
	if msg.Prepend != "" {
		name = msg.Prepend + name
	}
	return name
}
