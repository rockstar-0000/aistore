// Package xs_test - basic list-concatenation unit tests.
/*
 * Copyright (c) 2018-2023, NVIDIA CORPORATION. All rights reserved.
 */
package xs_test

import (
	"testing"

	"github.com/NVIDIA/aistore/cmn"
	"github.com/NVIDIA/aistore/tools/tassert"
	"github.com/NVIDIA/aistore/tools/trand"
)

func TestConcatObjLists(t *testing.T) {
	if testing.Short() {
		t.Skipf("skipping %s in short mode", t.Name())
	}
	tests := []struct {
		name      string
		objCounts []int
		maxSize   uint
		token     bool
	}{
		// * `st` stands for "single target"
		{name: "st/all", objCounts: []int{10}, maxSize: 0, token: false},
		{name: "st/half", objCounts: []int{10}, maxSize: 5, token: true},
		{name: "st/all_with_marker", objCounts: []int{10}, maxSize: 10, token: true},
		{name: "st/more_than_all", objCounts: []int{10}, maxSize: 11, token: false},

		// * `mt` stands for "multiple targets"
		// * `one` stands for "one target has objects"
		{name: "mt/one/all", objCounts: []int{0, 0, 10}, maxSize: 0, token: false},
		{name: "mt/one/half", objCounts: []int{0, 0, 10}, maxSize: 5, token: true},
		{name: "mt/one/all_with_marker", objCounts: []int{0, 0, 10}, maxSize: 10, token: true},
		{name: "mt/one/more_than_all", objCounts: []int{0, 0, 10}, maxSize: 11, token: false},

		// * `mt` stands for "multiple targets"
		// * `more` stands for "more than one target has objects"
		{name: "mt/more/all", objCounts: []int{5, 1, 4, 10}, maxSize: 0, token: false},
		{name: "mt/more/half", objCounts: []int{5, 1, 4, 10}, maxSize: 10, token: true},
		{name: "mt/more/all_with_marker", objCounts: []int{5, 1, 4, 10}, maxSize: 20, token: true},
		{name: "mt/more/more_than_all", objCounts: []int{5, 1, 4, 10}, maxSize: 21, token: false},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var (
				lists          = make([]*cmn.LsoResult, 0, len(test.objCounts))
				expectedObjCnt = 0
			)
			for _, objCount := range test.objCounts {
				list := &cmn.LsoResult{}
				for i := 0; i < objCount; i++ {
					list.Entries = append(list.Entries, &cmn.LsoEntry{
						Name: trand.String(5),
					})
				}
				lists = append(lists, list)
				expectedObjCnt += len(list.Entries)
			}
			expectedObjCnt = min(expectedObjCnt, int(test.maxSize))

			objs := cmn.ConcatLso(lists, test.maxSize)
			tassert.Errorf(
				t, test.maxSize == 0 || len(objs.Entries) == expectedObjCnt,
				"number of objects (%d) is different from expected (%d)", len(objs.Entries), expectedObjCnt,
			)
			tassert.Errorf(
				t, (objs.ContinuationToken != "") == test.token,
				"continuation token expected to be set=%t", test.token,
			)
		})
	}
}
