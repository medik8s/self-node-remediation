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

type ApiConnectivityCheck struct {
	client.Reader
	config                        *ApiConnectivityCheckConfig
	errorCount                    int
	timeOfLastPeerResponse        time.Time
	clientCreds                   credentials.TransportCredentials
	mutex                         sync.Mutex
	controlPlaneManager           *controlplane.Manager
	getHealthStatusFromRemoteFunc GetHealthStatusFromRemoteFunc
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
}

func New(config *ApiConnectivityCheckConfig, controlPlaneManager *controlplane.Manager) (c *ApiConnectivityCheck) {
	c = &ApiConnectivityCheck{
		config:                 config,
		mutex:                  sync.Mutex{},
		controlPlaneManager:    controlPlaneManager,
		timeOfLastPeerResponse: time.Now(),
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

		// reset error count after a successful API call
		c.errorCount = 0

	}, c.config.CheckInterval)

	return nil
}

// isConsideredHealthy keeps track of the number of errors reported, and when a certain amount of error occur within a certain
// time, ask peers if this node is healthy. Returns if the node is considered to be healthy or not.  It is usable
// whether this is a control plane node or a worker node
func (c *ApiConnectivityCheck) isConsideredHealthy() bool {

	isControlPlaneManagerNil := c.controlPlaneManager == nil

	isWorkerNode := isControlPlaneManagerNil || !c.controlPlaneManager.IsControlPlane()

	c.config.Log.Info("isConsideredHealthy called",
		"isControlPlaneManagerNil", isControlPlaneManagerNil,
		"isWorkerNode", isWorkerNode)

	workerPeersResponse := c.getPeersResponse(peers.Worker)

	if isWorkerNode {
		if workerPeersResponse.IsHealthy {
			c.config.Log.Info("isConsideredHealthy: I'm a worker node and my peers say I'm healthy",
				"workerPeersResponse.IsHealthy", workerPeersResponse.IsHealthy)
			return true
		}

		controlPlanePeersResponse := c.getPeersResponse(peers.ControlPlane)

		c.config.Log.Info("isConsideredHealthy: since peers think I'm unhealthy, double checking "+
			"by returning what the control plane nodes think of my state",
			"controlPlanePeersResponse.IsHealthy", controlPlanePeersResponse.IsHealthy)
		return controlPlanePeersResponse.IsHealthy

	}

	controlPlanePeersResponse := c.getPeersResponse(peers.ControlPlane)

	c.config.Log.Info("isConsideredHealthy: control planes report my health status",
		"controlPlanePeersResponse.IsHealthy", controlPlanePeersResponse.IsHealthy)

	isControlPlaneHealthy := c.controlPlaneManager.IsControlPlaneHealthy(controlPlanePeersResponse,
		c.canOtherControlPlanesBeReached())

	c.config.Log.Info("isConsideredHealthy: we have checked the control plane peer responses and cross "+
		"checked it against the control plane diagnostics ",
		"isControlPlaneHealthy", controlPlanePeersResponse.IsHealthy)

	return isControlPlaneHealthy

}

func (c *ApiConnectivityCheck) getPeersResponse(role peers.Role) peers.Response {
	c.errorCount++
	if c.errorCount < c.config.MaxErrorsThreshold {
		c.config.Log.Info("Ignoring api-server error, error count below threshold", "current count", c.errorCount, "threshold", c.config.MaxErrorsThreshold)
		return peers.Response{IsHealthy: true, Reason: peers.HealthyBecauseErrorsThresholdNotReached}
	}
	c.config.Log.Info("Error count was above threshold, we will continue and attempt to get the addressess" +
		" for our peers, I consider myself a WORKER at the moment")

	// MES: This gets called even if the current node is a control plane node.  Hopefully
	//	in an actual environment it is returning actual worker peers
	peersToAsk := c.config.Peers.GetPeersAddresses(role)

	c.config.Log.Info("Error count exceeds threshold, trying to ask other peer nodes if I'm healthy",
		"minPeersRequired", c.config.MinPeersForRemediation, "actualNumPeersFound", len(peersToAsk))

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

func (c *ApiConnectivityCheck) canOtherControlPlanesBeReached() bool {
	c.config.Log.Info("canOtherControlPlanesBeReached", "c.config.Peers",
		c.config.Peers)

	peersToAsk := c.config.Peers.GetPeersAddresses(peers.ControlPlane)
	numOfControlPlanePeers := len(peersToAsk)

	c.config.Log.Info("Getting peer control plane addresses", "peersToAsk",
		peersToAsk, "numOfControlPlanePeers", numOfControlPlanePeers)

	if numOfControlPlanePeers == 0 {
		c.config.Log.Info("Peers list is empty and / or couldn't be retrieved from server, other control planes can't be reached")
		return false
	}

	chosenPeersIPs := c.popPeerIPs(&peersToAsk, numOfControlPlanePeers)
	healthyResponses, unhealthyResponses, apiErrorsResponses, _ := c.getHealthStatusFromPeers(chosenPeersIPs)

	// Any response is an indication of communication with a peer
	return (healthyResponses + unhealthyResponses + apiErrorsResponses) > 0
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
