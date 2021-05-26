package peers

import (
	"context"
	"fmt"
	"io/ioutil"
	"net/http"
	"os"
	"strconv"
	"sync"
	"time"

	"github.com/go-logr/logr"

	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/selection"
	"k8s.io/apimachinery/pkg/util/wait"
	"sigs.k8s.io/controller-runtime/pkg/client"

	poisonPill "github.com/medik8s/poison-pill/api"
	"github.com/medik8s/poison-pill/pkg/reboot"
)

const (
	nodeNameEnvVar    = "MY_NODE_NAME"
	hostnameLabelName = "kubernetes.io/hostname"

	// TODO make some of this configurable?
	apiServerTimeout = 5 * time.Second
	nrOfPeers        = 15
	peerProtocol     = "http"
	peerPort         = 30001
	peerTimeout      = 10 * time.Second
)

type Peers struct {
	client.Reader
	log                logr.Logger
	peerList           *[]v1.Node
	peerSelector       labels.Selector
	peerUpdateInterval time.Duration
	ignoreNewErrorsFor time.Duration
	maxErrorsThreshold int
	mutex              sync.Mutex
	myNodeName         string
	rebooter           reboot.Rebooter
	lastErrorAt        time.Time
	errorCount         int
	httpClient         *http.Client
}

func New(r reboot.Rebooter, peerUpdateInterval time.Duration, ignoreNewErrorsFor time.Duration, maxErrorsThreshold int, reader client.Reader, log logr.Logger) *Peers {
	return &Peers{
		Reader:             reader,
		log:                log,
		peerUpdateInterval: peerUpdateInterval,
		ignoreNewErrorsFor: ignoreNewErrorsFor,
		maxErrorsThreshold: maxErrorsThreshold,
		mutex:              sync.Mutex{},
		myNodeName:         os.Getenv(nodeNameEnvVar),
		rebooter:           r,
		httpClient:         &http.Client{Timeout: peerTimeout},
	}
}

func (p *Peers) Start(ctx context.Context) error {

	// get own hostname label value and create a label selector from it
	// will be used for updating the peer list and skipping ourself
	myNode := &v1.Node{}
	key := client.ObjectKey{
		Name: p.myNodeName,
	}

	readerCtx, cancel := context.WithTimeout(ctx, apiServerTimeout)
	defer cancel()
	if err := p.Get(readerCtx, key, myNode); err != nil {
		p.log.Error(err, "failed to get own node")
		return err
	}
	if hostname, ok := myNode.Labels[hostnameLabelName]; !ok {
		err := fmt.Errorf("%s label not set on own node", hostnameLabelName)
		p.log.Error(err, "failed to get own hostname")
		return err
	} else {
		req, _ := labels.NewRequirement(hostnameLabelName, selection.NotEquals, []string{hostname})
		p.peerSelector = labels.NewSelector().Add(*req)
	}

	// get initial peer list
	p.updatePeers(ctx, false)

	go wait.UntilWithContext(ctx, func(ctx context.Context) {
		p.updatePeers(ctx, false)
	}, p.peerUpdateInterval)

	p.log.Info("peers started")

	<-ctx.Done()
	return nil
}

func (p *Peers) updatePeers(ctx context.Context, ignoreError bool) {
	p.mutex.Lock()
	defer p.mutex.Unlock()

	readerCtx, cancel := context.WithTimeout(ctx, apiServerTimeout)
	defer cancel()

	nodes := &v1.NodeList{}
	// get some nodes, but not ourself
	if err := p.List(readerCtx, nodes, client.Limit(nrOfPeers), client.MatchingLabelsSelector{Selector: p.peerSelector}); err != nil {
		if errors.IsNotFound(err) {
			// we are the only node at the moment... reset peerList
			p.peerList = &[]v1.Node{}
		}
		p.log.Error(err, "failed to update peer list")
		if ignoreError {
			return
		}
		if healthy := p.HandleError(ctx, err); !healthy {
			// we have a problem on this node
			p.log.Error(err, "we are unhealthy, triggering a reboot")
			if err := p.rebooter.Reboot(); err != nil {
				p.log.Error(err, "failed to trigger reboot")
			}
		} else {
			p.log.Error(err, "peers did not confirm that we are unhealthy, ignoring error")
		}
		return
	}
	// reset error count after a successful API call!
	p.reset()
	//p.log.Info("peers updated")
	p.peerList = &nodes.Items
}

func (p *Peers) getPeers() *[]v1.Node {
	p.mutex.Lock()
	defer p.mutex.Unlock()
	return p.peerList
}

// HandleError keeps track of the number of errors reported, and when a certain amount of error occur within a certain
// time, ask peers if this node is healthy. Returns if the node is considered to be healthy or not.
func (p *Peers) HandleError(ctx context.Context, newError error) bool {

	//we don't want to increase the err count too quick
	if !p.lastErrorAt.IsZero() && time.Now().Before(p.lastErrorAt.Add(p.ignoreNewErrorsFor)) {
		p.log.Info("Ignoring error, it came too fast after the last one", "error", newError)
		return true
	}

	p.lastErrorAt = time.Now()
	p.errorCount++

	if p.errorCount < p.maxErrorsThreshold {
		p.log.Info("Ignoring error, error count below threshold", "current count", p.errorCount, "threshold", p.maxErrorsThreshold)
		return true
	}

	p.log.Info("Error count exceeds threshold, trying to ask other nodes if I'm healthy")
	nodes := *p.getPeers() // de-reference nodes here, so we don't accidentally modify the original list later on
	if nodes == nil || len(nodes) == 0 {
		p.log.Info("Peers list is empty and / or couldn't be retrieved from server, nothing we can do, so consider the node being healthy")
		//todo maybe we need to check if this happens too much and reboot
		return true
	}

	// Re-enable nodesToAks in case we add some kind of retry here...
	//if nodesToAsk == nil || len(nodesToAsk) == 0 {
	//	//deep copy nodes to ask, as we're going to remove nodes we already asked from the list
	//	nodesToAsk = *nodes
	//}

	// TODO we only get max 15 nodes at the moment (does that make sense?), just ask a batch of 5 for now...?
	//nodesBatchCount := len(nodes) / 10
	//if nodesBatchCount == 0 {
	//	nodesBatchCount = 1
	//}
	nodesBatchCount := 5

	chosenNodesAddresses := p.popNodes(&nodes, nodesBatchCount)
	nrAddresses := len(chosenNodesAddresses)
	responsesChan := make(chan poisonPill.HealthCheckResponse, nrAddresses)

	for _, address := range chosenNodesAddresses {
		go p.getHealthStatusFromPeer(address, responsesChan)
	}

	healthyResponses, unhealthyResponses, apiErrorsResponses, _ := p.sumPeersResponses(nodesBatchCount, responsesChan)

	if healthyResponses > 0 {
		p.log.Info("Peer told me I'm healthy.")
		p.reset()
		return true
	}

	if unhealthyResponses > 0 {
		p.log.Info("Peer told me I'm unhealthy!")
		return false
	}

	//todo consider using [m|n]hc.spec.maxUnhealthy instead of 50%
	if apiErrorsResponses > nrAddresses/2 {
		//more than 50% of the nodes being asked returned an api error
		//assuming this is a control plane failure as others can't access api-server as well
		p.log.Info("More than 50% of the nodes couldn't access the api-server, assuming this is a control plane failure")
		p.reset()
		return true
	}

	// TODO really? ...
	// unclear status, assume we are unhealthy
	return false
}

func (p *Peers) popNodes(nodes *[]v1.Node, count int) []string {
	nrOfNodes := len(*nodes)
	if nrOfNodes == 0 {
		return []string{}
	}

	nodesCount := count
	if nodesCount > nrOfNodes {
		nodesCount = nrOfNodes
	}

	//todo maybe we should pick nodes randomly rather than relying on the order returned from api-server
	addresses := make([]string, nodesCount)
	for i := 0; i < nodesCount; i++ {
		addresses[i] = (*nodes)[i].Status.Addresses[0].Address //todo node might have multiple addresses or none
	}

	//*nodes = (*nodes)[nodesCount:] //remove popped nodes from the list

	return addresses
}

//getHealthStatusFromPeer issues a GET request to the specified IP and returns the result from the peer into the given channel
func (p *Peers) getHealthStatusFromPeer(endpointIp string, results chan<- poisonPill.HealthCheckResponse) {
	url := fmt.Sprintf("%s://%s:%d/health/%s", peerProtocol, endpointIp, peerPort, p.myNodeName)

	resp, err := p.httpClient.Get(url)
	if err != nil {
		p.log.Error(err, "failed to get health status from peer", "url", url)
		results <- poisonPill.RequestFailed
		return
	}

	defer func() {
		if err = resp.Body.Close(); err != nil {
			p.log.Error(err, "failed to close health response from peer")
		}
	}()

	body, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		p.log.Error(err, "failed to read health response from peer")
		results <- -1
		return
	}

	healthStatusResult, err := strconv.Atoi(string(body))

	if err != nil {
		p.log.Error(err, "failed to convert health check response from string to int")
	}

	results <- poisonPill.HealthCheckResponse(healthStatusResult)
	return
}

func (p *Peers) sumPeersResponses(nodesBatchCount int, responsesChan chan poisonPill.HealthCheckResponse) (int, int, int, int) {
	healthyResponses := 0
	unhealthyResponses := 0
	apiErrorsResponses := 0
	noResponse := 0

	for i := 0; i < nodesBatchCount; i++ {
		response := <-responsesChan
		p.log.Info("got response from peer", "response", response)

		switch response {
		case poisonPill.Unhealthy:
			healthyResponses++
			break
		case poisonPill.Healthy:
			unhealthyResponses++
			break
		case poisonPill.ApiError:
			apiErrorsResponses++
			break
		case poisonPill.RequestFailed:
			noResponse++
		default:
			p.log.Error(fmt.Errorf("unexpected response"),
				"Received unexpected value from peer while trying to retrieve health status", "value", response)
		}
	}

	return healthyResponses, unhealthyResponses, apiErrorsResponses, noResponse
}

func (p *Peers) reset() {
	p.lastErrorAt = time.Time{}
	p.errorCount = 0
}
