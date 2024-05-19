package reboot

import (
	"context"
	"errors"
	"reflect"
	"time"

	"github.com/go-logr/logr"
	"github.com/medik8s/common/pkg/events"
	commonlabels "github.com/medik8s/common/pkg/labels"

	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/selection"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/medik8s/self-node-remediation/api/v1alpha1"
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
	GetTimeToAssumeNodeRebooted() (time.Duration, error)
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
	recorder                                                                record.EventRecorder
	highestCalculatedBatchNumber                                            int
	isAgent                                                                 bool
}

func NewAgentSafeTimeCalculator(k8sClient client.Client, recorder record.EventRecorder, wd watchdog.Watchdog, maxErrorThreshold int, apiCheckInterval, apiServerTimeout, peerDialTimeout, peerRequestTimeout, timeToAssumeNodeRebooted time.Duration) SafeTimeCalculator {
	return &safeTimeCalculator{
		wd:                       wd,
		maxErrorThreshold:        maxErrorThreshold,
		apiCheckInterval:         apiCheckInterval,
		apiServerTimeout:         apiServerTimeout,
		peerDialTimeout:          peerDialTimeout,
		peerRequestTimeout:       peerRequestTimeout,
		timeToAssumeNodeRebooted: timeToAssumeNodeRebooted,
		k8sClient:                k8sClient,
		recorder:                 recorder,
		isAgent:                  true,
		log:                      ctrl.Log.WithName("safe-time-calculator"),
	}
}

func NewManagerSafeTimeCalculator(k8sClient client.Client) SafeTimeCalculator {
	return &safeTimeCalculator{
		k8sClient: k8sClient,
		isAgent:   false,
		log:       ctrl.Log.WithName("safe-time-calculator"),
	}
}

func (s *safeTimeCalculator) GetTimeToAssumeNodeRebooted() (time.Duration, error) {
	minTime := s.minTimeToAssumeNodeRebooted
	if !s.isAgent {
		config, err := s.getConfig()
		if err != nil {
			return 0, err
		}
		if minTime, err = s.getMinSafeTimeSecFromConfig(config); err != nil {
			return 0, err
		}
		s.timeToAssumeNodeRebooted = time.Duration(config.Spec.SafeTimeToAssumeNodeRebootedSeconds) * time.Second
	}

	if s.timeToAssumeNodeRebooted < minTime {
		return minTime, nil
	}
	return s.timeToAssumeNodeRebooted, nil
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

	//update related logic of min time on configuration if necessary
	if err := s.manageSafeRebootTimeInConfiguration(minTime); err != nil {
		return err
	}

	return nil
}

// manageSafeRebootTimeInConfiguration does two things:
//  1. It sets Status.MinSafeTimeToAssumeNodeRebootedSeconds in case it's changed by latest calculation.
//  2. It Adds/removes config.Status.SpecSafeTimeOverriddenWarning if necessary.
func (s *safeTimeCalculator) manageSafeRebootTimeInConfiguration(minTime time.Duration) error {
	minTimeSec := int(minTime.Seconds())
	config, err := s.getConfig()
	if err != nil {
		return err
	}
	orgConfig := config.DeepCopy()
	prevMinRebootTimeSec := config.Status.MinSafeTimeToAssumeNodeRebootedSeconds
	if prevMinRebootTimeSec != minTimeSec {
		config.Status.MinSafeTimeToAssumeNodeRebootedSeconds = minTimeSec
	}
	//Manage warning
	config.Status.SpecSafeTimeOverriddenWarning = ""
	if config.Spec.SafeTimeToAssumeNodeRebootedSeconds > 0 && minTimeSec > config.Spec.SafeTimeToAssumeNodeRebootedSeconds {
		config.Status.SpecSafeTimeOverriddenWarning = v1alpha1.SafeTimeToAssumeNodeRebootedSecondsWarning
		s.log.Info("SafeTimeToAssumeNodeRebootedSeconds is overridden by calculated value because it's invalid, calculated value stored in Status.MinSafeTimeToAssumeNodeRebootedSeconds would be used instead")
		events.WarningEvent(s.recorder, config, "SafeTimeToAssumeNodeRebootedSecondsInvalid", "SafeTimeToAssumeNodeRebootedSeconds is overridden by calculated value")
	}

	if !reflect.DeepEqual(config, orgConfig) {
		if err := s.k8sClient.Status().Patch(context.Background(), config, client.MergeFrom(orgConfig)); err != nil {
			return err
		}
	}

	return nil
}

func (s *safeTimeCalculator) getMinSafeTimeSecFromConfig(config *v1alpha1.SelfNodeRemediationConfig) (time.Duration, error) {
	minTimeSec := config.Status.MinSafeTimeToAssumeNodeRebootedSeconds
	if minTimeSec > 0 {
		return time.Duration(minTimeSec) * time.Second, nil
	}

	err := errors.New("failed getting MinSafeTimeToAssumeNodeRebootedSeconds, value isn't initialized")
	s.log.Error(err, "MinSafeTimeToAssumeNodeRebootedSeconds should not be empty")
	return 0, err
}

func (s *safeTimeCalculator) getConfig() (*v1alpha1.SelfNodeRemediationConfig, error) {
	confList := &v1alpha1.SelfNodeRemediationConfigList{}
	if err := s.k8sClient.List(context.Background(), confList); err != nil {
		s.log.Error(err, "failed to get snr configuration")
		return nil, err
	}

	var config v1alpha1.SelfNodeRemediationConfig
	if len(confList.Items) < 1 {
		err := errors.New("SNR config not found")
		s.log.Error(err, "failed to get snr configuration")
		return nil, err
	} else {
		config = confList.Items[0]
	}
	return &config, nil
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
