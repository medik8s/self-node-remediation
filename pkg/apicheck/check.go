package apicheck

import (
	"context"
	"fmt"
	"io/ioutil"
	"net/http"
	"strconv"
	"time"

	"github.com/go-logr/logr"

	v1 "k8s.io/api/core/v1"
	"k8s.io/apiextensions-apiserver/pkg/client/clientset/clientset"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"

	poisonPill "github.com/medik8s/poison-pill/api"
	"github.com/medik8s/poison-pill/pkg/peers"
	"github.com/medik8s/poison-pill/pkg/reboot"
)

const (
	// TODO make some of this configurable?
	apiServerTimeout = 5 * time.Second
	peerProtocol     = "http"
	peerPort         = 30001
	peerTimeout      = 10 * time.Second
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
	httpClient         *http.Client
	nodeKey            client.ObjectKey
}

func New(myNodeName string, p *peers.Peers, r reboot.Rebooter, checkInterval time.Duration, maxErrorsThreshold int, cfg *rest.Config, log logr.Logger) *ApiConnectivityCheck {
	return &ApiConnectivityCheck{
		log:                log,
		myNodeName:         myNodeName,
		checkInterval:      checkInterval,
		maxErrorsThreshold: maxErrorsThreshold,
		peers:              p,
		rebooter:           r,
		cfg:                cfg,
		httpClient:         &http.Client{Timeout: peerTimeout},
		nodeKey: client.ObjectKey{
			Name: myNodeName,
		},
	}
}

func (c *ApiConnectivityCheck) Start(ctx context.Context) error {

	cs, err := clientset.NewForConfig(c.cfg)
	if err != nil {
		return err
	}
	client := cs.RESTClient()

	go wait.UntilWithContext(ctx, func(ctx context.Context) {

		readerCtx, cancel := context.WithTimeout(ctx, apiServerTimeout)
		defer cancel()

		result, err := client.Verb(http.MethodGet).RequestURI("/readyz").Do(readerCtx).Raw()
		if err != nil || string(result) != "ok" {
			c.log.Error(err, "failed to check api server")
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
		responsesChan := make(chan poisonPill.HealthCheckResponse, nrAddresses)

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
			if apiErrorsResponsesSum > len(c.peers.GetPeers().Items)/2 { //already reached more than 50% of the nodes and all of them returned api error
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
func (c *ApiConnectivityCheck) getHealthStatusFromPeer(endpointIp string, results chan<- poisonPill.HealthCheckResponse) {
	url := fmt.Sprintf("%s://%s:%d/health/%s", peerProtocol, endpointIp, peerPort, c.myNodeName)

	resp, err := c.httpClient.Get(url)
	if err != nil {
		c.log.Error(err, "failed to get health status from peer", "url", url)
		results <- poisonPill.RequestFailed
		return
	}

	defer func() {
		if err = resp.Body.Close(); err != nil {
			c.log.Error(err, "failed to close health response from peer")
		}
	}()

	body, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		c.log.Error(err, "failed to read health response from peer")
		results <- -1
		return
	}

	healthStatusResult, err := strconv.Atoi(string(body))

	if err != nil {
		c.log.Error(err, "failed to convert health check response from string to int")
	}

	results <- poisonPill.HealthCheckResponse(healthStatusResult)
	return
}

func (c *ApiConnectivityCheck) sumPeersResponses(nodesBatchCount int, responsesChan chan poisonPill.HealthCheckResponse) (int, int, int, int) {
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
