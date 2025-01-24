package peers

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/go-logr/logr"
	commonlabels "github.com/medik8s/common/pkg/labels"
	pkgerrors "github.com/pkg/errors"

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

	// this is used instead of "peerUpdateInterval" when peer update fails
	quickPeerUpdateInterval = 2 * time.Minute
)

type Peers struct {
	client.Reader
	log                                              logr.Logger
	workerPeerSelector, controlPlanePeerSelector     labels.Selector
	peerUpdateInterval                               time.Duration
	myNodeName                                       string
	mutex                                            sync.Mutex
	apiServerTimeout                                 time.Duration
	workerPeersAddresses, controlPlanePeersAddresses []v1.PodIP
}

func New(myNodeName string, peerUpdateInterval time.Duration, reader client.Reader, log logr.Logger, apiServerTimeout time.Duration) *Peers {
	return &Peers{
		Reader:                     reader,
		log:                        log,
		peerUpdateInterval:         peerUpdateInterval,
		myNodeName:                 myNodeName,
		mutex:                      sync.Mutex{},
		apiServerTimeout:           apiServerTimeout,
		workerPeersAddresses:       []v1.PodIP{},
		controlPlanePeersAddresses: []v1.PodIP{},
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

	p.log.Info("peer starting", "name", p.myNodeName)
	wait.UntilWithContext(ctx, func(ctx context.Context) {
		updateWorkerPeersError := p.updateWorkerPeers(ctx)
		updateControlPlanePeersError := p.updateControlPlanePeers(ctx)
		if updateWorkerPeersError != nil || updateControlPlanePeersError != nil {
			// the default update interval is quite long, in case of an error we want to retry quicker
			quickCtx, quickCancel := context.WithCancel(ctx)
			wait.UntilWithContext(quickCtx, func(ctx context.Context) {
				quickUpdateWorkerPeersError := p.updateWorkerPeers(ctx)
				quickUpdateControlPlanePeersError := p.updateControlPlanePeers(ctx)
				if quickUpdateWorkerPeersError == nil && quickUpdateControlPlanePeersError == nil {
					quickCancel()
				}
			}, quickPeerUpdateInterval)
		}
	}, p.peerUpdateInterval)

	return nil
}

func (p *Peers) updateWorkerPeers(ctx context.Context) error {
	setterFunc := func(addresses []v1.PodIP) { p.workerPeersAddresses = addresses }
	selectorGetter := func() labels.Selector { return p.workerPeerSelector }
	return p.updatePeers(ctx, selectorGetter, setterFunc)
}

func (p *Peers) updateControlPlanePeers(ctx context.Context) error {
	setterFunc := func(addresses []v1.PodIP) { p.controlPlanePeersAddresses = addresses }
	selectorGetter := func() labels.Selector { return p.controlPlanePeerSelector }
	return p.updatePeers(ctx, selectorGetter, setterFunc)
}

func (p *Peers) updatePeers(ctx context.Context, getSelector func() labels.Selector, setAddresses func(addresses []v1.PodIP)) error {
	p.mutex.Lock()
	defer p.mutex.Unlock()

	readerCtx, cancel := context.WithTimeout(ctx, p.apiServerTimeout)
	defer cancel()

	nodes := v1.NodeList{}
	// get some nodes, but not ourself
	if err := p.List(readerCtx, &nodes, client.MatchingLabelsSelector{Selector: getSelector()}); err != nil {
		if errors.IsNotFound(err) {
			// we are the only node at the moment... reset peerList
			p.workerPeersAddresses = []v1.PodIP{}
		}
		p.log.Error(err, "failed to update peer list")
		return pkgerrors.Wrap(err, "failed to update peer list")
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
		return pkgerrors.Wrap(err, "could not get pods")
	}

	nodesCount := len(nodes.Items)
	addresses := make([]v1.PodIP, nodesCount)
	for i, node := range nodes.Items {
		for _, pod := range pods.Items {
			if pod.Spec.NodeName == node.Name {
				if pod.Status.PodIPs == nil || len(pod.Status.PodIPs) == 0 {
					return pkgerrors.New(fmt.Sprintf("empty Pod IP for Pod %s on Node %s", pod.Name, node.Name))
				}
				addresses[i] = pod.Status.PodIPs[0]
			}
		}
	}
	setAddresses(addresses)
	return nil
}

func (p *Peers) GetPeersAddresses(role Role) []v1.PodIP {
	p.mutex.Lock()
	defer p.mutex.Unlock()

	var addresses []v1.PodIP
	if role == Worker {
		addresses = p.workerPeersAddresses
	} else {
		addresses = p.controlPlanePeersAddresses
	}
	//we don't want the caller to be able to change the addresses
	//so we create a deep copy and return it
	addressesCopy := make([]v1.PodIP, len(addresses))
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
