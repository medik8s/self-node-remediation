package apicheck

import (
	"context"
	"fmt"
	"net"
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
	"github.com/medik8s/self-node-remediation/internal/certificates"
	"github.com/medik8s/self-node-remediation/internal/controlplane"
	"github.com/medik8s/self-node-remediation/internal/peerhealth"
	"github.com/medik8s/self-node-remediation/internal/peers"
	"github.com/medik8s/self-node-remediation/internal/reboot"
	"github.com/medik8s/self-node-remediation/internal/utils"
	snrwebhook "github.com/medik8s/self-node-remediation/internal/webhook/v1alpha1"
)

const (
	eventReasonPeerTimeoutAdjusted = "PeerTimeoutAdjusted"

	peerReachabilityTimeout = 2 * time.Second
	maxPeersToSample        = 3
)

// PeerDialer abstracts TCP dialing for peer reachability checks.
type PeerDialer interface {
	Dial(address string, timeout time.Duration) error
}

type netPeerDialer struct{}

func (d *netPeerDialer) Dial(address string, timeout time.Duration) error {
	conn, err := net.DialTimeout("tcp", address, timeout)
	if err != nil {
		return err
	}
	conn.Close()
	return nil
}

// PeerAddressProvider abstracts peer address retrieval.
// *peers.Peers satisfies this interface.
type PeerAddressProvider interface {
	GetPeersAddresses(role peers.Role) []corev1.PodIP
}

type ApiConnectivityCheck struct {
	client.Reader
	config                 *ApiConnectivityCheckConfig
	errorCount             int
	cpUnreachableCount     int
	timeOfLastPeerResponse time.Time
	clientCreds            credentials.TransportCredentials
	mutex                  sync.Mutex
	controlPlaneManager    *controlplane.Manager
	peerDialer             PeerDialer
}

type ApiConnectivityCheckConfig struct {
	Log                       logr.Logger
	MyNodeName                string
	MyMachineName             string
	CheckInterval             time.Duration
	MaxErrorsThreshold        int
	Peers                     PeerAddressProvider
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
}

func New(config *ApiConnectivityCheckConfig, controlPlaneManager *controlplane.Manager) *ApiConnectivityCheck {
	return &ApiConnectivityCheck{
		config:                 config,
		mutex:                  sync.Mutex{},
		controlPlaneManager:    controlPlaneManager,
		timeOfLastPeerResponse: time.Now(),
		peerDialer:             &netPeerDialer{},
	}
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

		// On control plane nodes, a passing readyz check is necessary but not
		// sufficient: the local API server may respond 200 even when the node
		// is network-isolated (the readyz sub-checks don't include etcd quorum).
		// Verify that we can actually reach at least one peer before concluding
		// we are healthy.
		if c.isControlPlane() && !c.canReachAnyPeer() {
			c.config.Log.Info("CP node: readyz passed but no peers are reachable, treating as connectivity failure")
			if isHealthy := c.isConsideredHealthy(); !isHealthy {
				c.config.Log.Info("CP isolation detected despite passing readyz, triggering a reboot")
				if err := c.config.Rebooter.Reboot(); err != nil {
					c.config.Log.Error(err, "failed to trigger reboot")
				}
			} else {
				c.config.Log.Info("peers did not confirm CP isolation, ignoring")
			}
			return
		}

		// reset error count after a fully successful check
		c.errorCount = 0
		c.cpUnreachableCount = 0

	}, c.config.CheckInterval)

	return nil
}

// isControlPlane returns true if this node is a control plane node.
func (c *ApiConnectivityCheck) isControlPlane() bool {
	return c.controlPlaneManager != nil && c.controlPlaneManager.IsControlPlane()
}

// canReachAnyPeer performs a lightweight TCP dial to a sample of peers to verify
// network connectivity. Returns true if at least one peer is reachable.
func (c *ApiConnectivityCheck) canReachAnyPeer() bool {
	allPeers := c.getAllPeerAddresses()
	if len(allPeers) == 0 {
		// No peers exist — we cannot determine isolation without peers.
		// Return true to avoid false positives on single-node clusters.
		c.config.Log.Info("canReachAnyPeer: no peers available, assuming reachable")
		return true
	}

	// Sample up to maxPeersToSample peers
	sampleSize := len(allPeers)
	if sampleSize > maxPeersToSample {
		sampleSize = maxPeersToSample
	}

	results := make(chan bool, sampleSize)
	for i := 0; i < sampleSize; i++ {
		peerAddr := fmt.Sprintf("%s:%d", allPeers[i].IP, c.config.PeerHealthPort)
		go func(addr string) {
			results <- c.peerDialer.Dial(addr, peerReachabilityTimeout) == nil
		}(peerAddr)
	}

	for i := 0; i < sampleSize; i++ {
		if <-results {
			return true
		}
	}

	c.config.Log.Info("canReachAnyPeer: no peers reachable",
		"peersChecked", sampleSize, "totalPeers", len(allPeers))
	return false
}

// getAllPeerAddresses returns a combined, deduplicated list of peer addresses.
func (c *ApiConnectivityCheck) getAllPeerAddresses() []corev1.PodIP {
	workerPeers := c.config.Peers.GetPeersAddresses(peers.Worker)
	cpPeers := c.config.Peers.GetPeersAddresses(peers.ControlPlane)

	// Deduplicate: on compact clusters, the same node may appear in both lists.
	seen := make(map[string]struct{}, len(workerPeers)+len(cpPeers))
	combined := make([]corev1.PodIP, 0, len(workerPeers)+len(cpPeers))

	for _, p := range workerPeers {
		if _, exists := seen[p.IP]; !exists && p.IP != "" {
			seen[p.IP] = struct{}{}
			combined = append(combined, p)
		}
	}
	for _, p := range cpPeers {
		if _, exists := seen[p.IP]; !exists && p.IP != "" {
			seen[p.IP] = struct{}{}
			combined = append(combined, p)
		}
	}
	return combined
}

// isConsideredHealthy keeps track of the number of errors reported, and when a certain amount of error occur within a certain
// time, ask peers if this node is healthy. Returns if the node is considered to be healthy or not.
func (c *ApiConnectivityCheck) isConsideredHealthy() bool {
	workerPeersResponse := c.getWorkerPeersResponse()
	isWorkerNode := c.controlPlaneManager == nil || !c.controlPlaneManager.IsControlPlane()
	if isWorkerNode {
		return workerPeersResponse.IsHealthy
	}
	canBeReached, cpUnhealthy := c.getControlPlanePeersStatus()
	if cpUnhealthy {
		c.config.Log.Info("Peer control plane nodes reported this node as unhealthy, triggering remediation")
		return false
	}

	// Track CP peer unreachability independently of the worker error threshold.
	// When CP peers are consistently unreachable, this is a strong isolation
	// signal that should not be masked by the worker threshold not being reached.
	if !canBeReached {
		c.cpUnreachableCount++
		if c.cpUnreachableCount >= c.config.MaxErrorsThreshold {
			c.config.Log.Info("CP peers consistently unreachable, escalating to isolation-based health assessment",
				"cpUnreachableCount", c.cpUnreachableCount, "threshold", c.config.MaxErrorsThreshold)
			return c.controlPlaneManager.IsControlPlaneHealthy(
				peers.Response{IsHealthy: false, Reason: peers.UnHealthyBecauseNodeIsIsolated},
				false,
			)
		}
	} else {
		c.cpUnreachableCount = 0
	}

	return c.controlPlaneManager.IsControlPlaneHealthy(workerPeersResponse, canBeReached)
}

func (c *ApiConnectivityCheck) getWorkerPeersResponse() peers.Response {
	c.errorCount++
	if c.errorCount < c.config.MaxErrorsThreshold {
		c.config.Log.Info("Ignoring api-server error, error count below threshold", "current count", c.errorCount, "threshold", c.config.MaxErrorsThreshold)
		return peers.Response{IsHealthy: true, Reason: peers.HealthyBecauseErrorsThresholdNotReached}
	}

	c.config.Log.Info("Error count exceeds threshold, trying to ask other nodes if I'm healthy")
	peersToAsk := c.config.Peers.GetPeersAddresses(peers.Worker)

	// We check to see if we have at least the number of peers that the user has configured as required.
	//  If we don't have this many peers (for instance there are zero peers, and the default value is set
	//  which requires at least one peer), we don't want to remediate. In this case we have some confusion
	//  and don't want to remediate a node when we shouldn't.  Note: It would be unusual for MinPeersForRemediation
	//  to be greater than 1 unless the environment has specific requirements.
	if len(peersToAsk) < c.config.MinPeersForRemediation {
		c.config.Log.Info("Ignoring api-server error as we have an insufficient number of peers found, "+
			"so we aren't going to attempt to contact any to check for a SelfNodeRemediation CR"+
			" - we will consider it as if there was no CR present & as healthy.", "minPeersRequired",
			c.config.MinPeersForRemediation, "actualNumPeersFound", len(peersToAsk))

		// TODO: maybe we need to check if this happens too much and reboot
		return peers.Response{IsHealthy: true, Reason: peers.HealthyBecauseNoPeersWereFound}
	}

	// If we make it here and there are no peers, we can't proceed because we need at least one peer
	//  to check.  So it doesn't make sense to continue on - we'll mark as unhealthy and exit fast
	if len(peersToAsk) == 0 {
		c.config.Log.Info("Marking node as unhealthy due to being isolated.  We don't have any peers to ask "+
			"and MinPeersForRemediation must be greater than zero", "minPeersRequired",
			c.config.MinPeersForRemediation, "actualNumPeersFound", len(peersToAsk))
		return peers.Response{IsHealthy: false, Reason: peers.UnHealthyBecauseNodeIsIsolated}
	}

	apiErrorsResponsesSum := 0
	nrAllPeers := len(peersToAsk)
	// peersToAsk is being reduced at every iteration, iterate until no peers left to ask
	for i := 0; len(peersToAsk) > 0; i++ {

		batchSize := utils.GetNextBatchSize(nrAllPeers, len(peersToAsk))
		chosenPeersIPs := c.popPeerIPs(&peersToAsk, batchSize)
		healthyResponses, unhealthyResponses, apiErrorsResponses, _ := c.getHealthStatusFromPeers(chosenPeersIPs)
		if healthyResponses+unhealthyResponses+apiErrorsResponses > 0 {
			c.timeOfLastPeerResponse = time.Now()
		}
		c.config.Log.Info("Aggregate peer health responses", "healthyResponses", healthyResponses,
			"unhealthyResponses", unhealthyResponses, "apiErrorsResponses", apiErrorsResponses)

		if healthyResponses > 0 {
			c.config.Log.Info("There is at least one peer who thinks this node healthy, so we'll respond "+
				"with a healthy status", "healthyResponses", healthyResponses, "reason",
				"peers.HealthyBecauseCRNotFound")
			c.errorCount = 0
			return peers.Response{IsHealthy: true, Reason: peers.HealthyBecauseCRNotFound}
		}

		if unhealthyResponses > 0 {
			c.config.Log.Info("I got at least one peer who thinks I'm unhealthy, so we'll respond "+
				"with unhealthy", "unhealthyResponses", unhealthyResponses, "reason",
				"peers.UnHealthyBecausePeersResponse")
			return peers.Response{IsHealthy: false, Reason: peers.UnHealthyBecausePeersResponse}
		}

		if apiErrorsResponses > 0 {
			c.config.Log.Info("If you see this, I didn't get any healthy or unhealthy peer responses, "+
				"instead they told me they can't access the API server either", "apiErrorsResponses",
				apiErrorsResponses)
			apiErrorsResponsesSum += apiErrorsResponses
			// TODO: consider using [m|n]hc.spec.maxUnhealthy instead of 50%
			if apiErrorsResponsesSum > nrAllPeers/2 { // already reached more than 50% of the peers and all of them returned api error
				// assuming this is a control plane failure as others can't access api-server as well
				c.config.Log.Info("More than 50% of the nodes couldn't access the api-server, assuming "+
					"this is a control plane failure, so we are going to return healthy in that case",
					"reason", "HealthyBecauseMostPeersCantAccessAPIServer")
				return peers.Response{IsHealthy: true, Reason: peers.HealthyBecauseMostPeersCantAccessAPIServer}
			}
		}

	}

	c.config.Log.Info("We have attempted communication with all known peers, and haven't gotten either: " +
		"a peer that believes we are healthy, a peer that believes we are unhealthy, or we haven't decided that " +
		"there is a control plane failure")

	//we asked all peers
	now := time.Now()
	// MaxTimeForNoPeersResponse check prevents the node from being considered unhealthy in case of short network outages
	if now.After(c.timeOfLastPeerResponse.Add(c.config.MaxTimeForNoPeersResponse)) {
		c.config.Log.Error(fmt.Errorf("failed health check"), "Failed to get health status peers. "+
			"Assuming unhealthy", "reason", "UnHealthyBecauseNodeIsIsolated")
		return peers.Response{IsHealthy: false, Reason: peers.UnHealthyBecauseNodeIsIsolated}
	} else {
		c.config.Log.Info("Ignoring no peers response error, time is below threshold for no peers response",
			"time without peers response (seconds)", now.Sub(c.timeOfLastPeerResponse).Seconds(),
			"threshold (seconds)", c.config.MaxTimeForNoPeersResponse.Seconds(),
			"reason", "HealthyBecauseNoPeersResponseNotReachedTimeout")
		return peers.Response{IsHealthy: true, Reason: peers.HealthyBecauseNoPeersResponseNotReachedTimeout}
	}

}

// getControlPlanePeersStatus contacts peer control plane nodes and returns whether they can be reached
// and whether they consider this node unhealthy (i.e. a SNR CR exists for it).
func (c *ApiConnectivityCheck) getControlPlanePeersStatus() (canBeReached bool, consideredUnhealthy bool) {
	peersToAsk := c.config.Peers.GetPeersAddresses(peers.ControlPlane)
	numOfControlPlanePeers := len(peersToAsk)
	if numOfControlPlanePeers == 0 {
		c.config.Log.Info("Peers list is empty and / or couldn't be retrieved from server, other control planes can't be reached")
		return false, false
	}

	chosenPeersIPs := c.popPeerIPs(&peersToAsk, numOfControlPlanePeers)
	healthyResponses, unhealthyResponses, apiErrorsResponses, _ := c.getHealthStatusFromPeers(chosenPeersIPs)

	c.config.Log.Info("Control plane peers status", "healthyResponses", healthyResponses,
		"unhealthyResponses", unhealthyResponses, "apiErrorsResponses", apiErrorsResponses)

	// Any response is an indication of communication with a peer
	return (healthyResponses + unhealthyResponses + apiErrorsResponses) > 0, unhealthyResponses > 0
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

	for _, address := range addresses {
		go c.getHealthStatusFromPeer(address, responsesChan)
	}

	return c.sumPeersResponses(nrAddresses, responsesChan)
}

// getEffectivePeerRequestTimeout calculates the effective peer request timeout
// ensuring it's safe relative to the API server timeout by enforcing a minimum buffer
func (c *ApiConnectivityCheck) getEffectivePeerRequestTimeout() time.Duration {
	minimumSafeTimeout := c.config.ApiServerTimeout + snrwebhook.MinimumBuffer

	if c.config.PeerRequestTimeout < minimumSafeTimeout {
		// Log warning about timeout adjustment
		c.config.Log.Info("PeerRequestTimeout is too low, using adjusted value for safety",
			"configuredTimeout", c.config.PeerRequestTimeout,
			"apiServerTimeout", c.config.ApiServerTimeout,
			"minimumBuffer", snrwebhook.MinimumBuffer,
			"effectiveTimeout", minimumSafeTimeout)
		events.WarningEventf(c.config.Recorder, &v1alpha1.SelfNodeRemediationConfig{ObjectMeta: metav1.ObjectMeta{Name: v1alpha1.ConfigCRName}}, eventReasonPeerTimeoutAdjusted, "PeerRequestTimeout (%s) was too low compared to ApiServerTimeout (%s), using safe value (%s) instead", c.config.PeerRequestTimeout, c.config.ApiServerTimeout, minimumSafeTimeout)
		return minimumSafeTimeout
	}

	return c.config.PeerRequestTimeout
}

// getHealthStatusFromPeer issues a GET request to the specified IP and returns the result from the peer into the given channel
func (c *ApiConnectivityCheck) getHealthStatusFromPeer(endpointIp corev1.PodIP, results chan<- selfNodeRemediation.HealthCheckResponseCode) {

	logger := c.config.Log.WithValues("IP", endpointIp.IP)
	logger.Info("getting health status from peer")

	if err := c.initClientCreds(); err != nil {
		logger.Error(err, "failed to init client credentials")
		results <- selfNodeRemediation.RequestFailed
		return
	}

	// TODO does this work with IPv6?
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
