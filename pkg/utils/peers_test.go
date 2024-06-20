package utils

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Utils/Peers tests", func() {

	DescribeTable("Batch size", func(nrOfNodes int, expectedBatchSizes []int) {
		Expect(GetNrOfBatches(nrOfNodes)).To(Equal(len(expectedBatchSizes)))
		remainingNodes := nrOfNodes
		for i := 0; remainingNodes > 0; i++ {
			batchSize := GetNextBatchSize(nrOfNodes, remainingNodes)
			Expect(batchSize).To(Equal(expectedBatchSizes[i]))
			remainingNodes -= batchSize
		}
	},
		Entry("small cluster", 2, []int{2}),
		Entry("medium cluster", 20, []int{3, 3, 3, 3, 3, 3, 2}),
		Entry("big cluster", 53, []int{3, 5, 5, 5, 5, 5, 5, 5, 5, 5, 5}),
	)

})
