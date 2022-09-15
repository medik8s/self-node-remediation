package peers

import (
	"context"
	"fmt"
	"sigs.k8s.io/controller-runtime"
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
	hostnameLabelName     = "kubernetes.io/hostname"
	WorkerLabelName       = "node-role.kubernetes.io/worker"
	MasterLabelName       = "node-role.kubernetes.io/master"
	ControlPlaneLabelName = "node-role.kubernetes.io/control-plane" //replacing master label since k8s 1.25
)

var (
	controlPlaneLabelLock sync.Mutex
	UsedControlPlaneLabel string
)

type Role int8

const (
	Worker Role = iota
	Master
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
	SetControlPlaneLabelType(myNode)
	if hostname, ok := myNode.Labels[hostnameLabelName]; !ok {
		err := fmt.Errorf("%s label not set on own node", hostnameLabelName)
		p.log.Error(err, "failed to get own hostname")
		return err
	} else {
		p.workerPeerSelector = createSelector(hostname, Worker)
		p.masterPeerSelector = createSelector(hostname, Master)
	}

	go wait.UntilWithContext(ctx, func(ctx context.Context) {
		p.updateWorkerPeers(ctx)
		p.updateMasterPeers(ctx)
	}, p.peerUpdateInterval)

	p.log.Info("peers started")

	<-ctx.Done()
	return nil
}

func (p *Peers) updateWorkerPeers(ctx context.Context) {
	p.log.Info("[DEBUG] Updating Worker Peers")
	setterFunc := func(addresses [][]v1.NodeAddress) { p.workerPeersAddresses = addresses }
	selectorGetter := func() labels.Selector { return p.workerPeerSelector }
	p.updatePeers(ctx, selectorGetter, setterFunc)
}

func (p *Peers) updateMasterPeers(ctx context.Context) {
	p.log.Info("[DEBUG] Updating Master Peers")
	setterFunc := func(addresses [][]v1.NodeAddress) { p.masterPeersAddresses = addresses }
	selectorGetter := func() labels.Selector { return p.masterPeerSelector }
	p.updatePeers(ctx, selectorGetter, setterFunc)
}

func (p *Peers) updatePeers(ctx context.Context, getSelector func() labels.Selector, setAddresses func(addresses [][]v1.NodeAddress)) {
	p.mutex.Lock()
	defer p.mutex.Unlock()

	readerCtx, cancel := context.WithTimeout(ctx, p.apiServerTimeout)
	defer cancel()

	nodes := v1.NodeList{}
	// get some nodes, but not ourself
	if err := p.List(readerCtx, &nodes, client.MatchingLabelsSelector{Selector: getSelector()}); err != nil {
		if errors.IsNotFound(err) {
			// we are the only node at the moment... reset peerList
			p.workerPeersAddresses = [][]v1.NodeAddress{}
		}
		p.log.Error(err, "failed to update peer list")
		return
	}
	//TODO mshitrit remove
	p.debugLogPeers(nodes)

	nodesCount := len(nodes.Items)
	addresses := make([][]v1.NodeAddress, nodesCount)
	for i, node := range nodes.Items {
		addresses[i] = node.Status.Addresses
	}
	setAddresses(addresses)
}

func (p *Peers) GetPeersAddresses(role Role) [][]v1.NodeAddress {
	p.mutex.Lock()
	defer p.mutex.Unlock()

	var addresses [][]v1.NodeAddress
	if role == Worker {
		addresses = p.workerPeersAddresses
	} else {
		p.log.Info("[DEBUG] 2.1.1 - GetPeersAddresses For masters starting ", "addresses", p.masterPeersAddresses)
		addresses = p.masterPeersAddresses
	}
	//we don't want the caller to be able to change the addresses
	//so we create a deep copy and return it
	addressesCopy := make([][]v1.NodeAddress, len(addresses))
	for i := range addressesCopy {
		addressesCopy[i] = make([]v1.NodeAddress, len(addresses[i]))
		copy(addressesCopy, addresses)
	}
	if role == Master {
		p.log.Info("[DEBUG] 2.1.2 - GetPeersAddresses For masters done ", "addresses", addressesCopy)
	}

	return addressesCopy
}

func createSelector(hostNameToExclude string, nodeRole Role) labels.Selector {
	var nodeTypeLabel string

	switch nodeRole {
	case Master:
		nodeTypeLabel = GetUsedControlPlaneLabel()
	default:
		nodeTypeLabel = WorkerLabelName
	}

	reqNotMe, _ := labels.NewRequirement(hostnameLabelName, selection.NotEquals, []string{hostNameToExclude})
	reqPeers, _ := labels.NewRequirement(nodeTypeLabel, selection.Exists, []string{})
	selector := labels.NewSelector()
	selector = selector.Add(*reqNotMe, *reqPeers)
	return selector
}

func (p *Peers) debugLogPeers(nodes v1.NodeList) {
	p.log.Info("[DEBUG] node peers found", "node name", p.myNodeName, "number of peers", len(nodes.Items))
	for _, node := range nodes.Items {
		p.log.Info("[DEBUG] node peer name", "peer name", node.Name)
	}

}

func SetControlPlaneLabelType(node *v1.Node) {
	controlPlaneLabelLock.Lock()
	defer controlPlaneLabelLock.Unlock()
	if len(UsedControlPlaneLabel) != 0 {
		return
	}
	if _, isMasterLabel := node.Labels[MasterLabelName]; isMasterLabel {
		debugLog.Info("[DEBUG] setting master label", "label key", MasterLabelName)
		UsedControlPlaneLabel = MasterLabelName
	} else if _, isControlPlaneLabel := node.Labels[ControlPlaneLabelName]; isControlPlaneLabel {
		debugLog.Info("[DEBUG] setting master label", "label key", ControlPlaneLabelName)
		UsedControlPlaneLabel = ControlPlaneLabelName
	}
	debugLog.Info("[DEBUG] setting master label done")
}

func GetUsedControlPlaneLabel() string {
	controlPlaneLabelLock.Lock()
	defer controlPlaneLabelLock.Unlock()
	if len(UsedControlPlaneLabel) == 0 {
		debugLog.Info("[DEBUG] getting master label default value")
		return MasterLabelName
	}
	debugLog.Info("[DEBUG] getting master label set value")
	return UsedControlPlaneLabel
}

//TODO mshitrit remove this logger
var debugLog = controllerruntime.Log.WithName("master").WithName("Debug")
