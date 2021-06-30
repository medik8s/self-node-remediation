package apicheck

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/go-logr/logr"

	v1 "k8s.io/api/core/v1"
	"k8s.io/apiextensions-apiserver/pkg/client/clientset/clientset"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"

	poisonPill "github.com/medik8s/poison-pill/api"
	"github.com/medik8s/poison-pill/pkg/certificates"
	"github.com/medik8s/poison-pill/pkg/peerhealth"
	"github.com/medik8s/poison-pill/pkg/peers"
	"github.com/medik8s/poison-pill/pkg/reboot"
)

const (
	peerRequestTimeout = 10 * time.Second
)

type ApiConnectivityCheck struct {
	client.Reader
	config     *ApiConnectivityCheckConfig
	errorCount int
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

func New(config *ApiConnectivityCheckConfig) *ApiConnectivityCheck {
	return &ApiConnectivityCheck{
		config: config,
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

		result := restClient.Verb(http.MethodGet).RequestURI("/readyz").Do(readerCtx)
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
			if isHealthy := c.handleError(); !isHealthy {
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

// HandleError keeps track of the number of errors reported, and when a certain amount of error occur within a certain
// time, ask peers if this node is healthy. Returns if the node is considered to be healthy or not.
func (c *ApiConnectivityCheck) handleError() bool {

	c.errorCount++
	if c.errorCount < c.config.MaxErrorsThreshold {
		c.config.Log.Info("Ignoring api-server error, error count below threshold", "current count", c.errorCount, "threshold", c.config.MaxErrorsThreshold)
		return true
	}

	c.config.Log.Info("Error count exceeds threshold, trying to ask other nodes if I'm healthy")
	nodesToAsk := c.config.Peers.GetPeers().Items
	if nodesToAsk == nil || len(nodesToAsk) == 0 {
		c.config.Log.Info("Peers list is empty and / or couldn't be retrieved from server, nothing we can do, so consider the node being healthy")
		//todo maybe we need to check if this happens too much and reboot
		return true
	}

	apiErrorsResponsesSum := 0
	nrAllNodes := len(nodesToAsk)
	// nodesToAsk is being reduced in every iteration, iterate until no nodes left to ask
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

		chosenNodesAddresses := c.popNodes(&nodesToAsk, nodesBatchCount)
		nrAddresses := len(chosenNodesAddresses)
		responsesChan := make(chan poisonPill.HealthCheckResponseCode, nrAddresses)

		for _, address := range chosenNodesAddresses {
			go c.getHealthStatusFromPeer(address, responsesChan)
		}

		healthyResponses, unhealthyResponses, apiErrorsResponses, _ := c.sumPeersResponses(nodesBatchCount, responsesChan)

		if healthyResponses > 0 {
			c.config.Log.Info("Peer told me I'm healthy.")
			c.errorCount = 0
			return true
		}

		if unhealthyResponses > 0 {
			c.config.Log.Info("Peer told me I'm unhealthy!")
			return false
		}

		if apiErrorsResponses > 0 {
			apiErrorsResponsesSum += apiErrorsResponses
			//todo consider using [m|n]hc.spec.maxUnhealthy instead of 50%
			if apiErrorsResponsesSum > nrAllNodes/2 { //already reached more than 50% of the nodes and all of them returned api error
				//assuming this is a control plane failure as others can't access api-server as well
				c.config.Log.Info("More than 50% of the nodes couldn't access the api-server, assuming this is a control plane failure")
				return true
			}
		}

	}

	//we asked all peers
	c.config.Log.Error(fmt.Errorf("failed health check"), "Failed to get health status peers. Assuming unhealthy")
	return false
}

func (c *ApiConnectivityCheck) popNodes(nodes *[]v1.Node, count int) []string {
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
		node := (*nodes)[i]
		if len(node.Status.Addresses) == 0 || node.Status.Addresses[0].Address == "" {
			c.config.Log.Info("could not get address from node", "node", node.Name)
			continue
		}
		addresses[i] = node.Status.Addresses[0].Address //todo node might have multiple addresses or none
	}

	*nodes = (*nodes)[count:] //remove popped nodes from the list

	return addresses
}

//getHealthStatusFromPeer issues a GET request to the specified IP and returns the result from the peer into the given channel
func (c *ApiConnectivityCheck) getHealthStatusFromPeer(endpointIp string, results chan<- poisonPill.HealthCheckResponseCode) {

	clientCreds, err := certificates.GetClientCredentialsFromCerts(c.config.CertReader)
	if err != nil {
		c.config.Log.Error(err, "failed to init client credentials")
		results <- poisonPill.RequestFailed
		return
	}

	// TODO does this work with IPv6?
	client, err := peerhealth.NewClient(fmt.Sprintf("%v:%v", endpointIp, c.config.PeerHealthPort), c.config.PeerDialTimeout, c.config.Log.WithName("peerhealth client"), clientCreds)
	if err != nil {
		c.config.Log.Error(err, "failed to init grpc client")
		results <- poisonPill.RequestFailed
		return
	}
	defer client.Close()

	ctx, cancel := context.WithTimeout(context.Background(), peerRequestTimeout)
	defer cancel()

	resp, err := client.IsHealthy(ctx, &peerhealth.HealthRequest{
		NodeName: c.config.MyNodeName,
	})
	if err != nil {
		c.config.Log.Error(err, "failed to read health response from peer")
		results <- poisonPill.RequestFailed
		return
	}

	results <- poisonPill.HealthCheckResponseCode(resp.Status)
	return
}

func (c *ApiConnectivityCheck) sumPeersResponses(nodesBatchCount int, responsesChan chan poisonPill.HealthCheckResponseCode) (int, int, int, int) {
	healthyResponses := 0
	unhealthyResponses := 0
	apiErrorsResponses := 0
	noResponse := 0

	for i := 0; i < nodesBatchCount; i++ {
		response := <-responsesChan
		c.config.Log.Info("got response from peer", "response", response)

		switch response {
		case poisonPill.Unhealthy:
			unhealthyResponses++
			break
		case poisonPill.Healthy:
			healthyResponses++
			break
		case poisonPill.ApiError:
			apiErrorsResponses++
			break
		case poisonPill.RequestFailed:
			noResponse++
		default:
			c.config.Log.Error(fmt.Errorf("unexpected response"),
				"Received unexpected value from peer while trying to retrieve health status", "value", response)
		}
	}

	return healthyResponses, unhealthyResponses, apiErrorsResponses, noResponse
}
