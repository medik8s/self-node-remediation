package apicheck

import (
	"context"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/go-logr/logr"
	"github.com/medik8s/common/pkg/events"
	"google.golang.org/grpc/credentials"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apiextensions-apiserver/pkg/client/clientset/clientset"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/client"

	selfNodeRemediation "github.com/medik8s/self-node-remediation/api"
	"github.com/medik8s/self-node-remediation/api/v1alpha1"
	"github.com/medik8s/self-node-remediation/pkg/certificates"
	"github.com/medik8s/self-node-remediation/pkg/controlplane"
	"github.com/medik8s/self-node-remediation/pkg/peerhealth"
	"github.com/medik8s/self-node-remediation/pkg/peers"
	"github.com/medik8s/self-node-remediation/pkg/reboot"
	"github.com/medik8s/self-node-remediation/pkg/utils"
)

const (
	eventReasonPeerTimeoutAdjusted = "PeerTimeoutAdjusted"
)

type PeersOverrideFunc func(role peers.Role) []corev1.PodIP

type ApiConnectivityCheck struct {
	client.Reader
	config                        *ApiConnectivityCheckConfig
	failureTracker                *FailureTracker
	workerLastPeerResponse        time.Time
	controlPlaneLastPeerResponse  time.Time
	workerPeerSilenceSince        time.Time
	controlPlanePeerSilenceSince  time.Time
	clientCreds                   credentials.TransportCredentials
	mutex                         sync.Mutex
	controlPlaneManager           *controlplane.Manager
	getHealthStatusFromRemoteFunc GetHealthStatusFromRemoteFunc
	peerOverride                  PeersOverrideFunc
}

type GetHealthStatusFromRemoteFunc func(endpointIp corev1.PodIP, results chan<- selfNodeRemediation.HealthCheckResponseCode)

type ApiConnectivityCheckConfig struct {
	Log                       logr.Logger
	MyNodeName                string
	MyMachineName             string
	CheckInterval             time.Duration
	MaxErrorsThreshold        int
	Peers                     *peers.Peers
	Rebooter                  reboot.Rebooter
	Cfg                       *rest.Config
	CertReader                certificates.CertStorageReader
	ApiServerTimeout          time.Duration
	PeerDialTimeout           time.Duration
	PeerRequestTimeout        time.Duration
	PeerHealthPort            int
	MaxTimeForNoPeersResponse time.Duration
	MinPeersForRemediation    int
	Recorder                  record.EventRecorder
	FailureWindow             time.Duration
	PeerQuorumTimeout         time.Duration
}

func New(config *ApiConnectivityCheckConfig, controlPlaneManager *controlplane.Manager) (c *ApiConnectivityCheck) {
	c = &ApiConnectivityCheck{
		config:              config,
		mutex:               sync.Mutex{},
		controlPlaneManager: controlPlaneManager,
		failureTracker:      NewFailureTracker(),
	}

	c.SetHealthStatusFunc(c.GetDefaultPeerHealthCheckFunc())

	return
}

func (c *ApiConnectivityCheck) GetDefaultPeerHealthCheckFunc() (fun GetHealthStatusFromRemoteFunc) {

	fun = func(endpointIp corev1.PodIP, results chan<- selfNodeRemediation.HealthCheckResponseCode) {
		logger := c.config.Log.WithValues("IP", endpointIp.IP)
		logger.Info("getting health status from peer")

		if err := c.initClientCreds(); err != nil {
			logger.Error(err, "failed to init client credentials")
			results <- selfNodeRemediation.RequestFailed
			return
		}

		// TODO does this work with IPv6?
		// MES: Yes it does, we've tested this
		phClient, err := peerhealth.NewClient(fmt.Sprintf("%v:%v", endpointIp.IP, c.config.PeerHealthPort), c.config.PeerDialTimeout, c.config.Log.WithName("peerhealth client"), c.clientCreds)
		if err != nil {
			logger.Error(err, "failed to init grpc client")
			results <- selfNodeRemediation.RequestFailed
			return
		}
		defer phClient.Close()

		effectiveTimeout := c.getEffectivePeerRequestTimeout()
		ctx, cancel := context.WithTimeout(context.Background(), effectiveTimeout)
		defer cancel()

		resp, err := phClient.IsHealthy(ctx, &peerhealth.HealthRequest{
			NodeName:    c.config.MyNodeName,
			MachineName: c.config.MyMachineName,
		})
		if err != nil {
			logger.Error(err, "failed to read health response from peer")
			results <- selfNodeRemediation.RequestFailed
			return
		}

		logger.Info("got response from peer", "status", resp.Status)

		results <- selfNodeRemediation.HealthCheckResponseCode(resp.Status)
		return
	}

	return
}

func (c *ApiConnectivityCheck) GetControlPlaneManager() *controlplane.Manager {
	return c.controlPlaneManager
}

func (c *ApiConnectivityCheck) SetControlPlaneManager(manager *controlplane.Manager) {
	c.controlPlaneManager = manager
}

func (c *ApiConnectivityCheck) SetPeersOverride(fn PeersOverrideFunc) {
	c.mutex.Lock()
	defer c.mutex.Unlock()
	c.peerOverride = fn
}

func (c *ApiConnectivityCheck) Start(ctx context.Context) error {

	cs, err := clientset.NewForConfig(c.config.Cfg)
	if err != nil {
		return err
	}
	restClient := cs.RESTClient()

	wait.UntilWithContext(ctx, func(ctx context.Context) {

		readerCtx, cancel := context.WithTimeout(ctx, c.config.ApiServerTimeout)
		defer cancel()

		result := restClient.Verb(http.MethodGet).RequestURI("/readyz?exclude=shutdown").Do(readerCtx)
		failure := ""
		if result.Error() != nil {
			failure = fmt.Sprintf("api server readyz endpoint error: %v", result.Error())
		} else {
			statusCode := 0
			result.StatusCode(&statusCode)
			if statusCode != 200 {
				failure = fmt.Sprintf("api server readyz endpoint status code: %v", statusCode)
			}
		}
		if failure != "" {
			c.config.Log.Info(fmt.Sprintf("failed to check api server: %s", failure))
			if isHealthy := c.isConsideredHealthy(); !isHealthy {
				// we have a problem on this node
				c.config.Log.Error(err, "we are unhealthy, triggering a reboot")
				if err := c.config.Rebooter.Reboot(); err != nil {
					c.config.Log.Error(err, "failed to trigger reboot")
				}
			} else {
				c.config.Log.Info("peers did not confirm that we are unhealthy, ignoring error")
			}
			return
		}

		// reset failure tracker after a successful API call
		if c.failureTracker != nil {
			c.failureTracker.Reset()
		}

	}, c.config.CheckInterval)

	return nil
}

// isConsideredHealthy keeps track of the number of errors reported, and when a certain amount of error occur within a certain
// time, ask peers if this node is healthy. Returns if the node is considered to be healthy or not.  It is usable
// whether this is a control plane node or a worker node
func (c *ApiConnectivityCheck) isConsideredHealthy() bool {
	now := time.Now()
	tracker := c.ensureFailureTracker()
	window := c.effectiveFailureWindow()
	tracker.RecordFailure(now)

	if !tracker.ShouldEscalate(now, window) {
		c.config.Log.V(1).Info("failure threshold not met, remaining healthy",
			"now", now,
			"window", window)
		return true
	}

	if c.isWorkerNode() {
		outcome := c.evaluateWorker(now)
		c.config.Log.Info("worker evaluation complete", "outcome", outcome)
		return c.outcomeIsHealthyForWorker(outcome)
	}

	outcome := c.evaluateControlPlane(now)
	c.config.Log.Info("control-plane evaluation complete", "outcome", outcome)
	if c.controlPlaneManager == nil {
		return c.outcomeIsHealthyForControlPlane(outcome)
	}
	return c.controlPlaneManager.IsControlPlaneHealthy(outcome)
}

type peerSummary struct {
	role       peers.Role
	addresses  []corev1.PodIP
	healthy    int
	unhealthy  int
	apiErrors  int
	failures   int
	responded  bool
	hadMinimum bool
}

func (ps peerSummary) totalPeers() int {
	return len(ps.addresses)
}

func (ps peerSummary) responseCount() int {
	return ps.healthy + ps.unhealthy + ps.apiErrors
}

func (ps peerSummary) majorityApiError() bool {
	total := ps.totalPeers()
	if total == 0 {
		return false
	}
	return ps.apiErrors > total/2
}

func (c *ApiConnectivityCheck) evaluateWorker(now time.Time) controlplane.EvaluationOutcome {
	summary := c.gatherPeerResponses(peers.Worker, now)

	if summary.totalPeers() == 0 {
		if c.config.MinPeersForRemediation == 0 {
			return controlplane.EvaluationIsolation
		}
		if c.peerTimeoutExceeded(peers.Worker, now) {
			return controlplane.EvaluationIsolation
		}
		return controlplane.EvaluationAwaitQuorum
	}

	if summary.healthy > 0 {
		return controlplane.EvaluationHealthy
	}

	if summary.unhealthy > 0 {
		outcome := c.escalateToControlPlanes(now)
		if outcome == controlplane.EvaluationAwaitQuorum && c.peerTimeoutExceeded(peers.ControlPlane, now) {
			return controlplane.EvaluationIsolation
		}
		return outcome
	}

	if summary.majorityApiError() {
		return controlplane.EvaluationGlobalOutage
	}

	if !summary.responded || !summary.hadMinimum {
		if c.peerTimeoutExceeded(peers.Worker, now) {
			return controlplane.EvaluationIsolation
		}
		return controlplane.EvaluationAwaitQuorum
	}

	return controlplane.EvaluationHealthy
}

func (c *ApiConnectivityCheck) evaluateControlPlane(now time.Time) controlplane.EvaluationOutcome {
	workerSummary := c.gatherPeerResponses(peers.Worker, now)

	if workerSummary.majorityApiError() {
		return controlplane.EvaluationGlobalOutage
	}

	if workerSummary.unhealthy > 0 {
		outcome := c.escalateToControlPlanes(now)
		if outcome == controlplane.EvaluationAwaitQuorum && c.peerTimeoutExceeded(peers.ControlPlane, now) {
			return controlplane.EvaluationIsolation
		}
		return outcome
	}

	controlSummary := c.gatherPeerResponses(peers.ControlPlane, now)
	if controlSummary.totalPeers() == 0 {
		return controlplane.EvaluationIsolation
	}

	if controlSummary.unhealthy > 0 {
		return controlplane.EvaluationRemediate
	}

	if controlSummary.majorityApiError() {
		return controlplane.EvaluationGlobalOutage
	}

	if controlSummary.healthy > 0 {
		return controlplane.EvaluationHealthy
	}

	if !controlSummary.responded || !controlSummary.hadMinimum {
		if c.peerTimeoutExceeded(peers.ControlPlane, now) {
			return controlplane.EvaluationIsolation
		}
		return controlplane.EvaluationAwaitQuorum
	}

	if workerSummary.responded && workerSummary.healthy > 0 {
		return controlplane.EvaluationHealthy
	}

	if !workerSummary.responded && c.peerTimeoutExceeded(peers.Worker, now) {
		return controlplane.EvaluationIsolation
	}

	return controlplane.EvaluationHealthy
}

func (c *ApiConnectivityCheck) escalateToControlPlanes(now time.Time) controlplane.EvaluationOutcome {
	summary := c.gatherPeerResponses(peers.ControlPlane, now)

	if summary.totalPeers() == 0 {
		return controlplane.EvaluationIsolation
	}

	if summary.unhealthy > 0 {
		return controlplane.EvaluationRemediate
	}

	if summary.majorityApiError() {
		return controlplane.EvaluationGlobalOutage
	}

	if summary.healthy > 0 {
		return controlplane.EvaluationHealthy
	}

	if !summary.responded || !summary.hadMinimum {
		if c.peerTimeoutExceeded(peers.ControlPlane, now) {
			return controlplane.EvaluationIsolation
		}
		return controlplane.EvaluationAwaitQuorum
	}

	return controlplane.EvaluationHealthy
}

func (c *ApiConnectivityCheck) gatherPeerResponses(role peers.Role, now time.Time) peerSummary {
	addresses := c.listPeers(role)
	summary := peerSummary{
		role:      role,
		addresses: addresses,
	}

	minimum := c.config.MinPeersForRemediation
	if minimum <= 0 {
		summary.hadMinimum = true
	} else {
		summary.hadMinimum = len(addresses) >= minimum
	}

	if len(addresses) == 0 {
		c.recordPeerSilence(role, now)
		return summary
	}

	peersToAsk := make([]corev1.PodIP, len(addresses))
	copy(peersToAsk, addresses)
	allPeers := len(peersToAsk)

	for len(peersToAsk) > 0 {
		batchSize := utils.GetNextBatchSize(allPeers, len(peersToAsk))
		chosen := c.popPeerIPs(&peersToAsk, batchSize)
		healthy, unhealthy, apiErrors, failures := c.getHealthStatusFromPeers(chosen)
		summary.healthy += healthy
		summary.unhealthy += unhealthy
		summary.apiErrors += apiErrors
		summary.failures += failures
		if healthy+unhealthy+apiErrors > 0 {
			summary.responded = true
		}
	}

	if summary.responded {
		c.recordPeerActivity(role, now)
	} else {
		c.recordPeerSilence(role, now)
	}

	return summary
}

func (c *ApiConnectivityCheck) listPeers(role peers.Role) []corev1.PodIP {
	c.mutex.Lock()
	override := c.peerOverride
	c.mutex.Unlock()

	if override != nil {
		custom := override(role)
		if len(custom) == 0 {
			return nil
		}
		copySlice := make([]corev1.PodIP, len(custom))
		copy(copySlice, custom)
		return copySlice
	}

	if c.config == nil || c.config.Peers == nil {
		return nil
	}

	return c.config.Peers.GetPeersAddresses(role)
}

func (c *ApiConnectivityCheck) recordPeerActivity(role peers.Role, now time.Time) {
	if role == peers.Worker {
		c.workerLastPeerResponse = now
		c.workerPeerSilenceSince = time.Time{}
	} else {
		c.controlPlaneLastPeerResponse = now
		c.controlPlanePeerSilenceSince = time.Time{}
	}
}

func (c *ApiConnectivityCheck) recordPeerSilence(role peers.Role, now time.Time) {
	if role == peers.Worker {
		if c.workerPeerSilenceSince.IsZero() {
			c.workerPeerSilenceSince = now
		}
	} else {
		if c.controlPlanePeerSilenceSince.IsZero() {
			c.controlPlanePeerSilenceSince = now
		}
	}
}

func (c *ApiConnectivityCheck) ResetPeerTimers() {
	c.workerLastPeerResponse = time.Time{}
	c.workerPeerSilenceSince = time.Time{}
	c.controlPlaneLastPeerResponse = time.Time{}
	c.controlPlanePeerSilenceSince = time.Time{}
}

func (c *ApiConnectivityCheck) outcomeIsHealthyForWorker(outcome controlplane.EvaluationOutcome) bool {
	switch outcome {
	case controlplane.EvaluationRemediate, controlplane.EvaluationIsolation:
		return false
	default:
		return true
	}
}

func (c *ApiConnectivityCheck) outcomeIsHealthyForControlPlane(outcome controlplane.EvaluationOutcome) bool {
	switch outcome {
	case controlplane.EvaluationRemediate, controlplane.EvaluationIsolation:
		return false
	default:
		return true
	}
}

func (c *ApiConnectivityCheck) isWorkerNode() bool {
	if c.controlPlaneManager == nil {
		return true
	}
	return !c.controlPlaneManager.IsControlPlane()
}

func (c *ApiConnectivityCheck) ensureFailureTracker() *FailureTracker {
	if c.failureTracker == nil {
		c.failureTracker = NewFailureTracker()
	}
	return c.failureTracker
}

func (c *ApiConnectivityCheck) effectiveFailureWindow() time.Duration {
	if c.config == nil {
		return 0
	}
	if c.config.FailureWindow > 0 {
		return c.config.FailureWindow
	}
	if c.config.CheckInterval > 0 && c.config.MaxErrorsThreshold > 0 {
		return c.config.CheckInterval * time.Duration(c.config.MaxErrorsThreshold)
	}
	return 0
}

func (c *ApiConnectivityCheck) effectivePeerQuorumTimeout() time.Duration {
	if c.config == nil {
		return 0
	}
	if c.config.PeerQuorumTimeout > 0 {
		return c.config.PeerQuorumTimeout
	}
	return c.config.MaxTimeForNoPeersResponse
}

func (c *ApiConnectivityCheck) peerTimeoutExceeded(role peers.Role, now time.Time) bool {
	deadline := c.effectivePeerQuorumTimeout()
	if deadline <= 0 {
		return false
	}

	var silenceSince time.Time
	if role == peers.Worker {
		silenceSince = c.workerPeerSilenceSince
	} else {
		silenceSince = c.controlPlanePeerSilenceSince
	}

	if silenceSince.IsZero() {
		return false
	}

	return now.Sub(silenceSince) >= deadline
}

func (c *ApiConnectivityCheck) popPeerIPs(peersIPs *[]corev1.PodIP, count int) []corev1.PodIP {
	nrOfPeers := len(*peersIPs)
	if nrOfPeers == 0 {
		return []corev1.PodIP{}
	}

	if count > nrOfPeers {
		count = nrOfPeers
	}

	// TODO: maybe we should pick nodes randomly rather than relying on the order returned from api-server
	selectedIPs := make([]corev1.PodIP, count)
	for i := 0; i < count; i++ {
		ip := (*peersIPs)[i]
		if ip.IP == "" {
			// This should not happen, but keeping it for good measure.
			c.config.Log.Info("ignoring peers without IP address")
			continue
		}
		selectedIPs[i] = ip
	}

	*peersIPs = (*peersIPs)[count:] //remove popped nodes from the list

	return selectedIPs
}

func (c *ApiConnectivityCheck) getHealthStatusFromPeers(addresses []corev1.PodIP) (int, int, int, int) {
	nrAddresses := len(addresses)
	responsesChan := make(chan selfNodeRemediation.HealthCheckResponseCode, nrAddresses)

	c.config.Log.Info("Attempting to get health status from peers", "addresses", addresses)

	for _, address := range addresses {
		go c.getHealthStatusFromPeer(address, responsesChan)
	}

	return c.sumPeersResponses(nrAddresses, responsesChan)
}

// getEffectivePeerRequestTimeout calculates the effective peer request timeout
// ensuring it's safe relative to the API server timeout by enforcing a minimum buffer
func (c *ApiConnectivityCheck) getEffectivePeerRequestTimeout() time.Duration {
	minimumSafeTimeout := c.config.ApiServerTimeout + v1alpha1.MinimumBuffer

	if c.config.PeerRequestTimeout < minimumSafeTimeout {
		c.config.Log.Info("PeerRequestTimeout is too low, using adjusted value for safety",
			"configuredTimeout", c.config.PeerRequestTimeout,
			"apiServerTimeout", c.config.ApiServerTimeout,
			"minimumBuffer", v1alpha1.MinimumBuffer,
			"effectiveTimeout", minimumSafeTimeout)
		events.WarningEventf(c.config.Recorder, &v1alpha1.SelfNodeRemediationConfig{ObjectMeta: metav1.ObjectMeta{Name: v1alpha1.ConfigCRName}}, eventReasonPeerTimeoutAdjusted, "PeerRequestTimeout (%s) was too low compared to ApiServerTimeout (%s), using safe value (%s) instead", c.config.PeerRequestTimeout, c.config.ApiServerTimeout, minimumSafeTimeout)
		return minimumSafeTimeout
	}

	return c.config.PeerRequestTimeout
}

func (c *ApiConnectivityCheck) SetHealthStatusFunc(f GetHealthStatusFromRemoteFunc) {
	c.getHealthStatusFromRemoteFunc = f
}

func (c *ApiConnectivityCheck) GetHealthStatusFunc() (f GetHealthStatusFromRemoteFunc) {
	f = c.getHealthStatusFromRemoteFunc
	return
}

// GetHealthStatusFromPeer issues a GET request to the specified IP and returns the result from the peer into the given channel
func (c *ApiConnectivityCheck) getHealthStatusFromPeer(endpointIp corev1.PodIP, results chan<- selfNodeRemediation.HealthCheckResponseCode) {
	c.getHealthStatusFromRemoteFunc(endpointIp, results)
}

func (c *ApiConnectivityCheck) initClientCreds() error {
	c.mutex.Lock()
	defer c.mutex.Unlock()
	if c.clientCreds == nil {
		clientCreds, err := certificates.GetClientCredentialsFromCerts(c.config.CertReader)
		if err != nil {
			return err
		}
		c.clientCreds = clientCreds
	}
	return nil
}

func (c *ApiConnectivityCheck) sumPeersResponses(nodesBatchCount int, responsesChan chan selfNodeRemediation.HealthCheckResponseCode) (int, int, int, int) {
	healthyResponses := 0
	unhealthyResponses := 0
	apiErrorsResponses := 0
	noResponse := 0

	for i := 0; i < nodesBatchCount; i++ {
		response := <-responsesChan
		switch response {
		case selfNodeRemediation.Unhealthy:
			unhealthyResponses++
			break
		case selfNodeRemediation.Healthy:
			healthyResponses++
			break
		case selfNodeRemediation.ApiError:
			apiErrorsResponses++
			break
		case selfNodeRemediation.RequestFailed:
			noResponse++
		default:
			c.config.Log.Error(fmt.Errorf("unexpected response"),
				"Received unexpected value from peer while trying to retrieve health status", "value", response)
		}
	}

	return healthyResponses, unhealthyResponses, apiErrorsResponses, noResponse
}
