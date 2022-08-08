package peers

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/go-logr/logr"

	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/selection"
	"k8s.io/apimachinery/pkg/util/wait"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	hostnameLabelName = "kubernetes.io/hostname"
	WorkerLabelName   = "node-role.kubernetes.io/worker"
	MasterLabelName   = "node-role.kubernetes.io/master"
)

type Peers struct {
	client.Reader
	log                                        logr.Logger
	workerPeerSelector, masterPeerSelector     labels.Selector
	peerUpdateInterval                         time.Duration
	myNodeName                                 string
	mutex                                      sync.Mutex
	apiServerTimeout                           time.Duration
	workerPeersAddresses, masterPeersAddresses [][]v1.NodeAddress
	peersNames                                 []string
}

func New(myNodeName string, peerUpdateInterval time.Duration, reader client.Reader, log logr.Logger, apiServerTimeout time.Duration) *Peers {
	return &Peers{
		Reader:               reader,
		log:                  log,
		peerUpdateInterval:   peerUpdateInterval,
		myNodeName:           myNodeName,
		mutex:                sync.Mutex{},
		apiServerTimeout:     apiServerTimeout,
		workerPeersAddresses: [][]v1.NodeAddress{},
		masterPeersAddresses: [][]v1.NodeAddress{},
	}
}

func (p *Peers) Start(ctx context.Context) error {

	// get own hostname label value and create a label selector from it
	// will be used for updating the peer list and skipping ourself
	myNode := &v1.Node{}
	key := client.ObjectKey{
		Name: p.myNodeName,
	}

	readerCtx, cancel := context.WithTimeout(ctx, p.apiServerTimeout)
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
		p.workerPeerSelector = createSelector(hostname, true)
		p.masterPeerSelector = createSelector(hostname, false)
	}

	go wait.UntilWithContext(ctx, func(ctx context.Context) {
		p.updatePeers(ctx)
	}, p.peerUpdateInterval)

	p.log.Info("peers started")

	<-ctx.Done()
	return nil
}



func (p *Peers) updatePeers(ctx context.Context) {
	p.mutex.Lock()
	defer p.mutex.Unlock()

	readerCtx, cancel := context.WithTimeout(ctx, p.apiServerTimeout)
	defer cancel()

	nodes := v1.NodeList{}
	// get some nodes, but not ourself
	if err := p.List(readerCtx, &nodes, client.MatchingLabelsSelector{Selector: p.workerPeerSelector}); err != nil {
		if errors.IsNotFound(err) {
			// we are the only node at the moment... reset peerList
			p.workerPeersAddresses = [][]v1.NodeAddress{}
			p.peersNames = []string{}
		}
		p.log.Error(err, "failed to update peer list")
		return
	}
	//TODO mshitrit remove
	p.log.Info("[DEBUG] node peers found", "mode name", p.myNodeName, "number of peers", len(nodes.Items))

	nodesCount := len(nodes.Items)
	addresses := make([][]v1.NodeAddress, nodesCount)
	peerNames := make([]string, nodesCount)
	for i, node := range nodes.Items {
		addresses[i] = node.Status.Addresses
		peerNames[i] = node.Name
	}
	p.workerPeersAddresses = addresses
	p.peersNames = peerNames
}

func (p *Peers) GetPeersAddresses() [][]v1.NodeAddress {
	p.mutex.Lock()
	defer p.mutex.Unlock()

	//we don't want the caller to be able to change the addresses
	//so we create a deep copy and return it
	addressesCopy := make([][]v1.NodeAddress, len(p.workerPeersAddresses))
	for i := range p.workerPeersAddresses {
		addressesCopy[i] = make([]v1.NodeAddress, len(p.workerPeersAddresses[i]))
		copy(addressesCopy, p.workerPeersAddresses)
	}

	return addressesCopy
}

func createSelector(hostNameToExclude string, isWorkerSelector bool) labels.Selector {
	var nodeTypeLabel string
	if isWorkerSelector{
		nodeTypeLabel = WorkerLabelName
	}else{
		nodeTypeLabel = MasterLabelName
	}

	reqNotMe, _ := labels.NewRequirement(hostnameLabelName, selection.NotEquals, []string{hostNameToExclude})
	reqWorkers, _ := labels.NewRequirement(nodeTypeLabel, selection.Exists, []string{})
	selector := labels.NewSelector()
	selector = selector.Add(*reqNotMe, *reqWorkers)
	return selector
}