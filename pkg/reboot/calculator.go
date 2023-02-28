package reboot

import (
	"context"
	"time"

	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/selection"

	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/go-logr/logr"

	"github.com/medik8s/self-node-remediation/pkg/utils"
	"github.com/medik8s/self-node-remediation/pkg/watchdog"
)

const (
	MaxTimeForNoPeersResponse = 30 * time.Second
)

type SafeTimeCalculator interface {
	GetTimeToAssumeNodeRebooted() time.Duration
	Start(ctx context.Context) error
}

type safeTimeCalculator struct {
	timeToAssumeNodeRebooted, minTimeToAssumeNodeRebooted                   time.Duration
	wd                                                                      watchdog.Watchdog
	maxErrorThreshold                                                       int
	apiCheckInterval, apiServerTimeout, peerDialTimeout, peerRequestTimeout time.Duration
	log                                                                     logr.Logger
	k8sClient                    client.Client
	highestCalculatedBatchNumber int
}

func NewSafeTimeCalculator(k8sClient client.Client, wd watchdog.Watchdog, maxErrorThreshold int, apiCheckInterval, apiServerTimeout, peerDialTimeout, peerRequestTimeout, timeToAssumeNodeRebooted time.Duration) SafeTimeCalculator {
	return &safeTimeCalculator{
		wd:                       wd,
		maxErrorThreshold:        maxErrorThreshold,
		apiCheckInterval:         apiCheckInterval,
		apiServerTimeout:         apiServerTimeout,
		peerDialTimeout:          peerDialTimeout,
		peerRequestTimeout:       peerRequestTimeout,
		timeToAssumeNodeRebooted: timeToAssumeNodeRebooted,
		k8sClient:                k8sClient,
		log:                      ctrl.Log.WithName("safe-time-calculator"),
	}
}

func (s *safeTimeCalculator) GetTimeToAssumeNodeRebooted() time.Duration {
	s.calcMinTimeAssumeRebooted()
	if s.timeToAssumeNodeRebooted < s.minTimeToAssumeNodeRebooted {
		return s.minTimeToAssumeNodeRebooted
	}
	return s.timeToAssumeNodeRebooted
}

func (s *safeTimeCalculator) Start(_ context.Context) error {
	s.calcMinTimeAssumeRebooted()
	return nil
}

func (s *safeTimeCalculator) calcMinTimeAssumeRebooted() {
	// The reboot time needs be at least the time we know we need for determining a node issue and trigger the reboot!
	// 1. time for determine node issue
	minTime := (s.apiCheckInterval+s.apiServerTimeout)*time.Duration(s.maxErrorThreshold) + MaxTimeForNoPeersResponse
	// 2. time for asking peers (10% batches + 1st smaller batch)
	minTime += time.Duration(s.calcNumOfBatches()) * (s.peerDialTimeout + s.peerRequestTimeout)
	// 3. watchdog timeout
	if s.wd != nil {
		minTime += s.wd.GetTimeout()
	}
	// 4. some buffer
	minTime += 15 * time.Second
	s.log.Info("calculated minTimeToAssumeNodeRebooted is:", "minTimeToAssumeNodeRebooted", minTime)
	s.minTimeToAssumeNodeRebooted = minTime
}

func (s *safeTimeCalculator) calcNumOfBatches() int {

	reqPeers, _ := labels.NewRequirement(utils.WorkerLabelName, selection.Exists, []string{})
	selector := labels.NewSelector()
	selector = selector.Add(*reqPeers)

	nodes := &v1.NodeList{}
	// time for asking peers (10% batches + 1st smaller batch)
	maxNumberOfBatches := 10 + 1
	if err := s.k8sClient.List(context.Background(), nodes, client.MatchingLabelsSelector{Selector: selector}); err != nil {
		s.log.Error(err, "couldn't fetch worker nodes")
		return maxNumberOfBatches
	}
	workerNodesCount := len(nodes.Items)

	var numberOfBatches int
	switch {
	// 3 workers of first batch + 10% for each batch
	case workerNodesCount >= 13:
		numberOfBatches = maxNumberOfBatches
	// first 3 workers use one batch
	case workerNodesCount <= 3:
		numberOfBatches = 1
	//assuming worst case of one batch for the first 3 worker nodes and another batch for each other node
	default:
		numberOfBatches = workerNodesCount - 2

	}
	//In order to stay on the safe side taking the largest calculated batch number (capped at 11)
	if s.highestCalculatedBatchNumber < numberOfBatches {
		s.highestCalculatedBatchNumber = numberOfBatches
	}
	return s.highestCalculatedBatchNumber

}
