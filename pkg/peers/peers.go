package peers

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/go-logr/logr"
	commonlabels "github.com/medik8s/common/pkg/labels"
	pkgerrors "github.com/pkg/errors"

	v1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
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
		updateControlPlanePeersError := p.UpdateControlPlanePeers(ctx)
		if updateWorkerPeersError != nil || updateControlPlanePeersError != nil {
			// the default update interval is quite long, in case of an error we want to retry quicker
			quickCtx, quickCancel := context.WithCancel(ctx)
			wait.UntilWithContext(quickCtx, func(ctx context.Context) {
				quickUpdateWorkerPeersError := p.updateWorkerPeers(ctx)
				quickUpdateControlPlanePeersError := p.UpdateControlPlanePeers(ctx)
				if quickUpdateWorkerPeersError == nil && quickUpdateControlPlanePeersError == nil {
					quickCancel()
				}
			}, quickPeerUpdateInterval)
		}
	}, p.peerUpdateInterval)

	return nil
}

func (p *Peers) updateWorkerPeers(ctx context.Context) error {
	p.log.Info("updateWorkerPeers entered")
	setterFunc := func(addresses []v1.PodIP) {
		p.log.Info("updateWorkerPeers setter called", "addresses", addresses)
		p.workerPeersAddresses = addresses
	}
	selectorGetter := func() labels.Selector {
		p.log.Info("updateWorkerPeers getter called", "workerPeerSelector", p.workerPeerSelector)
		return p.workerPeerSelector
	}
	resetFunc := func() {
		p.workerPeersAddresses = []v1.PodIP{}
	}
	return p.updatePeers(ctx, selectorGetter, setterFunc, resetFunc)
}

func (p *Peers) UpdateControlPlanePeers(ctx context.Context) error {
	p.log.Info("UpdateControlPlanePeers entered")

	setterFunc := func(addresses []v1.PodIP) {
		p.log.Info("UpdateControlPlanePeers setter called", "addresses", addresses)
		p.controlPlanePeersAddresses = addresses
	}
	selectorGetter := func() labels.Selector {
		p.log.Info("UpdateControlPlanePeers getter called", "workerPeerSelector", p.workerPeerSelector)
		return p.controlPlanePeerSelector
	}
	resetFunc := func() {
		p.controlPlanePeersAddresses = []v1.PodIP{}
	}
	return p.updatePeers(ctx, selectorGetter, setterFunc, resetFunc)
}

func (p *Peers) updatePeers(ctx context.Context, getSelector func() labels.Selector, setAddresses func(addresses []v1.PodIP),
	resetPeers func()) error {
	p.mutex.Lock()
	defer p.mutex.Unlock()

	readerCtx, cancel := context.WithTimeout(ctx, p.apiServerTimeout)
	defer cancel()

	nodes := v1.NodeList{}

	// get some nodes, but not ourself
	if err := p.List(readerCtx, &nodes, client.MatchingLabelsSelector{Selector: getSelector()}); err != nil {
		if apierrors.IsNotFound(err) {
			// we are the only node at the moment... reset peerList
			resetPeers()
		}
		p.log.Error(err, "failed to update peer list")
		return pkgerrors.Wrap(err, "failed to update peer list")
	}

	p.log.Info("updatePeers", "nodes", nodes)

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

	addresses, err := p.mapNodesToPrimaryPodIPs(nodes, pods)
	setAddresses(addresses)
	return err
}

func (p *Peers) mapNodesToPrimaryPodIPs(nodes v1.NodeList, pods v1.PodList) ([]v1.PodIP, error) {
	var err error
	addresses := []v1.PodIP{}

	for _, node := range nodes.Items {
		found := false
		for _, pod := range pods.Items {
			if pod.Spec.NodeName == node.Name {
				if len(pod.Status.PodIPs) == 0 || pod.Status.PodIPs[0].IP == "" {
					err = errors.Join(err, fmt.Errorf("empty IP for Pod %s on Node %s", pod.Name, node.Name))
				} else {
					found = true
					addresses = append(addresses, pod.Status.PodIPs[0])
				}
				break
			} else {
				p.log.Info("Skipping current node/pod combo",
					"node.Name", node.Name,
					"pod.Spec.NodeName", pod.Spec.NodeName)
			}
		}
		if !found {
			err = errors.Join(err, fmt.Errorf("Node %s has no matching Pod", node.Name))
		}
	}

	return addresses, err
}

func (p *Peers) GetPeersAddresses(role Role) []v1.PodIP {
	p.mutex.Lock()
	defer p.mutex.Unlock()

	p.log.Info("GetPeersAddresses", "workerPeersAddresses", p.workerPeersAddresses,
		"controlPlanePeersAddresses", p.controlPlanePeersAddresses)

	var addresses []v1.PodIP
	if role == Worker {
		addresses = p.workerPeersAddresses
		p.log.Info("Got a request to see how many worker peers exist", "workerPeersAddresses", p.workerPeersAddresses)
	} else {
		addresses = p.controlPlanePeersAddresses
		p.log.Info("Got a request to see how many control plane peers exist", "controlPlanePeersAddresses", p.controlPlanePeersAddresses)
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
