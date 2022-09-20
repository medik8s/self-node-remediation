package apicheck

import (
	"context"
	"fmt"
	"github.com/medik8s/self-node-remediation/pkg/master"
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
	"github.com/medik8s/self-node-remediation/pkg/peerhealth"
	"github.com/medik8s/self-node-remediation/pkg/peers"
	"github.com/medik8s/self-node-remediation/pkg/reboot"
)

type ApiConnectivityCheck struct {
	client.Reader
	config        *ApiConnectivityCheckConfig
	errorCount    int
	clientCreds   credentials.TransportCredentials
	masterManager *master.Manager
	mutex         sync.Mutex
}

type ApiConnectivityCheckConfig struct {
	Log                logr.Logger
	MyNodeName         string
	CheckInterval      time.Duration
	MaxErrorsThreshold int
	Peers              *peers.Peers
	Rebooter           reboot.Rebooter
	Cfg                *rest.Config
	CertReader         certificates.CertStorageReader
	ApiServerTimeout   time.Duration
	PeerDialTimeout    time.Duration
	PeerRequestTimeout time.Duration
	PeerHealthPort     int
}

func New(config *ApiConnectivityCheckConfig, masterManager *master.Manager) *ApiConnectivityCheck {
	return &ApiConnectivityCheck{
		config:        config,
		mutex:         sync.Mutex{},
		masterManager: masterManager,
	}
}

func (c *ApiConnectivityCheck) Start(ctx context.Context) error {

	cs, err := clientset.NewForConfig(c.config.Cfg)
	if err != nil {
		return err
	}
	restClient := cs.RESTClient()

	go wait.UntilWithContext(ctx, func(ctx context.Context) {

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
		c.config.Log.Info("[DEBUG] 0 - Check Start ", "failure val", failure)
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
		}

		// reset error count after a successful API call
		c.errorCount = 0

	}, c.config.CheckInterval)

	c.config.Log.Info("api connectivity check started")

	<-ctx.Done()
	return nil
}

// isConsideredHealthy keeps track of the number of errors reported, and when a certain amount of error occur within a certain
// time, ask peers if this node is healthy. Returns if the node is considered to be healthy or not.
func (c *ApiConnectivityCheck) isConsideredHealthy() bool {
	workerPeersResponse := c.GetWorkerPeersResponse()
	isWorkerNode := c.masterManager == nil || !c.masterManager.IsMaster()
	c.config.Log.Info("[DEBUG] 1 - isConsideredHealthy ", "workerPeersResponse", workerPeersResponse, "isWorkerNode", isWorkerNode)
	if isWorkerNode {
		return workerPeersResponse.IsHealthy
	} else {
		return c.masterManager.IsMasterHealthy(workerPeersResponse, c.isOtherMastersCanBeReached())
	}

}

func (c *ApiConnectivityCheck) GetWorkerPeersResponse() peers.Response {
	c.config.Log.Info("[DEBUG] 0.1 GetWorkerPeersResponse starting")

	c.errorCount++
	if c.errorCount < c.config.MaxErrorsThreshold {
		//TODO mshitrit uncomment
		//c.config.Log.Info("Ignoring api-server error, error count below threshold", "current count", c.errorCount, "threshold", c.config.MaxErrorsThreshold)
		return peers.Response{IsHealthy: true, Reason: peers.HealthyBecauseErrorsThresholdNotReached}
	}

	c.config.Log.Info("Error count exceeds threshold, trying to ask other nodes if I'm healthy")
	nodesToAsk := c.config.Peers.GetPeersAddresses(peers.Worker)
	if nodesToAsk == nil || len(nodesToAsk) == 0 {
		//TODO mshitrit uncomment
		//c.config.Log.Info("Peers list is empty and / or couldn't be retrieved from server, nothing we can do, so consider the node being healthy")
		//todo maybe we need to check if this happens too much and reboot
		return peers.Response{IsHealthy: true, Reason: peers.HealthyBecauseNoPeersWereFound}
	}

	apiErrorsResponsesSum := 0
	nrAllNodes := len(nodesToAsk)
	// nodesToAsk is being reduced in every iteration, iterate until no nodes left to ask
	c.config.Log.Info("[DEBUG] 0.2 GetWorkerPeersResponse about to query", "# nodes", nrAllNodes)
	for i := 0; len(nodesToAsk) > 0; i++ {

		// start asking a few nodes only in first iteration to cover the case we get a healthy / unhealthy result
		nodesBatchCount := 3
		if i > 0 {
			// after that ask 10% of the cluster each time to check the api problem case
			nodesBatchCount = len(nodesToAsk) / 10
			if nodesBatchCount == 0 {
				nodesBatchCount = 1
			}
		}

		// but do not ask more then we have
		if len(nodesToAsk) < nodesBatchCount {
			nodesBatchCount = len(nodesToAsk)
		}

		chosenNodesAddresses := c.popNodes(&nodesToAsk, nodesBatchCount)
		nrAddresses := len(chosenNodesAddresses)
		responsesChan := make(chan selfNodeRemediation.HealthCheckResponseCode, nrAddresses)

		for _, address := range chosenNodesAddresses {
			c.config.Log.Info("[DEBUG] 0.4 GetWorkerPeersResponse ASYNC about to query", "node address", address)

			go c.getHealthStatusFromPeer(address, responsesChan, false)
		}
		c.config.Log.Info("[DEBUG] 0.6 GetWorkerPeersResponse Waiting for ASYNC routines")

		healthyResponses, unhealthyResponses, apiErrorsResponses, _ := c.sumPeersResponses(nrAddresses, responsesChan, false)

		c.config.Log.Info("[DEBUG] 0.8 GetWorkerPeersResponse Done waiting for ASYNC routines", "healthyResponses", healthyResponses, "unhealthyResponses", unhealthyResponses, "apiErrorsResponses", apiErrorsResponses)

		if healthyResponses > 0 {
			c.config.Log.Info("Peer told me I'm healthy.")
			c.errorCount = 0
			return peers.Response{IsHealthy: true, Reason: peers.HealthyBecauseCRNotFound}
		}

		if unhealthyResponses > 0 {
			c.config.Log.Info("Peer told me I'm unhealthy!")
			return peers.Response{IsHealthy: false, Reason: peers.UnHealthyBecauseCRFound}
		}

		if apiErrorsResponses > 0 {
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
	c.config.Log.Error(fmt.Errorf("failed health check"), "Failed to get health status peers. Assuming unhealthy")
	return peers.Response{IsHealthy: false, Reason: peers.UnHealthyBecauseNodeIsIsolated}

}

func (c *ApiConnectivityCheck) isOtherMastersCanBeReached() bool {
	c.config.Log.Info("[DEBUG] 2 - isOtherMastersCanBeReached starting... ")

	nodesToAsk := c.config.Peers.GetPeersAddresses(peers.Master)
	numOfMasterPeers := len(nodesToAsk)
	if nodesToAsk == nil || numOfMasterPeers == 0 {
		//TODO mshitrit uncomment
		//c.config.Log.Info("Peers list is empty and / or couldn't be retrieved from server, other masters can't be reached")

		c.config.Log.Info("[DEBUG] 2.2 - isOtherMastersCanBeReached return false ")
		return false
	}
	c.config.Log.Info("[DEBUG] 2.3 - isOtherMastersCanBeReached about to pop nodes")
	chosenNodesAddresses := c.popNodes(&nodesToAsk, numOfMasterPeers)

	nrAddresses := len(chosenNodesAddresses)
	responsesChan := make(chan selfNodeRemediation.HealthCheckResponseCode, nrAddresses)
	var wg sync.WaitGroup
	wg.Add(len(chosenNodesAddresses))
	for i, address := range chosenNodesAddresses {
		c.config.Log.Info("[DEBUG] 2.5 - isOtherMastersCanBeReached about to get peer health status ASYNC", "peer address", address, "index", i, "total chosenNodesAddresses", len(chosenNodesAddresses))
		tmpAddress := address
		go func() {
			defer wg.Done()
			c.getHealthStatusFromPeer(tmpAddress, responsesChan, true)
		}()

	}
	c.config.Log.Info("[DEBUG] 2.54 - isOtherMastersCanBeReached about to wait for ASYNC calls to complete")
	wg.Wait()
	c.config.Log.Info("[DEBUG] 2.58 - isOtherMastersCanBeReached done waiting for ASYNC calls to complete")
	//We are not expecting API Server connectivity at this stage, however an API Error is an indication to communication with the peer (peer is communicating with current node that it was unable to reach the API server)
	c.config.Log.Info("[DEBUG] 2.6 - isOtherMastersCanBeReached about to sumPeersResponses ")

	_, _, apiErrorsResponses, _ := c.sumPeersResponses(nrAddresses, responsesChan, true)

	c.config.Log.Info("[DEBUG] 2.7 - isOtherMastersCanBeReached return", "value", apiErrorsResponses > 0)
	return apiErrorsResponses > 0
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

//getHealthStatusFromPeer issues a GET request to the specified IP and returns the result from the peer into the given channel
func (c *ApiConnectivityCheck) getHealthStatusFromPeer(endpointIp string, results chan<- selfNodeRemediation.HealthCheckResponseCode, isMaster bool) {

	logger := c.config.Log.WithValues("IP", endpointIp)
	//TODO mshirit uncomment
	//logger.Info("getting health status from peer")
	//TODO mshitrit remove isMaster flag
	if isMaster {
		logger.Info("[DEBUG] 2.5.1 - getHealthStatusFromPeer masters starting")
	} else {
		c.config.Log.Info("[DEBUG] 0.4.1 getHealthStatusFromPeer workers starting")

	}

	if err := c.initClientCreds(); err != nil {
		logger.Error(err, "failed to init client credentials")
		results <- selfNodeRemediation.RequestFailed
		if isMaster {
			logger.Info("[DEBUG] 2.5.2 - getHealthStatusFromPeer masters RequestFailed", "error", err)
		} else {
			c.config.Log.Info("[DEBUG] 0.4.2 getHealthStatusFromPeer workers RequestFailed", "error", err)
		}

		return
	}

	if isMaster {
		logger.Info("[DEBUG] 2.5.3 - getHealthStatusFromPeer masters about to create client")
	} else {
		c.config.Log.Info("[DEBUG] 0.4.3 getHealthStatusFromPeer workers about to create client")
	}

	// TODO does this work with IPv6?
	phClient, err := peerhealth.NewClient(fmt.Sprintf("%v:%v", endpointIp, c.config.PeerHealthPort), c.config.PeerDialTimeout, c.config.Log.WithName("peerhealth client"), c.clientCreds)

	if isMaster {
		logger.Info("[DEBUG] 2.5.4 - getHealthStatusFromPeer  masters client  created", "error", err)
	} else {
		c.config.Log.Info("[DEBUG] 0.4.4 getHealthStatusFromPeer workers client  created", "error", err)
	}

	if err != nil {
		//TODO mshitrit uncomment
		//logger.Error(err, "failed to init grpc client")
		results <- selfNodeRemediation.RequestFailed
		if isMaster {
			logger.Info("[DEBUG] 2.5.5 - getHealthStatusFromPeer masters RequestFailed", "error", err)
		} else {
			c.config.Log.Info("[DEBUG] 0.4.5 getHealthStatusFromPeer workers RequestFailed", "error", err)
		}

		return
	}
	defer phClient.Close()

	ctx, cancel := context.WithTimeout(context.Background(), c.config.PeerRequestTimeout)
	defer cancel()

	resp, err := phClient.IsHealthy(ctx, &peerhealth.HealthRequest{
		NodeName: c.config.MyNodeName,
	})
	if err != nil {
		logger.Error(err, "failed to read health response from peer")
		results <- selfNodeRemediation.RequestFailed
		if isMaster {
			logger.Info("[DEBUG] 2.5.6 - getHealthStatusFromPeer masters RequestFailed", "error", err)
		} else {
			c.config.Log.Info("[DEBUG] 0.4.6 getHealthStatusFromPeer workers RequestFailed", "error", err)
		}
		return
	}

	logger.Info("got response from peer", "status", resp.Status)

	results <- selfNodeRemediation.HealthCheckResponseCode(resp.Status)
	if isMaster {
		logger.Info("[DEBUG] 2.5.7 - getHealthStatusFromPeer masters done", "status", resp.Status)
	} else {
		c.config.Log.Info("[DEBUG] 0.4.7 getHealthStatusFromPeer workers done", "status", resp.Status)
	}
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

func (c *ApiConnectivityCheck) sumPeersResponses(nodesBatchCount int, responsesChan chan selfNodeRemediation.HealthCheckResponseCode, isMaster bool) (int, int, int, int) {
	healthyResponses := 0
	unhealthyResponses := 0
	apiErrorsResponses := 0
	noResponse := 0
	//TODO mshitrit remove isMaster flag
	if isMaster {
		c.config.Log.Info("[DEBUG] 2.6.1 - sumPeersResponses masters starting")
	}
	for i := 0; i < nodesBatchCount; i++ {
		if isMaster {
			c.config.Log.Info("[DEBUG] 2.6.2 - sumPeersResponses masters summing ", "index", i, "total", nodesBatchCount)
		}
		response := <-responsesChan
		switch response {
		case selfNodeRemediation.Unhealthy:
			unhealthyResponses++
			if isMaster {
				c.config.Log.Info("[DEBUG] 2.6.3 - sumPeersResponses masters summing - Unhealthy")
			}
			break
		case selfNodeRemediation.Healthy:
			healthyResponses++
			if isMaster {
				c.config.Log.Info("[DEBUG] 2.6.4 - sumPeersResponses masters summing - Healthy")
			}
			break
		case selfNodeRemediation.ApiError:
			apiErrorsResponses++
			if isMaster {
				c.config.Log.Info("[DEBUG] 2.6.5 - sumPeersResponses masters summing - ApiError")
			}
			break
		case selfNodeRemediation.RequestFailed:
			noResponse++
			if isMaster {
				c.config.Log.Info("[DEBUG] 2.6.6 - sumPeersResponses masters summing - RequestFailed")
			}
		default:
			if isMaster {
				c.config.Log.Info("[DEBUG] 2.6.7 - sumPeersResponses masters summing - unexpected")
			}
			c.config.Log.Error(fmt.Errorf("unexpected response"),
				"Received unexpected value from peer while trying to retrieve health status", "value", response)
		}
	}
	if isMaster {
		c.config.Log.Info("[DEBUG] 2.6.8 - sumPeersResponses masters done", "healthyResponses", healthyResponses, "unhealthyResponses", unhealthyResponses, "apiErrorsResponses", apiErrorsResponses, "noResponse", noResponse)
	}
	return healthyResponses, unhealthyResponses, apiErrorsResponses, noResponse
}
