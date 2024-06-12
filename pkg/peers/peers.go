package peers

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/go-logr/logr"
	commonlabels "github.com/medik8s/common/pkg/labels"

	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/selection"
	"k8s.io/apimachinery/pkg/util/wait"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	hostnameLabelName = "kubernetes.io/hostname"
)

type Role int8

const (
	Worker Role = iota
	ControlPlane
)

type Peers struct {
	client.Reader
	log                                              logr.Logger
	workerPeerSelector, controlPlanePeerSelector     labels.Selector
	peerUpdateInterval                               time.Duration
	myNodeName                                       string
	mutex                                            sync.Mutex
	apiServerTimeout                                 time.Duration
	workerPeersAddresses, controlPlanePeersAddresses []string
}

func New(myNodeName string, peerUpdateInterval time.Duration, reader client.Reader, log logr.Logger, apiServerTimeout time.Duration) *Peers {
	return &Peers{
		Reader:                     reader,
		log:                        log,
		peerUpdateInterval:         peerUpdateInterval,
		myNodeName:                 myNodeName,
		mutex:                      sync.Mutex{},
		apiServerTimeout:           apiServerTimeout,
		workerPeersAddresses:       []string{},
		controlPlanePeersAddresses: []string{},
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
		p.workerPeerSelector = createSelector(hostname, commonlabels.WorkerRole)
		p.controlPlanePeerSelector = createSelector(hostname, getControlPlaneLabel(myNode))
	}

	go wait.UntilWithContext(ctx, func(ctx context.Context) {
		p.updateWorkerPeers(ctx)
		p.updateControlPlanePeers(ctx)
	}, p.peerUpdateInterval)

	p.log.Info("peers started")

	<-ctx.Done()
	return nil
}

func (p *Peers) updateWorkerPeers(ctx context.Context) {
	setterFunc := func(addresses []string) { p.workerPeersAddresses = addresses }
	selectorGetter := func() labels.Selector { return p.workerPeerSelector }
	p.updatePeers(ctx, selectorGetter, setterFunc)
}

func (p *Peers) updateControlPlanePeers(ctx context.Context) {
	setterFunc := func(addresses []string) { p.controlPlanePeersAddresses = addresses }
	selectorGetter := func() labels.Selector { return p.controlPlanePeerSelector }
	p.updatePeers(ctx, selectorGetter, setterFunc)
}

func (p *Peers) updatePeers(ctx context.Context, getSelector func() labels.Selector, setAddresses func(addresses []string)) {
	p.mutex.Lock()
	defer p.mutex.Unlock()

	readerCtx, cancel := context.WithTimeout(ctx, p.apiServerTimeout)
	defer cancel()

	nodes := v1.NodeList{}
	// get some nodes, but not ourself
	if err := p.List(readerCtx, &nodes, client.MatchingLabelsSelector{Selector: getSelector()}); err != nil {
		if errors.IsNotFound(err) {
			// we are the only node at the moment... reset peerList
			p.workerPeersAddresses = []string{}
		}
		p.log.Error(err, "failed to update peer list")
		return
	}

	pods := v1.PodList{}
	listOptions := &client.ListOptions{
		LabelSelector: labels.SelectorFromSet(labels.Set{
			"app.kubernetes.io/name":      "self-node-remediation",
			"app.kubernetes.io/component": "agent",
		}),
	}
	if err := p.List(readerCtx, &pods, listOptions); err != nil {
		p.log.Error(err, "could not get pods")
	}

	nodesCount := len(nodes.Items)
	addresses := make([]string, nodesCount)
	for i, node := range nodes.Items {
		for _, pod := range pods.Items {
			if pod.Spec.NodeName == node.Name {
				addresses[i] = pod.Status.PodIP
			}
		}
	}
	setAddresses(addresses)
}

func (p *Peers) GetPeersAddresses(role Role) []string {
	p.mutex.Lock()
	defer p.mutex.Unlock()

	var addresses []string
	if role == Worker {
		addresses = p.workerPeersAddresses
	} else {
		addresses = p.controlPlanePeersAddresses
	}
	//we don't want the caller to be able to change the addresses
	//so we create a deep copy and return it
	addressesCopy := make([]string, len(addresses))
	copy(addressesCopy, addresses)

	return addressesCopy
}

func createSelector(hostNameToExclude string, nodeTypeLabel string) labels.Selector {
	reqNotMe, _ := labels.NewRequirement(hostnameLabelName, selection.NotEquals, []string{hostNameToExclude})
	reqPeers, _ := labels.NewRequirement(nodeTypeLabel, selection.Exists, []string{})
	selector := labels.NewSelector()
	selector = selector.Add(*reqNotMe, *reqPeers)
	return selector
}

func getControlPlaneLabel(node *v1.Node) string {
	if _, isControlPlaneLabelExist := node.Labels[commonlabels.ControlPlaneRole]; isControlPlaneLabelExist {
		return commonlabels.ControlPlaneRole
	}
	return commonlabels.MasterRole
}
