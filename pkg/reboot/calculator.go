package reboot

import (
	"context"
	"sync"
	"time"

	"github.com/go-logr/logr"
	commonlabels "github.com/medik8s/common/pkg/labels"
	"github.com/pkg/errors"

	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/selection"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/medik8s/self-node-remediation/api/v1alpha1"
	"github.com/medik8s/self-node-remediation/pkg/utils"
)

const (
	MaxTimeForNoPeersResponse = 30 * time.Second
)

type Calculator interface {
	// GetRebootDuration returns the safe time to assume node was already rebooted.
	// Can be either the specified SafeTimeToAssumeNodeRebootedSeconds or the calculated minimum reboot duration.
	// Note that this time must include the time for an unhealthy node without api-server access to reach the conclusion that it's unhealthy.
	// This should be at least worst-case time to reach a conclusion from the other peers * request context timeout + watchdog interval + maxFailuresThreshold * reconcileInterval + padding
	GetRebootDuration(ctx context.Context, node *v1.Node) (time.Duration, error)
	// SetConfig sets the SelfNodeRemediationConfig to be used for calculating the minimum reboot duration
	SetConfig(config *v1alpha1.SelfNodeRemediationConfig)
}

var _ Calculator = &calculator{}

type calculator struct {
	k8sClient client.Client
	log       logr.Logger
	// storing the config here when reconciling it increases resilience in case of issues during remediation
	snrConfig     *v1alpha1.SelfNodeRemediationConfig
	snrConfigLock sync.RWMutex
}

func NewCalculator(k8sClient client.Client, log logr.Logger) Calculator {
	return &calculator{
		k8sClient: k8sClient,
		log:       log,
	}
}

func (r *calculator) SetConfig(config *v1alpha1.SelfNodeRemediationConfig) {
	r.snrConfigLock.Lock()
	defer r.snrConfigLock.Unlock()
	r.snrConfig = config
}

// TODO add unit test!
func (r *calculator) GetRebootDuration(ctx context.Context, node *v1.Node) (time.Duration, error) {

	r.snrConfigLock.Lock()
	defer r.snrConfigLock.Unlock()
	if r.snrConfig == nil {
		return 0, errors.New("SelfNodeRemediationConfig not set yet, can't calculate minimum reboot duration")
	}

	watchdogTimeout, err := utils.GetWatchdogTimeout(node)
	if err != nil {
		// 60s is the maximum default watchdog timeout according to https://docs.kernel.org/watchdog/watchdog-parameters.html
		defaultWatchdogTimeout := 60 * time.Second
		r.log.Error(err, "failed to get watchdog timeout from node annotations, will use the default timeout", "node", node.Name, "default timeout in seconds", defaultWatchdogTimeout.Seconds())
		watchdogTimeout = defaultWatchdogTimeout
	}
	minimumCalculatedRebootDuration, err := r.calculateMinimumRebootDuration(ctx, watchdogTimeout)
	if err != nil {
		return 0, errors.Wrap(err, "failed to calculate minimum reboot duration")
	}

	specRebootDurationSeconds := r.snrConfig.Spec.SafeTimeToAssumeNodeRebootedSeconds
	if specRebootDurationSeconds == nil {
		r.log.Info("No SafeTimeToAssumeNodeRebootedSeconds specified, using calculated minimum safe reboot time",
			"calculated minimum time in seconds", minimumCalculatedRebootDuration)
		return minimumCalculatedRebootDuration, nil
	}

	specRebootDuration := time.Duration(*specRebootDurationSeconds) * time.Second
	// In case users specified a lower reboot time, ignore it
	if specRebootDuration < minimumCalculatedRebootDuration {
		r.log.V(0).Info("Warning: Ignoring specified SafeTimeToAssumeNodeRebootedSeconds because it's lower than the calculated minimum safe reboot time",
			"specified time in seconds", specRebootDuration.Seconds(), "calculated minimum time in seconds", minimumCalculatedRebootDuration.Seconds())
		// TODO event
		return minimumCalculatedRebootDuration, nil
	}
	r.log.Info("Using specified SafeTimeToAssumeNodeRebootedSeconds because it's greater than the calculated minimum safe reboot time",
		"specified time in seconds", specRebootDuration.Seconds(), "calculated minimum time in seconds", minimumCalculatedRebootDuration.Seconds())
	return specRebootDuration, nil
}

func (r *calculator) calculateMinimumRebootDuration(ctx context.Context, watchdogTimeout time.Duration) (time.Duration, error) {

	spec := r.snrConfig.Spec

	// The minimum reboot duration consists of the duration
	// 1) to detect API connectivity issue
	// 2) to confirm issue with peers
	// 3) to trigger the reboot

	// 1. detect API connectivity issue
	// a) max API check duration ...
	apiCheckDuration := spec.ApiCheckInterval.Duration + spec.ApiServerTimeout.Duration
	// b) ... times error threshold ...
	apiCheckDuration *= time.Duration(spec.MaxApiErrorThreshold)

	// 2. confirm issue with peers
	// a) nr of peer request batches (10% batches + 1st smaller batch) ...
	numBatches, err := r.calcNumOfBatches(r.k8sClient, ctx)
	if err != nil {
		return 0, errors.Wrap(err, "failed to calculate number of batches")
	}
	peerRequestsDuration := time.Duration(numBatches)
	// b) ... times max peer request duration
	peerRequestsDuration *= spec.PeerDialTimeout.Duration + spec.PeerRequestTimeout.Duration
	// c) in order to prevent false positives in case of temporary network issues,
	//    we don't consider nodes being unhealthy before MaxTimeForNoPeersResponse.
	//    So that's the minimum time we need for the peers check.
	if peerRequestsDuration < MaxTimeForNoPeersResponse {
		peerRequestsDuration = MaxTimeForNoPeersResponse
	}

	// 3. trigger the reboot
	// a) watchdog timeout ...
	rebootDuration := watchdogTimeout
	// b) ... plus some buffer for actually rebooting
	rebootDuration += 30 * time.Second

	return apiCheckDuration + peerRequestsDuration + rebootDuration, nil
}

func (r *calculator) calcNumOfBatches(k8sClient client.Client, ctx context.Context) (int, error) {

	// get all worker nodes
	reqPeers, _ := labels.NewRequirement(commonlabels.WorkerRole, selection.Exists, []string{})
	selector := labels.NewSelector()
	selector = selector.Add(*reqPeers)

	nodes := &v1.NodeList{}
	if err := k8sClient.List(ctx, nodes, client.MatchingLabelsSelector{Selector: selector}); err != nil {
		return 0, errors.Wrap(err, "failed to list worker nodes")
	}

	workerNodesCount := len(nodes.Items)
	batchCount := utils.GetNrOfBatches(workerNodesCount)
	return batchCount, nil
}
