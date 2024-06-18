package utils

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Utils/Peers tests", func() {

	var nrOfNodes int

	Context("small cluster", func() {
		BeforeEach(func() {
			nrOfNodes = 2
		})
		It("GetNextBatchSize should return correct first batch size", func() {
			Expect(GetNextBatchSize(nrOfNodes, nrOfNodes)).To(Equal(2))
		})
		It("GetNrOfBatches should return correct nr of batches", func() {
			Expect(GetNrOfBatches(nrOfNodes)).To(Equal(1))
		})
	})

	Context("medium cluster", func() {
		BeforeEach(func() {
			nrOfNodes = 20
			// expected batches: 3 + 5*3 + 2 = 7 batches
		})
		It("GetNextBatchSize should return correct first batch size", func() {
			Expect(GetNextBatchSize(nrOfNodes, nrOfNodes)).To(Equal(3))
		})
		It("GetNextBatchSize should return correct next batch size", func() {
			Expect(GetNextBatchSize(nrOfNodes, nrOfNodes-3)).To(Equal(3))
		})
		It("GetNextBatchSize should return correct last batch size", func() {
			Expect(GetNextBatchSize(nrOfNodes, 2)).To(Equal(2))
		})
		It("GetNrOfBatches should return correct nr of batches", func() {
			Expect(GetNrOfBatches(nrOfNodes)).To(Equal(7))
		})
	})

	Context("big cluster", func() {
		BeforeEach(func() {
			nrOfNodes = 53
			// expected batches: 3 + 10*5 = 11 batches
		})
		It("GetNextBatchSize should return correct first batch size", func() {
			Expect(GetNextBatchSize(nrOfNodes, nrOfNodes)).To(Equal(3))
		})
		It("GetNextBatchSize should return correct next batch size", func() {
			Expect(GetNextBatchSize(nrOfNodes, nrOfNodes-3)).To(Equal(5))
		})
		It("GetNextBatchSize should return correct last batch size", func() {
			Expect(GetNextBatchSize(nrOfNodes, 0)).To(Equal(0))
		})
		It("GetNrOfBatches should return correct nr of batches", func() {
			Expect(GetNrOfBatches(nrOfNodes)).To(Equal(11))
		})
	})

})
