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
	// topologyKey is the node label identifying the failure domain of a node, empty when the feature is disabled.
	// Set once before Start.
	topologyKey string
	// The fields below are guarded by mutex.
	// myTopologyDomain is the value of topologyKey on our own node, empty when unknown
	myTopologyDomain string
	// topologyDomains maps a peer pod IP to the failure domain of its node, only for peers whose node carries the label
	topologyDomains map[string]string
	// controlPlaneNodeDomains maps every control plane node other than our own to its failure domain (empty when the
	// node has no label). It is built from the nodes, not from the agent pods, so that it stays complete when the
	// agent does not run on control plane nodes.
	controlPlaneNodeDomains map[string]string
}

// ControlPlaneDomains describes where the control plane nodes other than our own node are located, relative to our
// own failure domain.
type ControlPlaneDomains struct {
	// InMyDomain is the number of control plane nodes known to be in our own failure domain
	InMyDomain int
	// InOtherDomain is the number of control plane nodes known to be in another failure domain
	InOtherDomain int
	// Unknown is the number of control plane nodes that do not carry the topology label
	Unknown int
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
		topologyDomains:            map[string]string{},
		controlPlaneNodeDomains:    map[string]string{},
	}
}

// SetTopologyKey enables failure domain awareness: peers are grouped by the value of the given node label.
// An empty key disables the feature. Must be called before Start.
func (p *Peers) SetTopologyKey(key string) {
	p.topologyKey = key
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
	if p.topologyKey != "" {
		myTopologyDomain := myNode.Labels[p.topologyKey]
		// read concurrently by the API connectivity check
		p.mutex.Lock()
		p.myTopologyDomain = myTopologyDomain
		p.mutex.Unlock()
		if myTopologyDomain == "" {
			p.log.Info("topology key is set but own node does not carry the label, failure domain awareness is disabled on this node", "topologyKey", p.topologyKey)
		} else {
			p.log.Info("failure domain awareness enabled", "topologyKey", p.topologyKey, "myTopologyDomain", myTopologyDomain)
		}
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
	return p.updatePeers(ctx, selectorGetter, setterFunc, nil)
}

func (p *Peers) updateControlPlanePeers(ctx context.Context) error {
	setterFunc := func(addresses []v1.PodIP) { p.controlPlanePeersAddresses = addresses }
	selectorGetter := func() labels.Selector { return p.controlPlanePeerSelector }
	return p.updatePeers(ctx, selectorGetter, setterFunc, p.updateControlPlaneNodeDomains)
}

// updatePeers refreshes the peers of one role. setNodes, when not nil, receives the listed nodes of that role.
func (p *Peers) updatePeers(ctx context.Context, getSelector func() labels.Selector, setAddresses func(addresses []v1.PodIP), setNodes func(nodes v1.NodeList)) error {
	p.mutex.Lock()
	defer p.mutex.Unlock()

	readerCtx, cancel := context.WithTimeout(ctx, p.apiServerTimeout)
	defer cancel()

	nodes := v1.NodeList{}
	// get some nodes, but not ourself
	if err := p.List(readerCtx, &nodes, client.MatchingLabelsSelector{Selector: getSelector()}); err != nil {
		if apierrors.IsNotFound(err) {
			// we are the only node at the moment... reset peerList
			p.workerPeersAddresses = []v1.PodIP{}
		}
		p.log.Error(err, "failed to update peer list")
		return pkgerrors.Wrap(err, "failed to update peer list")
	}
	if setNodes != nil {
		setNodes(nodes)
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

	addresses, err := p.mapNodesToPrimaryPodIPs(nodes, pods)
	setAddresses(addresses)
	p.updateTopologyDomains(nodes, pods)
	return err
}

// updateControlPlaneNodeDomains records the failure domain of every control plane node other than our own.
func (p *Peers) updateControlPlaneNodeDomains(nodes v1.NodeList) {
	if p.topologyKey == "" {
		return
	}
	domains := make(map[string]string, len(nodes.Items))
	for _, node := range nodes.Items {
		domains[node.Name] = node.Labels[p.topologyKey] // empty when the node has no label
	}
	p.controlPlaneNodeDomains = domains
}

// GetControlPlaneDomains returns how the control plane nodes other than our own node are spread over the failure
// domains, relative to our own domain. All counts are zero when failure domain awareness is disabled or our own
// domain is unknown.
func (p *Peers) GetControlPlaneDomains() ControlPlaneDomains {
	p.mutex.Lock()
	defer p.mutex.Unlock()

	domains := ControlPlaneDomains{}
	if p.myTopologyDomain == "" {
		return domains
	}
	for _, domain := range p.controlPlaneNodeDomains {
		switch domain {
		case "":
			domains.Unknown++
		case p.myTopologyDomain:
			domains.InMyDomain++
		default:
			domains.InOtherDomain++
		}
	}
	return domains
}

// updateTopologyDomains records the failure domain of every peer whose node carries the topology label.
// Peers without the label are deliberately left out of the map: they are treated as being outside of our own domain.
// Only the pods running on the given nodes are considered: this is called once per role (workers, control planes)
// with the nodes of that role, and must not forget what was learned for the other role.
func (p *Peers) updateTopologyDomains(nodes v1.NodeList, pods v1.PodList) {
	if p.topologyKey == "" {
		return
	}
	domainByNode := map[string]string{}
	for _, node := range nodes.Items {
		domainByNode[node.Name] = node.Labels[p.topologyKey] // empty when the node has no label
	}
	for _, pod := range pods.Items {
		if len(pod.Status.PodIPs) == 0 || pod.Status.PodIPs[0].IP == "" {
			continue
		}
		domain, onListedNode := domainByNode[pod.Spec.NodeName]
		if !onListedNode {
			continue
		}
		ip := pod.Status.PodIPs[0].IP
		if domain != "" {
			p.topologyDomains[ip] = domain
		} else {
			delete(p.topologyDomains, ip)
		}
	}
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

// Snapshot is a consistent view of the peers of one role, taken under a single lock, so that the addresses,
// the failure domain classification and the number of peers outside of our own domain all describe the
// same peer list even if a peer refresh happens meanwhile.
type Snapshot struct {
	// Addresses are the pod IPs of the peers
	Addresses []v1.PodIP
	// InMyDomain tells, per pod IP, whether the peer is known to be in our own failure domain.
	// Empty when failure domain awareness is disabled or our own domain is unknown.
	InMyDomain map[string]bool
	// OutsideMyDomain is the number of peers that are not in our own failure domain (including peers whose
	// domain is unknown), or 0 when failure domain awareness is disabled or our own domain is unknown.
	OutsideMyDomain int
}

// GetPeersSnapshot returns a consistent snapshot of the peers of the given role, see Snapshot.
func (p *Peers) GetPeersSnapshot(role Role) Snapshot {
	p.mutex.Lock()
	defer p.mutex.Unlock()

	addresses := p.workerPeersAddresses
	if role == ControlPlane {
		addresses = p.controlPlanePeersAddresses
	}
	snapshot := Snapshot{
		Addresses:  make([]v1.PodIP, len(addresses)),
		InMyDomain: map[string]bool{},
	}
	copy(snapshot.Addresses, addresses)

	if p.myTopologyDomain == "" {
		return snapshot
	}
	for _, address := range addresses {
		domain, known := p.topologyDomains[address.IP]
		if known && domain == p.myTopologyDomain {
			snapshot.InMyDomain[address.IP] = true
		} else {
			snapshot.OutsideMyDomain++
		}
	}
	return snapshot
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
