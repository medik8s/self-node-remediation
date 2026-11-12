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

	"github.com/medik8s/self-node-remediation/v5/api/v1alpha1"
	"github.com/medik8s/self-node-remediation/v5/internal/utils"
	snrwebhook "github.com/medik8s/self-node-remediation/v5/internal/webhook/v1alpha1"
)

const (
	// MaxTimeForNoPeersResponse is the default for
	// SelfNodeRemediationConfig.Spec.MaxTimeForNoPeersResponse, used when the
	// field is unset.
	MaxTimeForNoPeersResponse = 30 * time.Second
	// minWatchdogTimeout is the smallest watchdog timeout the reboot duration relies on. 60s is the maximum default
	// watchdog timeout according to https://docs.kernel.org/watchdog/watchdog-parameters.html, so it is used when the
	// node annotation is missing, and also when it holds a smaller value, such as the 0 an agent writes when it finds
	// no watchdog device and falls back to a software reboot.
	minWatchdogTimeout = 60 * time.Second
	// rebootBuffer is the time allowed for the node to actually go down once the reboot is triggered
	rebootBuffer = 30 * time.Second
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

// GetRebootDuration returns the time after which the given node can be assumed to have rebooted: the configured
// SafeTimeToAssumeNodeRebootedSeconds when it is at least the calculated minimum, otherwise that minimum. The
// watchdog timeout comes from the node's annotation, floored at minWatchdogTimeout.
func (r *calculator) GetRebootDuration(ctx context.Context, node *v1.Node) (time.Duration, error) {

	r.snrConfigLock.RLock()
	defer r.snrConfigLock.RUnlock()
	if r.snrConfig == nil {
		return 0, errors.New("SelfNodeRemediationConfig not set yet, can't calculate minimum reboot duration")
	}

	watchdogTimeout, err := utils.GetWatchdogTimeout(node)
	if err != nil {
		r.log.Error(err, "failed to get watchdog timeout from node annotations, will use the default timeout", "node", node.Name, "default timeout in seconds", minWatchdogTimeout.Seconds())
		watchdogTimeout = minWatchdogTimeout
	} else if watchdogTimeout < minWatchdogTimeout {
		r.log.Info("watchdog timeout from node annotations is below the minimum the reboot duration relies on, using the minimum", "node", node.Name, "annotated timeout in seconds", watchdogTimeout.Seconds(), "minimum timeout in seconds", minWatchdogTimeout.Seconds())
		watchdogTimeout = minWatchdogTimeout
	}
	minimumCalculatedRebootDuration := r.calculateMinimumRebootDuration(watchdogTimeout, r.nrOfBatches(ctx))

	specRebootDurationSeconds := r.snrConfig.Spec.SafeTimeToAssumeNodeRebootedSeconds
	if specRebootDurationSeconds == nil {
		r.log.Info("No SafeTimeToAssumeNodeRebootedSeconds specified, using calculated minimum safe reboot time",
			"calculated minimum time in seconds", minimumCalculatedRebootDuration.Seconds())
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

// calculateMinimumRebootDuration returns the worst-case time an unhealthy node without API server access needs to
// conclude that it is unhealthy and to reboot, counted from the moment it lost the API server. Its agent:
//
//  1. detects the API connectivity issue: MaxApiErrorThreshold checks fail, each taking up to
//     ApiCheckInterval + ApiServerTimeout;
//  2. asks its peers, in at most nrOfBatches batches of PeerDialTimeout + PeerRequestTimeout each. A peer answering
//     "api error" at the very end of that sweep restarts the no-peers-response window;
//  3. waits MaxTimeForNoPeersResponse without any peer answer. The sweep that ends after the window elapsed may have
//     started one check cycle before it elapsed, so one more check and one more full sweep follow;
//  4. triggers the reboot: the watchdog fires after its timeout, then the node needs some time to actually go down.
func (r *calculator) calculateMinimumRebootDuration(watchdogTimeout time.Duration, nrOfBatches int) time.Duration {

	spec := r.snrConfig.Spec

	// 1. detect API connectivity issue
	// a) max API check duration ...
	checkDuration := spec.ApiCheckInterval.Duration + spec.ApiServerTimeout.Duration
	// b) ... times error threshold
	apiCheckDuration := checkDuration * time.Duration(spec.MaxApiErrorThreshold)

	// 2. confirm issue with peers
	// a) nr of peer request batches times max peer request duration, the agent raises too low request timeouts
	sweepDuration := time.Duration(nrOfBatches) * (spec.PeerDialTimeout.Duration + effectivePeerRequestTimeout(spec))
	// b) in order to prevent false positives in case of temporary network issues, nodes are not considered unhealthy
	//    before MaxTimeForNoPeersResponse elapsed since the last peer answer, which can come at the end of the sweep
	maxTimeForNoPeersResponse := MaxTimeForNoPeersResponse
	if spec.MaxTimeForNoPeersResponse != nil {
		maxTimeForNoPeersResponse = spec.MaxTimeForNoPeersResponse.Duration
	}
	// c) the sweep noticing that the window elapsed can start one check cycle after the one that ended just before
	peerRequestsDuration := sweepDuration + maxTimeForNoPeersResponse + checkDuration + sweepDuration

	// 3. trigger the reboot
	// a) watchdog timeout ...
	rebootDuration := watchdogTimeout
	// b) ... plus some buffer for actually rebooting
	rebootDuration += rebootBuffer

	return apiCheckDuration + peerRequestsDuration + rebootDuration
}

// nrOfBatches returns the number of peer request batches the reboot duration must cover: the largest batch count of any
// cluster up to the current number of worker nodes. GetNrOfBatches is not monotonic in the number of nodes (42 nodes
// take 14 batches, 43 take 11) and nodes can join during a sweep, so the count for the exact current size would be
// too small; the bound over every size up to it is not. When the nodes cannot be listed, the bound over all cluster
// sizes is used, so that a remediation is never blocked by a failed List.
func (r *calculator) nrOfBatches(ctx context.Context) int {
	reqPeers, _ := labels.NewRequirement(commonlabels.WorkerRole, selection.Exists, []string{})
	selector := labels.NewSelector().Add(*reqPeers)

	nodes := &v1.NodeList{}
	if err := r.k8sClient.List(ctx, nodes, client.MatchingLabelsSelector{Selector: selector}); err != nil {
		r.log.Error(err, "failed to list worker nodes, using the largest possible number of peer batches", "batches", utils.MaxNrOfBatches)
		return utils.MaxNrOfBatches
	}
	return utils.MaxNrOfBatchesUpTo(len(nodes.Items))
}

// effectivePeerRequestTimeout mirrors the agent: PeerRequestTimeout is raised to ApiServerTimeout plus a buffer when
// it is lower, so that a peer has time to query the API server before the request to it times out.
func effectivePeerRequestTimeout(spec v1alpha1.SelfNodeRemediationConfigSpec) time.Duration {
	minimumSafeTimeout := spec.ApiServerTimeout.Duration + snrwebhook.MinimumBuffer
	if spec.PeerRequestTimeout.Duration < minimumSafeTimeout {
		return minimumSafeTimeout
	}
	return spec.PeerRequestTimeout.Duration
}
