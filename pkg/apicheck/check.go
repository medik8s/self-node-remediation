package apicheck

import (
	"context"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/go-logr/logr"
	"google.golang.org/grpc/credentials"

	v1 "k8s.io/api/core/v1"
	"k8s.io/apiextensions-apiserver/pkg/client/clientset/clientset"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"

	selfNodeRemediation "github.com/medik8s/self-node-remediation/api"
	"github.com/medik8s/self-node-remediation/pkg/certificates"
	"github.com/medik8s/self-node-remediation/pkg/controlplane"
	"github.com/medik8s/self-node-remediation/pkg/peerhealth"
	"github.com/medik8s/self-node-remediation/pkg/peers"
	"github.com/medik8s/self-node-remediation/pkg/reboot"
	"github.com/medik8s/self-node-remediation/pkg/utils"
)

type ApiConnectivityCheck struct {
	client.Reader
	config                 *ApiConnectivityCheckConfig
	errorCount             int
	timeOfLastPeerResponse time.Time
	clientCreds            credentials.TransportCredentials
	mutex                  sync.Mutex
	controlPlaneManager    *controlplane.Manager
}

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
}

func New(config *ApiConnectivityCheckConfig, controlPlaneManager *controlplane.Manager) *ApiConnectivityCheck {
	return &ApiConnectivityCheck{
		config:                 config,
		mutex:                  sync.Mutex{},
		controlPlaneManager:    controlPlaneManager,
		timeOfLastPeerResponse: time.Now(),
	}
}

func (c *ApiConnectivityCheck) Start(ctx context.Context) error {

	cs, err := clientset.NewForConfig(c.config.Cfg)
	if err != nil {
		return err
	}
	restClient := cs.RESTClient()

	c.config.Log.Info("api connectivity check starting")

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
			c.config.Log.Error(fmt.Errorf(failure), "failed to check api server")
			if isHealthy := c.isConsideredHealthy(); !isHealthy {
				// we have a problem on this node
				c.config.Log.Error(err, "we are unhealthy, triggering a reboot")
				if err := c.config.Rebooter.Reboot(); err != nil {
					c.config.Log.Error(err, "failed to trigger reboot")
				}
			} else {
				c.config.Log.Error(err, "peers did not confirm that we are unhealthy, ignoring error")
			}
			return
		} else {
			c.config.Log.Info("Api check report", "client get result", result)
		}

		// reset error count after a successful API call
		c.errorCount = 0

	}, c.config.CheckInterval)

	return nil
}

// isConsideredHealthy keeps track of the number of errors reported, and when a certain amount of error occur within a certain
// time, ask peers if this node is healthy. Returns if the node is considered to be healthy or not.
func (c *ApiConnectivityCheck) isConsideredHealthy() bool {
	c.config.Log.Info("isConsideredHealthy")
	workerPeersResponse := c.getWorkerPeersResponse()
	isWorkerNode := c.controlPlaneManager == nil || !c.controlPlaneManager.IsControlPlane()
	if isWorkerNode {
		return workerPeersResponse.IsHealthy
	} else {
		return c.controlPlaneManager.IsControlPlaneHealthy(workerPeersResponse, c.canOtherControlPlanesBeReached())
	}

}

func (c *ApiConnectivityCheck) getWorkerPeersResponse() peers.Response {
	c.errorCount++
	c.config.Log.Info("getWorkerPeersResponse")
	if c.errorCount < c.config.MaxErrorsThreshold {
		c.config.Log.Info("Ignoring api-server error, error count below threshold", "current count", c.errorCount, "threshold", c.config.MaxErrorsThreshold)
		return peers.Response{IsHealthy: true, Reason: peers.HealthyBecauseErrorsThresholdNotReached}
	}

	c.config.Log.Info("Error count exceeds threshold, trying to ask other nodes if I'm healthy")
	nodesToAsk := c.config.Peers.GetPeersAddresses(peers.Worker)
	if nodesToAsk == nil || len(nodesToAsk) == 0 {
		c.config.Log.Info("Peers list is empty and / or couldn't be retrieved from server, nothing we can do, so consider the node being healthy")
		//todo maybe we need to check if this happens too much and reboot
		return peers.Response{IsHealthy: true, Reason: peers.HealthyBecauseNoPeersWereFound}
	}

	apiErrorsResponsesSum := 0
	nrAllNodes := len(nodesToAsk)
	// nodesToAsk is being reduced in every iteration, iterate until no nodes left to ask
	for i := 0; len(nodesToAsk) > 0; i++ {

		batchSize := utils.GetNextBatchSize(nrAllNodes, len(nodesToAsk))
		chosenNodesAddresses := c.popNodes(&nodesToAsk, batchSize)
		healthyResponses, unhealthyResponses, apiErrorsResponses, _ := c.getHealthStatusFromPeers(chosenNodesAddresses)
		if healthyResponses+unhealthyResponses+apiErrorsResponses > 0 {
			c.timeOfLastPeerResponse = time.Now()
		}

		if healthyResponses > 0 {
			c.config.Log.Info("Peer told me I'm healthy.")
			c.errorCount = 0
			return peers.Response{IsHealthy: true, Reason: peers.HealthyBecauseCRNotFound}
		}

		if unhealthyResponses > 0 {
			c.config.Log.Info("Peer told me I'm unhealthy!")
			return peers.Response{IsHealthy: false, Reason: peers.UnHealthyBecausePeersResponse}
		}

		if apiErrorsResponses > 0 {
			c.config.Log.Info("Peer can't access the api-server")
			apiErrorsResponsesSum += apiErrorsResponses
			//todo consider using [m|n]hc.spec.maxUnhealthy instead of 50%
			if apiErrorsResponsesSum > nrAllNodes/2 { //already reached more than 50% of the nodes and all of them returned api error
				//assuming this is a control plane failure as others can't access api-server as well
				c.config.Log.Info("More than 50% of the nodes couldn't access the api-server, assuming this is a control plane failure")
				return peers.Response{IsHealthy: true, Reason: peers.HealthyBecauseMostPeersCantAccessAPIServer}
			}
		}

	}

	//we asked all peers
	now := time.Now()
	// MaxTimeForNoPeersResponse check prevents the node from being considered unhealthy in case of short network outages
	if now.After(c.timeOfLastPeerResponse.Add(c.config.MaxTimeForNoPeersResponse)) {
		c.config.Log.Error(fmt.Errorf("failed health check"), "Failed to get health status peers. Assuming unhealthy")
		return peers.Response{IsHealthy: false, Reason: peers.UnHealthyBecauseNodeIsIsolated}
	} else {
		c.config.Log.Info("Ignoring no peers response error, time is below threshold for no peers response", "time without peers response (seconds)", now.Sub(c.timeOfLastPeerResponse).Seconds(), "threshold (seconds)", c.config.MaxTimeForNoPeersResponse.Seconds())
		return peers.Response{IsHealthy: true, Reason: peers.HealthyBecauseNoPeersResponseNotReachedTimeout}
	}

}

func (c *ApiConnectivityCheck) canOtherControlPlanesBeReached() bool {
	nodesToAsk := c.config.Peers.GetPeersAddresses(peers.ControlPlane)
	numOfControlPlanePeers := len(nodesToAsk)
	if numOfControlPlanePeers == 0 {
		c.config.Log.Info("Peers list is empty and / or couldn't be retrieved from server, other control planes can't be reached")
		return false
	}

	chosenNodesAddresses := c.popNodes(&nodesToAsk, numOfControlPlanePeers)
	healthyResponses, unhealthyResponses, apiErrorsResponses, _ := c.getHealthStatusFromPeers(chosenNodesAddresses)

	// Any response is an indication of communication with a peer
	return (healthyResponses + unhealthyResponses + apiErrorsResponses) > 0
}

func (c *ApiConnectivityCheck) popNodes(nodes *[][]v1.NodeAddress, count int) []string {
	nrOfNodes := len(*nodes)
	if nrOfNodes == 0 {
		return []string{}
	}

	if count > nrOfNodes {
		count = nrOfNodes
	}

	//todo maybe we should pick nodes randomly rather than relying on the order returned from api-server
	addresses := make([]string, count)
	for i := 0; i < count; i++ {
		nodeAddresses := (*nodes)[i]
		if len(nodeAddresses) == 0 || nodeAddresses[0].Address == "" {
			c.config.Log.Info("ignoring node without IP address")
			continue
		}
		addresses[i] = nodeAddresses[0].Address //todo node might have multiple addresses or none
	}

	*nodes = (*nodes)[count:] //remove popped nodes from the list

	return addresses
}

func (c *ApiConnectivityCheck) getHealthStatusFromPeers(addresses []string) (int, int, int, int) {
	nrAddresses := len(addresses)
	responsesChan := make(chan selfNodeRemediation.HealthCheckResponseCode, nrAddresses)

	for _, address := range addresses {
		go c.getHealthStatusFromPeer(address, responsesChan)
	}

	return c.sumPeersResponses(nrAddresses, responsesChan)
}

// getHealthStatusFromPeer issues a GET request to the specified IP and returns the result from the peer into the given channel
func (c *ApiConnectivityCheck) getHealthStatusFromPeer(endpointIp string, results chan<- selfNodeRemediation.HealthCheckResponseCode) {

	logger := c.config.Log.WithValues("IP", endpointIp)
	logger.Info("getting health status from peer")

	if err := c.initClientCreds(); err != nil {
		logger.Error(err, "failed to init client credentials")
		results <- selfNodeRemediation.RequestFailed
		return
	}

	// TODO does this work with IPv6?
	phClient, err := peerhealth.NewClient(fmt.Sprintf("%v:%v", endpointIp, c.config.PeerHealthPort), c.config.PeerDialTimeout, c.config.Log.WithName("peerhealth client"), c.clientCreds)
	if err != nil {
		logger.Error(err, "failed to init grpc client")
		results <- selfNodeRemediation.RequestFailed
		return
	}
	defer phClient.Close()

	ctx, cancel := context.WithTimeout(context.Background(), c.config.PeerRequestTimeout)
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
