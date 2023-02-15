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
	timeToAssumeNodeRebooted                                                time.Duration
	wd                                                                      watchdog.Watchdog
	maxErrorThreshold                                                       int
	apiCheckInterval, apiServerTimeout, peerDialTimeout, peerRequestTimeout time.Duration
	log                                                                     logr.Logger
	k8sClient                                                               client.Client
}

func New(k8sClient client.Client, wd watchdog.Watchdog, maxErrorThreshold int, apiCheckInterval, apiServerTimeout, peerDialTimeout, peerRequestTimeout, timeToAssumeNodeRebooted time.Duration) SafeTimeCalculator {
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
	return s.timeToAssumeNodeRebooted
}

//goland:noinspection GoUnusedParameter
func (s *safeTimeCalculator) Start(ctx context.Context) error {
	s.calcTimeAssumeRebooted()
	return nil
}

func (s *safeTimeCalculator) calcTimeAssumeRebooted() {
	// but the reboot time needs be at least the time we know we need for determining a node issue and trigger the reboot!
	// 1. time for determine node issue
	minTimeToAssumeNodeRebooted := (s.apiCheckInterval+s.apiServerTimeout)*time.Duration(s.maxErrorThreshold) + MaxTimeForNoPeersResponse
	// 2. time for asking peers (10% batches + 1st smaller batch)
	minTimeToAssumeNodeRebooted += s.calcNumOfBatches() * (s.peerDialTimeout + s.peerRequestTimeout)
	// 3. watchdog timeout
	if s.wd != nil {
		minTimeToAssumeNodeRebooted += s.wd.GetTimeout()
	}
	// 4. some buffer
	minTimeToAssumeNodeRebooted += 15 * time.Second
	s.log.Info("calculated minTimeToAssumeNodeRebooted is:", "minTimeToAssumeNodeRebooted", minTimeToAssumeNodeRebooted)
	if s.timeToAssumeNodeRebooted < minTimeToAssumeNodeRebooted {
		s.timeToAssumeNodeRebooted = minTimeToAssumeNodeRebooted
	}
}

func (s *safeTimeCalculator) calcNumOfBatches() time.Duration {

	reqPeers, _ := labels.NewRequirement(utils.WorkerLabelName, selection.Exists, []string{})
	selector := labels.NewSelector()
	selector = selector.Add(*reqPeers)

	nodes := &v1.NodeList{}
	// time for asking peers (10% batches + 1st smaller batch)
	numberOfBatches := 10
	if err := s.k8sClient.List(context.Background(), nodes, client.MatchingLabelsSelector{Selector: selector}); err != nil {
		s.log.Error(err, "couldn't fetch worker nodes")
		return time.Duration(numberOfBatches)
	}

	//in case there are less than 10 worker peers we can cover all of them with less than batches
	if workerNodesCount := len(nodes.Items); workerNodesCount > 0 && workerNodesCount < numberOfBatches {
		numberOfBatches = workerNodesCount
	}

	numberOfBatches = numberOfBatches + 1
	return time.Duration(numberOfBatches)
}
