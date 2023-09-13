package reboot

import (
	"context"
	"fmt"
	"time"

	"github.com/go-logr/logr"
	commonlabels "github.com/medik8s/common/pkg/labels"

	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/selection"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/medik8s/self-node-remediation/pkg/watchdog"
)

const (
	MaxTimeForNoPeersResponse = 30 * time.Second
	MinNodesNumberInBatch     = 3
	MaxBatchesAfterFirst      = 10
)

type SafeTimeCalculator interface {
	// GetTimeToAssumeNodeRebooted returns the safe time to assume node was already rebooted
	// note that this time must include the time for a unhealthy node without api-server access to reach the conclusion that it's unhealthy
	// this should be at least worst-case time to reach a conclusion from the other peers * request context timeout + watchdog interval + maxFailuresThreshold * reconcileInterval + padding
	GetTimeToAssumeNodeRebooted() time.Duration
	SetTimeToAssumeNodeRebooted(time.Duration)
	Start(ctx context.Context) error
	//IsAgent return true in case running on an agent pod (responsible for reboot) or false in case running on a manager pod
	IsAgent() bool
}

type safeTimeCalculator struct {
	timeToAssumeNodeRebooted, minTimeToAssumeNodeRebooted                   time.Duration
	wd                                                                      watchdog.Watchdog
	maxErrorThreshold                                                       int
	apiCheckInterval, apiServerTimeout, peerDialTimeout, peerRequestTimeout time.Duration
	log                                                                     logr.Logger
	k8sClient                                                               client.Client
	highestCalculatedBatchNumber                                            int
	isAgent                                                                 bool
}

func NewAgentSafeTimeCalculator(k8sClient client.Client, wd watchdog.Watchdog, maxErrorThreshold int, apiCheckInterval, apiServerTimeout, peerDialTimeout, peerRequestTimeout, timeToAssumeNodeRebooted time.Duration) SafeTimeCalculator {
	return &safeTimeCalculator{
		wd:                       wd,
		maxErrorThreshold:        maxErrorThreshold,
		apiCheckInterval:         apiCheckInterval,
		apiServerTimeout:         apiServerTimeout,
		peerDialTimeout:          peerDialTimeout,
		peerRequestTimeout:       peerRequestTimeout,
		timeToAssumeNodeRebooted: timeToAssumeNodeRebooted,
		k8sClient:                k8sClient,
		isAgent:                  true,
		log:                      ctrl.Log.WithName("safe-time-calculator"),
	}
}

func NewManagerSafeTimeCalculator(k8sClient client.Client, timeToAssumeNodeRebooted time.Duration) SafeTimeCalculator {
	return &safeTimeCalculator{
		timeToAssumeNodeRebooted: timeToAssumeNodeRebooted,
		k8sClient:                k8sClient,
		isAgent:                  false,
		log:                      ctrl.Log.WithName("safe-time-calculator"),
	}
}

func (s *safeTimeCalculator) GetTimeToAssumeNodeRebooted() time.Duration {
	if !s.isAgent {
		return s.timeToAssumeNodeRebooted
	}

	if s.timeToAssumeNodeRebooted < s.minTimeToAssumeNodeRebooted {
		return s.minTimeToAssumeNodeRebooted
	}
	return s.timeToAssumeNodeRebooted
}

func (s *safeTimeCalculator) SetTimeToAssumeNodeRebooted(timeToAssumeNodeRebooted time.Duration) {
	s.timeToAssumeNodeRebooted = timeToAssumeNodeRebooted
}

func (s *safeTimeCalculator) Start(_ context.Context) error {
	return s.calcMinTimeAssumeRebooted()
}

func (s *safeTimeCalculator) calcMinTimeAssumeRebooted() error {
	if !s.isAgent {
		return nil
	}
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

	if s.timeToAssumeNodeRebooted < minTime {
		err := fmt.Errorf("snr agent can't start requested value for timeToAssumeNodeRebooted is too low")
		s.log.Error(err, err.Error(), "requested timeToAssumeNodeRebooted", s.timeToAssumeNodeRebooted, "timeToAssumeNodeRebooted minimal valid value", minTime)
		return err
	}
	return nil
}

func (s *safeTimeCalculator) IsAgent() bool {
	return s.isAgent
}

func (s *safeTimeCalculator) calcNumOfBatches() int {

	reqPeers, _ := labels.NewRequirement(commonlabels.WorkerRole, selection.Exists, []string{})
	selector := labels.NewSelector()
	selector = selector.Add(*reqPeers)

	nodes := &v1.NodeList{}
	// time for asking peers (10% batches + 1st smaller batch)
	maxNumberOfBatches := MaxBatchesAfterFirst + 1
	if err := s.k8sClient.List(context.Background(), nodes, client.MatchingLabelsSelector{Selector: selector}); err != nil {
		s.log.Error(err, "couldn't fetch worker nodes")
		return maxNumberOfBatches
	}
	workerNodesCount := len(nodes.Items)

	var numberOfBatches int
	switch {
	//high number of workers: we need max batches (for example 53 nodes will be done in 11 batches -> 1 * 3 + 10 * 5 )
	case workerNodesCount > maxNumberOfBatches*MinNodesNumberInBatch:
		numberOfBatches = maxNumberOfBatches
	//there are few enough nodes to use the min batch (for example 20 nodes will be done in 7 batches -> 1 * 3 +  6 * 3 )
	default:
		numberOfBatches = workerNodesCount / MinNodesNumberInBatch
		if workerNodesCount%MinNodesNumberInBatch != 0 {
			numberOfBatches++
		}
	}
	//In order to stay on the safe side taking the largest calculated batch number (capped at 11)
	if s.highestCalculatedBatchNumber < numberOfBatches {
		s.highestCalculatedBatchNumber = numberOfBatches
	}
	return s.highestCalculatedBatchNumber

}
