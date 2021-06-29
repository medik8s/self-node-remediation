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
	log                logr.Logger
	checkInterval      time.Duration
	maxErrorsThreshold int
	errorCount         int
	myNodeName         string
	peers              *peers.Peers
	rebooter           reboot.Rebooter
	cfg                *rest.Config
	nodeKey            client.ObjectKey
	certReader         certificates.CertStorageReader
	apiServerTimeout   time.Duration
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
	PeerTimeout        time.Duration
}

func New(config *ApiConnectivityCheckConfig) *ApiConnectivityCheck {
	return &ApiConnectivityCheck{
		log:                config.Log,
		myNodeName:         config.MyNodeName,
		checkInterval:      config.CheckInterval,
		maxErrorsThreshold: config.MaxErrorsThreshold,
		peers:              config.Peers,
		rebooter:           config.Rebooter,
		cfg:                config.Cfg,
		certReader:         config.CertReader,
		apiServerTimeout:   config.ApiServerTimeout,
		nodeKey: client.ObjectKey{
			Name: config.MyNodeName,
		},
	}
}

func (c *ApiConnectivityCheck) Start(ctx context.Context) error {

	cs, err := clientset.NewForConfig(c.cfg)
	if err != nil {
		return err
	}
	restClient := cs.RESTClient()

	go wait.UntilWithContext(ctx, func(ctx context.Context) {

		readerCtx, cancel := context.WithTimeout(ctx, c.apiServerTimeout)
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
			c.log.Error(fmt.Errorf(failure), "failed to check api server")
			if isHealthy := c.handleError(); !isHealthy {
				// we have a problem on this node
				c.log.Error(err, "we are unhealthy, triggering a reboot")
				if err := c.rebooter.Reboot(); err != nil {
					c.log.Error(err, "failed to trigger reboot")
				}
			} else {
				c.log.Error(err, "peers did not confirm that we are unhealthy, ignoring error")
			}
			return
		}

		// reset error count after a successful API call
		c.errorCount = 0

	}, c.checkInterval)

	c.log.Info("api connectivity check started")

	<-ctx.Done()
	return nil
}

// HandleError keeps track of the number of errors reported, and when a certain amount of error occur within a certain
// time, ask peers if this node is healthy. Returns if the node is considered to be healthy or not.
func (c *ApiConnectivityCheck) handleError() bool {

	c.errorCount++
	if c.errorCount < c.maxErrorsThreshold {
		c.log.Info("Ignoring api-server error, error count below threshold", "current count", c.errorCount, "threshold", c.maxErrorsThreshold)
		return true
	}

	c.log.Info("Error count exceeds threshold, trying to ask other nodes if I'm healthy")
	nodesToAsk := c.peers.GetPeers().Items
	if nodesToAsk == nil || len(nodesToAsk) == 0 {
		c.log.Info("Peers list is empty and / or couldn't be retrieved from server, nothing we can do, so consider the node being healthy")
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
			c.log.Info("Peer told me I'm healthy.")
			c.errorCount = 0
			return true
		}

		if unhealthyResponses > 0 {
			c.log.Info("Peer told me I'm unhealthy!")
			return false
		}

		if apiErrorsResponses > 0 {
			apiErrorsResponsesSum += apiErrorsResponses
			//todo consider using [m|n]hc.spec.maxUnhealthy instead of 50%
			if apiErrorsResponsesSum > nrAllNodes/2 { //already reached more than 50% of the nodes and all of them returned api error
				//assuming this is a control plane failure as others can't access api-server as well
				c.log.Info("More than 50% of the nodes couldn't access the api-server, assuming this is a control plane failure")
				return true
			}
		}

	}

	//we asked all peers
	c.log.Error(fmt.Errorf("failed health check"), "Failed to get health status peers. Assuming unhealthy")
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
			c.log.Info("could not get address from node", "node", node.Name)
			continue
		}
		addresses[i] = node.Status.Addresses[0].Address //todo node might have multiple addresses or none
	}

	*nodes = (*nodes)[count:] //remove popped nodes from the list

	return addresses
}

//getHealthStatusFromPeer issues a GET request to the specified IP and returns the result from the peer into the given channel
func (c *ApiConnectivityCheck) getHealthStatusFromPeer(endpointIp string, results chan<- poisonPill.HealthCheckResponseCode) {

	clientCreds, err := certificates.GetClientCredentialsFromCerts(c.certReader)
	if err != nil {
		c.log.Error(err, "failed to init client credentials")
		results <- poisonPill.RequestFailed
		return
	}

	client, err := peerhealth.NewClient(endpointIp, c.log.WithName("peerhealth client"), clientCreds)
	if err != nil {
		c.log.Error(err, "failed to init grpc client")
		results <- poisonPill.RequestFailed
		return
	}
	defer client.Close()

	ctx, cancel := context.WithTimeout(context.Background(), peerRequestTimeout)
	defer cancel()

	resp, err := client.IsHealthy(ctx, &peerhealth.HealthRequest{
		NodeName: c.nodeKey.Name,
	})
	if err != nil {
		c.log.Error(err, "failed to read health response from peer")
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
		c.log.Info("got response from peer", "response", response)

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
			c.log.Error(fmt.Errorf("unexpected response"),
				"Received unexpected value from peer while trying to retrieve health status", "value", response)
		}
	}

	return healthyResponses, unhealthyResponses, apiErrorsResponses, noResponse
}
