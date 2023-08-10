// Package dsort provides distributed massively parallel resharding for very large datasets.
/*
 * Copyright (c) 2018-2023, NVIDIA CORPORATION. All rights reserved.
 */

// Package dsort provides APIs for distributed archive file shuffling.
package dsort

import (
	"fmt"

	"github.com/NVIDIA/aistore/ext/dsort/extract"
	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"
)

func createRecords(keys ...any) *extract.Records {
	records := extract.NewRecords(len(keys))
	for _, key := range keys {
		records.Insert(&extract.Record{Key: key, Name: fmt.Sprintf("%v", key)})
	}
	return records
}

var _ = Describe("SortRecords", func() {
	It("should sort records alphanumerically ascending", func() {
		expected := createRecords("abc", "def")
		fm := createRecords("abc", "def")
		err := sortRecords(fm, &Algorithm{Decreasing: false, ContentKeyType: extract.ContentKeyString})
		Expect(err).ToNot(HaveOccurred())
		Expect(fm).To(Equal(expected))
	})

	It("should sort records alphanumerically ascending when already sorted", func() {
		expected := createRecords("abc", "def")
		fm := createRecords("def", "abc")
		err := sortRecords(fm, &Algorithm{Decreasing: false, ContentKeyType: extract.ContentKeyString})
		Expect(err).ToNot(HaveOccurred())
		Expect(fm).To(Equal(expected))
	})

	It("should sort records alphanumerically descending", func() {
		expected := createRecords("def", "abc")
		fm := createRecords("abc", "def")
		err := sortRecords(fm, &Algorithm{Decreasing: true, ContentKeyType: extract.ContentKeyString})
		Expect(err).ToNot(HaveOccurred())
		Expect(fm).To(Equal(expected))
	})

	It("should sort records alphanumerically descending when already sorted", func() {
		expected := createRecords("def", "abc")
		fm := createRecords("def", "abc")
		err := sortRecords(fm, &Algorithm{Decreasing: true, ContentKeyType: extract.ContentKeyString})
		Expect(err).ToNot(HaveOccurred())
		Expect(fm).To(Equal(expected))
	})

	It("should sort records alphanumerically ascending when keys are ints", func() {
		expected := createRecords(int64(10), int64(20))
		fm := createRecords(int64(20), int64(10))
		err := sortRecords(fm, &Algorithm{Decreasing: false, ContentKeyType: extract.ContentKeyInt})
		Expect(err).ToNot(HaveOccurred())
		Expect(fm).To(Equal(expected))
	})

	It("should sort records alphanumerically ascending when keys are floats", func() {
		expected := createRecords(float64(10.20), float64(20.10))
		fm := createRecords(float64(20.10), float64(10.20))
		err := sortRecords(fm, &Algorithm{Decreasing: false, ContentKeyType: extract.ContentKeyFloat})
		Expect(err).ToNot(HaveOccurred())
		Expect(fm).To(Equal(expected))
	})

	It("should not sort records when none algorithm specified", func() {
		expected := createRecords("def", "abc")
		fm := createRecords("def", "abc")
		err := sortRecords(fm, &Algorithm{Kind: None, ContentKeyType: extract.ContentKeyString})
		Expect(err).ToNot(HaveOccurred())
		Expect(fm).To(Equal(expected))
	})

	It("should shuffle records reproducibly when same seed specified", func() {
		expected := createRecords("def", "abc")
		fm := createRecords("abc", "def")
		err := sortRecords(fm, &Algorithm{Kind: Shuffle, Seed: "1010102", ContentKeyType: extract.ContentKeyString})
		Expect(err).ToNot(HaveOccurred())
		Expect(fm).To(Equal(expected))
	})

	It("should return error when some keys are missing", func() {
		fm := createRecords("def", "abc")
		fm.All()[0].Key = nil

		err := sortRecords(fm, &Algorithm{Decreasing: true, ContentKeyType: extract.ContentKeyString})
		Expect(err).To(HaveOccurred())
	})
})
