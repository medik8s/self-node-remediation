package utils

const (
	MinNrOfNodesInBatch      = 3
	MaxNrOfBatchesAfterFirst = 10
	// MaxNrOfBatches is the largest number of batches GetNrOfBatches returns for any number of nodes. The batches after
	// the first one hold a tenth of the nodes, but never fewer than MinNrOfNodesInBatch, so the count is not monotonic
	// in the number of nodes: 42 nodes take 14 batches while 43 take 11. Callers that need an upper bound, such as the
	// reboot duration calculator, use MaxNrOfBatchesUpTo, or this constant when the number of nodes is unknown.
	MaxNrOfBatches = 14
)

// MaxNrOfBatchesUpTo returns the largest number of batches GetNrOfBatches returns for any number of nodes up to
// nrOfNodes. Unlike GetNrOfBatches it never decreases when nodes are added, so a bound computed for the current
// cluster size still holds when a few nodes join during a peer sweep, and it never exceeds MaxNrOfBatches.
func MaxNrOfBatchesUpTo(nrOfNodes int) int {
	maxNrOfBatches := 0
	for n := 1; n <= nrOfNodes; n++ {
		if nrOfBatches := GetNrOfBatches(n); nrOfBatches > maxNrOfBatches {
			maxNrOfBatches = nrOfBatches
		}
		if maxNrOfBatches >= MaxNrOfBatches {
			return MaxNrOfBatches
		}
	}
	return maxNrOfBatches
}

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
