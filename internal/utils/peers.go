package utils

const (
	MinNrOfNodesInBatch      = 3
	MaxNrOfBatchesAfterFirst = 10
)

// GetNextBatchSize returns the number of nodes to ask in the next batch
func GetNextBatchSize(totalNrOfNodes, remainingNrOfNodes int) int {

	isFirstBatch := totalNrOfNodes == remainingNrOfNodes
	var batchSize int
	if isFirstBatch {
		// start asking a few nodes only in first iteration to cover the case we get a healthy / unhealthy result
		batchSize = MinNrOfNodesInBatch
	} else {
		// after that ask 10% of the remaining cluster each time
		batchSize = (totalNrOfNodes - MinNrOfNodesInBatch) / MaxNrOfBatchesAfterFirst
		// but never less than MinNrOfNodesInBatch
		if batchSize < MinNrOfNodesInBatch {
			batchSize = MinNrOfNodesInBatch
		}
	}

	// do not ask more than we have in the last batch
	if remainingNrOfNodes < batchSize {
		batchSize = remainingNrOfNodes
	}

	return batchSize
}

// GetNrOfBatches returns the number of batches we need for the given total number of nodes
func GetNrOfBatches(totalNrOfNodes int) int {
	nrOfRemainingNodes := totalNrOfNodes
	nrOfBatches := 0
	for nrOfRemainingNodes > 0 {
		nrOfBatches++
		batchSize := GetNextBatchSize(totalNrOfNodes, nrOfRemainingNodes)
		nrOfRemainingNodes -= batchSize
	}
	return nrOfBatches
}
