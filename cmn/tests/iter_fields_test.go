// Package test provides tests for common low-level types and utilities for all aistore projects
/*
 * Copyright (c) 2018-2023, NVIDIA CORPORATION. All rights reserved.
 */
package tests

import (
	"github.com/NVIDIA/aistore/api"
	"github.com/NVIDIA/aistore/api/apc"
	"github.com/NVIDIA/aistore/cmn"
	"github.com/NVIDIA/aistore/cmn/cos"
	. "github.com/onsi/ginkgo"
	. "github.com/onsi/ginkgo/extensions/table"
	. "github.com/onsi/gomega"
)

var _ = Describe("IterFields", func() {
	type (
		Foo struct {
			A int `list:"omit"`
			B int `json:"b"`
		}
		bar struct {
			Foo Foo    `json:"foo"`
			C   string `json:"c"`
		}
		barInline struct {
			Foo `json:",inline"`
			C   string `json:"c"`
		}
	)

	Describe("IterFields", func() {
		DescribeTable("should successfully iterate fields in structs",
			func(v any, expected map[string]any) {
				got := make(map[string]any)
				err := cmn.IterFields(v, func(tag string, field cmn.IterField) (error, bool) {
					got[tag] = field.Value()
					return nil, false
				})
				Expect(err).NotTo(HaveOccurred())
				Expect(got).To(Equal(expected))
			},
			Entry("list BucketProps fields",
				cmn.BucketProps{
					Provider: apc.AIS,
					BackendBck: cmn.Bck{
						Name:     "name",
						Provider: apc.GCP,
					},
					EC: cmn.ECConf{
						Enabled:      true,
						ParitySlices: 1024,
					},
					LRU: cmn.LRUConf{},
					Cksum: cmn.CksumConf{
						Type: cos.ChecksumXXHash,
					},
					Extra: cmn.ExtraProps{
						AWS: cmn.ExtraPropsAWS{CloudRegion: "us-central"},
					},
				},
				map[string]any{
					"provider": apc.AIS,

					"backend_bck.name":     "name",
					"backend_bck.provider": apc.GCP,

					"mirror.enabled":      false,
					"mirror.copies":       int64(0),
					"mirror.burst_buffer": 0,

					"ec.enabled":           true,
					"ec.parity_slices":     1024,
					"ec.data_slices":       0,
					"ec.objsize_limit":     int64(0),
					"ec.compression":       "",
					"ec.bundle_multiplier": 0,
					"ec.disk_only":         false,

					"versioning.enabled":           false,
					"versioning.validate_warm_get": false,

					"checksum.type":              cos.ChecksumXXHash,
					"checksum.validate_warm_get": false,
					"checksum.validate_cold_get": false,
					"checksum.validate_obj_move": false,
					"checksum.enable_read_range": false,

					"lru.enabled":           false,
					"lru.dont_evict_time":   cos.Duration(0),
					"lru.capacity_upd_time": cos.Duration(0),

					"extra.aws.cloud_region": "us-central",
					"extra.aws.endpoint":     "",
					"extra.aws.profile":      "",

					"access":  apc.AccessAttrs(0),
					"created": int64(0),

					"write_policy.data": apc.WritePolicy(""),
					"write_policy.md":   apc.WritePolicy(""),
				},
			),
			Entry("list BucketPropsToUpdate fields",
				&cmn.BucketPropsToUpdate{
					EC: &cmn.ECConfToUpdate{
						Enabled:      api.Bool(true),
						ParitySlices: api.Int(1024),
					},
					LRU: &cmn.LRUConfToUpdate{},
					Cksum: &cmn.CksumConfToUpdate{
						Type: api.String(cos.ChecksumXXHash),
					},
					Access: api.AccessAttrs(1024),
					WritePolicy: &cmn.WritePolicyConfToUpdate{
						MD: api.WritePolicy(apc.WriteDelayed),
					},
				},
				map[string]any{
					"backend_bck.name":     (*string)(nil),
					"backend_bck.provider": (*string)(nil),

					"mirror.enabled":      (*bool)(nil),
					"mirror.copies":       (*int64)(nil),
					"mirror.burst_buffer": (*int)(nil),

					"ec.enabled":           api.Bool(true),
					"ec.parity_slices":     api.Int(1024),
					"ec.data_slices":       (*int)(nil),
					"ec.objsize_limit":     (*int64)(nil),
					"ec.compression":       (*string)(nil),
					"ec.bundle_multiplier": (*int)(nil),
					"ec.disk_only":         (*bool)(nil),

					"versioning.enabled":           (*bool)(nil),
					"versioning.validate_warm_get": (*bool)(nil),

					"checksum.type":              api.String(cos.ChecksumXXHash),
					"checksum.validate_warm_get": (*bool)(nil),
					"checksum.validate_cold_get": (*bool)(nil),
					"checksum.validate_obj_move": (*bool)(nil),
					"checksum.enable_read_range": (*bool)(nil),

					"lru.enabled":           (*bool)(nil),
					"lru.dont_evict_time":   (*cos.Duration)(nil),
					"lru.capacity_upd_time": (*cos.Duration)(nil),

					"access": api.AccessAttrs(1024),

					"write_policy.data": (*apc.WritePolicy)(nil),
					"write_policy.md":   api.WritePolicy(apc.WriteDelayed),

					"extra.hdfs.ref_directory": (*string)(nil),
					"extra.aws.cloud_region":   (*string)(nil),
					"extra.aws.endpoint":       (*string)(nil),
					"extra.aws.profile":        (*string)(nil),
					"extra.http.original_url":  (*string)(nil),
				},
			),
			Entry("check for omit tag",
				Foo{A: 1, B: 2},
				map[string]any{
					"b": 2,
				},
			),
		)

		It("list all the fields (not only leafs)", func() {
			v := bar{Foo: Foo{A: 3, B: 10}, C: "string"}
			expected := map[string]any{
				"foo.b": 10,
				"foo":   Foo{A: 3, B: 10},
				"c":     "string",
			}

			got := make(map[string]any)
			err := cmn.IterFields(v, func(tag string, field cmn.IterField) (error, bool) {
				got[tag] = field.Value()
				return nil, false
			}, cmn.IterOpts{VisitAll: true})
			Expect(err).NotTo(HaveOccurred())
			Expect(got).To(Equal(expected))
		})

		It("list inline fields", func() {
			v := barInline{Foo: Foo{A: 3, B: 10}, C: "string"}
			expected := map[string]any{
				"b": 10,
				"":  Foo{A: 3, B: 10},
				"c": "string",
			}

			got := make(map[string]any)
			err := cmn.IterFields(v, func(tag string, field cmn.IterField) (error, bool) {
				got[tag] = field.Value()
				return nil, false
			}, cmn.IterOpts{VisitAll: true})
			Expect(err).NotTo(HaveOccurred())
			Expect(got).To(Equal(expected))
		})
	})

	Describe("UpdateFieldValue", func() {
		DescribeTable("should successfully update the fields in struct",
			func(v any, values map[string]any, expected any) {
				for name, value := range values {
					err := cmn.UpdateFieldValue(v, name, value)
					Expect(err).NotTo(HaveOccurred())
				}
				Expect(v).To(Equal(expected))
			},
			Entry("update some BucketProps",
				&cmn.BucketProps{
					Versioning: cmn.VersionConf{
						ValidateWarmGet: true,
					},
				},
				map[string]any{
					"mirror.enabled":      "true", // type == bool
					"mirror.copies":       "120",  // type == int
					"mirror.burst_buffer": "9560", // type == int64

					"ec.enabled":       true,
					"ec.parity_slices": 1024,
					"ec.objsize_limit": int64(0),
					"ec.compression":   "",

					"versioning.enabled": false,

					"checksum.type": cos.ChecksumXXHash,

					"access":          "12", // type == uint64
					"write_policy.md": apc.WriteNever,
				},
				&cmn.BucketProps{
					Mirror: cmn.MirrorConf{
						Enabled: true,
						Copies:  120,
						Burst:   9560,
					},
					EC: cmn.ECConf{
						Enabled:      true,
						ParitySlices: 1024,
					},
					LRU: cmn.LRUConf{},
					Cksum: cmn.CksumConf{
						Type: cos.ChecksumXXHash,
					},
					Versioning: cmn.VersionConf{
						Enabled:         false,
						ValidateWarmGet: true,
					},
					Access:      12,
					WritePolicy: cmn.WritePolicyConf{MD: apc.WriteNever},
				},
			),
			Entry("update some BucketPropsToUpdate",
				&cmn.BucketPropsToUpdate{
					Cksum: &cmn.CksumConfToUpdate{
						ValidateWarmGet: api.Bool(true),
					},
				},
				map[string]any{
					"mirror.enabled":      "true", // type == bool
					"mirror.copies":       "120",  // type == int
					"mirror.burst_buffer": "9560", // type == int64

					"ec.enabled":       true,
					"ec.parity_slices": 1024,
					"ec.objsize_limit": int64(0),
					"ec.compression":   "",

					"versioning.enabled": false,

					"checksum.type": cos.ChecksumXXHash,

					"access":          "12", // type == uint64
					"write_policy.md": apc.WriteNever,
				},
				&cmn.BucketPropsToUpdate{
					Versioning: &cmn.VersionConfToUpdate{
						Enabled: api.Bool(false),
					},
					Mirror: &cmn.MirrorConfToUpdate{
						Enabled: api.Bool(true),
						Copies:  api.Int64(120),
						Burst:   api.Int(9560),
					},
					EC: &cmn.ECConfToUpdate{
						Enabled:      api.Bool(true),
						ParitySlices: api.Int(1024),
						ObjSizeLimit: api.Int64(0),
						Compression:  api.String(""),
					},
					Cksum: &cmn.CksumConfToUpdate{
						Type:            api.String(cos.ChecksumXXHash),
						ValidateWarmGet: api.Bool(true),
					},
					Access: api.AccessAttrs(12),
					WritePolicy: &cmn.WritePolicyConfToUpdate{
						MD: api.WritePolicy(apc.WriteNever),
					},
				},
			),
		)

		DescribeTable("should error on update",
			func(v any, values map[string]any) {
				for name, value := range values {
					err := cmn.UpdateFieldValue(v, name, value)
					Expect(err).To(HaveOccurred())
				}
			},
			Entry("non-pointer struct", cmn.BucketProps{}, map[string]any{
				"mirror.enabled": true,
			}),
			Entry("readonly field", &cmn.BucketProps{}, map[string]any{
				"provider": apc.AIS,
			}),
			Entry("field not found", &Foo{}, map[string]any{
				"foo.bar": 2,
			}),
		)

		DescribeTable("should error on update",
			func(orig *cmn.ConfigToUpdate, merge *cmn.ConfigToUpdate, expected *cmn.ConfigToUpdate) {
				orig.Merge(merge)
				Expect(orig).To(Equal(expected))
			},
			Entry("override configuration", &cmn.ConfigToUpdate{
				Mirror: &cmn.MirrorConfToUpdate{
					Enabled: api.Bool(true),
					Copies:  api.Int64(2),
				},
			}, &cmn.ConfigToUpdate{
				Mirror: &cmn.MirrorConfToUpdate{
					Enabled: api.Bool(false),
				},
			}, &cmn.ConfigToUpdate{
				Mirror: &cmn.MirrorConfToUpdate{
					Enabled: api.Bool(false),
					Copies:  api.Int64(2),
				},
			}),

			Entry("add new fields", &cmn.ConfigToUpdate{
				Mirror: &cmn.MirrorConfToUpdate{
					Enabled: api.Bool(true),
					Copies:  api.Int64(2),
				},
			}, &cmn.ConfigToUpdate{
				Mirror: &cmn.MirrorConfToUpdate{
					Enabled: api.Bool(false),
				},
				EC: &cmn.ECConfToUpdate{
					Enabled: api.Bool(true),
				},
			}, &cmn.ConfigToUpdate{
				Mirror: &cmn.MirrorConfToUpdate{
					Enabled: api.Bool(false),
					Copies:  api.Int64(2),
				},
				EC: &cmn.ECConfToUpdate{
					Enabled: api.Bool(true),
				},
			}),

			Entry("nested fields", &cmn.ConfigToUpdate{
				Net: &cmn.NetConfToUpdate{
					HTTP: &cmn.HTTPConfToUpdate{
						Certificate: api.String("secret"),
					},
				},
			}, &cmn.ConfigToUpdate{
				Net: &cmn.NetConfToUpdate{
					HTTP: &cmn.HTTPConfToUpdate{
						UseHTTPS: api.Bool(true),
					},
				},
			}, &cmn.ConfigToUpdate{
				Net: &cmn.NetConfToUpdate{
					HTTP: &cmn.HTTPConfToUpdate{
						Certificate: api.String("secret"),
						UseHTTPS:    api.Bool(true),
					},
				},
			}),
		)
	})
})
