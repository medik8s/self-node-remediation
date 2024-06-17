package reboot

import (
	"context"
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
	MinNodesNumberInBatch     = 3
	MaxBatchesAfterFirst      = 10
)

type Calculator interface {
	// GetRebootDuration returns the safe time to assume node was already rebooted.
	// Can be either the specified SafeTimeToAssumeNodeRebootedSeconds or the calculated minimum reboot duration.
	// Note that this time must include the time for a unhealthy node without api-server access to reach the conclusion that it's unhealthy.
	// This should be at least worst-case time to reach a conclusion from the other peers * request context timeout + watchdog interval + maxFailuresThreshold * reconcileInterval + padding
	GetRebootDuration(k8sClient client.Client, ctx context.Context, node *v1.Node) (time.Duration, error)
	// SetConfig sets the SelfNodeRemediationConfig to be used for calculating the minimum reboot duration
	SetConfig(config *v1alpha1.SelfNodeRemediationConfig)
}

var _ Calculator = &calculator{}

type calculator struct {
	k8sClient client.Client
	log       logr.Logger
	// storing the config here when reconciling it increases resilience in case of issues during remediation
	snrConfig *v1alpha1.SelfNodeRemediationConfig
}

func NewCalculator(k8sClient client.Client, log logr.Logger) Calculator {
	return &calculator{
		k8sClient: k8sClient,
		log:       log,
	}
}

func (r *calculator) SetConfig(config *v1alpha1.SelfNodeRemediationConfig) {
	r.snrConfig = config
}

// TODO add unit test!
func (r *calculator) GetRebootDuration(k8sClient client.Client, ctx context.Context, node *v1.Node) (time.Duration, error) {

	if r.snrConfig == nil {
		return 0, errors.New("SelfNodeRemediationConfig not set yet, can't calculate minimum reboot duration")
	}

	watchdogTimeout := utils.GetWatchdogTimeout(node)
	minimumCalculatedRebootDuration, err := r.calculateMinimumRebootDuration(k8sClient, ctx, watchdogTimeout)
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

func (r *calculator) calculateMinimumRebootDuration(k8sClient client.Client, ctx context.Context, watchdogTimeout time.Duration) (time.Duration, error) {

	spec := r.snrConfig.Spec

	// The minimum reboot duration consists of the duration to identify a node issue, and to trigger the reboot

	// 1. time for determine node issue
	// a) API check duration ...
	minTime := spec.ApiCheckInterval.Duration + spec.ApiServerTimeout.Duration
	// b) ... times error threshold ...
	minTime *= time.Duration(spec.MaxApiErrorThreshold)
	// c) ... plus peer timeout
	minTime += MaxTimeForNoPeersResponse

	// 2. plus time for asking peers (10% batches + 1st smaller batch)
	numBatches, err := r.calcNumOfBatches(k8sClient, ctx)
	if err != nil {
		return 0, errors.Wrap(err, "failed to calculate number of batches")
	}
	minTime += time.Duration(numBatches)*spec.PeerDialTimeout.Duration + spec.PeerRequestTimeout.Duration

	// 3. plus watchdog timeout
	minTime += watchdogTimeout

	// 4. plus some buffer
	minTime += 15 * time.Second

	return minTime, nil
}

func (r *calculator) calcNumOfBatches(k8sClient client.Client, ctx context.Context) (int, error) {

	reqPeers, _ := labels.NewRequirement(commonlabels.WorkerRole, selection.Exists, []string{})
	selector := labels.NewSelector()
	selector = selector.Add(*reqPeers)

	nodes := &v1.NodeList{}
	// time for asking peers (10% batches + 1st smaller batch)
	maxNumberOfBatches := MaxBatchesAfterFirst + 1
	if err := k8sClient.List(ctx, nodes, client.MatchingLabelsSelector{Selector: selector}); err != nil {
		return maxNumberOfBatches, errors.Wrap(err, "failed to list worker nodes")
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
	return numberOfBatches, nil
}

func (r *calculator) getConfig(k8sClient client.Client, ctx context.Context, namespace string) (*v1alpha1.SelfNodeRemediationConfig, error) {
	config := &v1alpha1.SelfNodeRemediationConfig{}
	err := k8sClient.Get(ctx, client.ObjectKey{Namespace: namespace, Name: v1alpha1.ConfigCRName}, config)
	return config, err
}
