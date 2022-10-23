package master

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"time"

	"github.com/go-logr/logr"
	"github.com/go-ping/ping"

	corev1 "k8s.io/api/core/v1"

	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/medik8s/self-node-remediation/pkg/peers"
)

const (
	initErrorText    = "error initializing master handler"
	kubeletPort      = "10250"
	endpointToAccess = "www.google.com"
)

var (
	initError    = errors.New(initErrorText)
	processError = errors.New("an error occurred during master remediation process")
)

//Manager contains logic and info needed to fence and remediate master nodes
type Manager struct {
	nodeName                     string
	nodeRole                     peers.Role
	wasEndpointAccessibleAtStart bool
	client                       client.Client
	log                          logr.Logger
}

//NewManager inits a new Manager return nil if init fails
func NewManager(nodeName string, myClient client.Client) *Manager {
	return &Manager{
		nodeName:                     nodeName,
		client:                       myClient,
		wasEndpointAccessibleAtStart: false,
		log:                          ctrl.Log.WithName("master").WithName("Manager"),
	}
}

func (manager *Manager) Start(ctx context.Context) error {
	if err := manager.initializeManager(); err != nil {
		return err
	}
	return nil
}

func (manager *Manager) IsMaster() bool {
	return manager.nodeRole == peers.Master
}

func (manager *Manager) IsMasterHealthy(workerPeerResponse peers.Response, canOtherMastersBeReached bool) bool {
	switch workerPeerResponse.Reason {
	//reported unhealthy by worker peers
	case peers.UnHealthyBecausePeersResponse:
		return false
	case peers.UnHealthyBecauseNodeIsIsolated:
		return canOtherMastersBeReached
	//reported healthy by worker peers
	case peers.HealthyBecauseErrorsThresholdNotReached, peers.HealthyBecauseCRNotFound:
		return true
	//master node has connection to most workers, we assume it's not isolated (or at least that the master node that does not have worker peers quorum will reboot)
	case peers.HealthyBecauseMostPeersCantAccessAPIServer:
		return manager.isDiagnosticsPassed()
	case peers.HealthyBecauseNoPeersWereFound:
		return manager.isDiagnosticsPassed() && canOtherMastersBeReached

	default:
		manager.log.Error(processError, "node is considered unhealthy by worker peers for an unknown reason", "reason", workerPeerResponse.Reason, "node name", manager.nodeName)
		return false
	}

}

func (manager *Manager) isDiagnosticsPassed() bool {
	if manager.isEndpointAccessLost() {
		return false
	} else if !manager.isKubeletServiceRunning() {
		return false
	} else if !manager.isEtcdRunning() {
		return false
	}
	return true
}

func wrapWithInitError(err error) error {
	return fmt.Errorf(initErrorText+" [%w]", err)
}

func (manager *Manager) initializeManager() error {
	nodesList := &corev1.NodeList{}
	if err := manager.client.List(context.TODO(), nodesList, &client.ListOptions{}); err != nil {
		manager.log.Error(err, "could not retrieve nodes")
		return wrapWithInitError(err)
	}
	var node corev1.Node
	for _, n := range nodesList.Items {
		if n.Name == manager.nodeName {
			manager.setNodeRole(n)
			node = n
			break
		}
	}

	if &node == nil {
		manager.log.Error(initError, "could not find node")
		return initError
	}

	manager.wasEndpointAccessibleAtStart = manager.isEndpointAccessible()
	return nil
}

func (manager *Manager) setNodeRole(node corev1.Node) {
	if _, isWorker := node.Labels[peers.WorkerLabelName]; isWorker {
		manager.nodeRole = peers.Worker
	} else {
		peers.SetControlPlaneLabelType(&node)
		if _, isMaster := node.Labels[peers.GetUsedControlPlaneLabel()]; isMaster {
			manager.nodeRole = peers.Master
		}
	}
}

func (manager *Manager) isEndpointAccessLost() bool {
	if !manager.wasEndpointAccessibleAtStart {
		return false
	}
	return !manager.isEndpointAccessible()
}

func (manager *Manager) isEndpointAccessible() bool {
	pinger, err := ping.NewPinger(endpointToAccess)
	if err != nil {
		return false
	}
	pinger.Count = 3
	pinger.Timeout = time.Second * 5

	if err := pinger.Run(); err != nil {
		return false
	}
	return true
}

func (manager *Manager) isKubeletServiceRunning() bool {
	url := fmt.Sprintf("https://%s:%s/pods", manager.nodeName, kubeletPort)
	cmd := exec.Command("curl", "-k", "-X", "GET", url)
	if err := cmd.Run(); err != nil {
		manager.log.Error(err, "kubelet service is down", "node name", manager.nodeName)

		return false
	}

	return true
}

func (manager *Manager) isEtcdRunning() bool {
	//TODO mshitrit implement
	return true
}
