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
	workerLabelName   = "node-role.kubernetes.io/worker"
)

type Peers struct {
	client.Reader
	log                logr.Logger
	peerSelector       labels.Selector
	peerUpdateInterval time.Duration
	myNodeName         string
	mutex              sync.Mutex
	apiServerTimeout   time.Duration
	peersAddresses     [][]v1.NodeAddress
}

func New(myNodeName string, peerUpdateInterval time.Duration, reader client.Reader, log logr.Logger, apiServerTimeout time.Duration) *Peers {
	return &Peers{
		Reader:             reader,
		log:                log,
		peerUpdateInterval: peerUpdateInterval,
		myNodeName:         myNodeName,
		mutex:              sync.Mutex{},
		apiServerTimeout:   apiServerTimeout,
		peersAddresses:     [][]v1.NodeAddress{},
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
		reqNotMe, _ := labels.NewRequirement(hostnameLabelName, selection.NotEquals, []string{hostname})
		reqWorkers, _ := labels.NewRequirement(workerLabelName, selection.Exists, []string{})
		selector := labels.NewSelector()
		selector = selector.Add(*reqNotMe, *reqWorkers)
		p.peerSelector = selector
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
	if err := p.List(readerCtx, &nodes, client.MatchingLabelsSelector{Selector: p.peerSelector}); err != nil {
		if errors.IsNotFound(err) {
			// we are the only node at the moment... reset peerList
			p.peersAddresses = [][]v1.NodeAddress{}
		}
		p.log.Error(err, "failed to update peer list")
		return
	}
	nodesCount := len(nodes.Items)
	addresses := make([][]v1.NodeAddress, nodesCount)
	for i, node := range nodes.Items {
		addresses[i] = node.Status.Addresses
	}
	p.peersAddresses = addresses
}

func (p *Peers) GetPeersAddresses() [][]v1.NodeAddress {
	p.mutex.Lock()
	defer p.mutex.Unlock()

	//we don't want the caller to be able to change the addresses
	//so we create a deep copy and return it
	addressesCopy := make([][]v1.NodeAddress, len(p.peersAddresses))
	for i := range p.peersAddresses {
		addressesCopy[i] = make([]v1.NodeAddress, len(p.peersAddresses[i]))
		copy(addressesCopy, p.peersAddresses)
	}

	return addressesCopy
}
