package master

import (
	"context"
	"errors"
	"fmt"
	"github.com/go-logr/logr"
	"github.com/medik8s/self-node-remediation/pkg/peers"
	corev1 "k8s.io/api/core/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	initErrorText = "error initializing master handler"
)

var (
	initError    = errors.New(initErrorText)
	processError = errors.New("an error occurred during master remediation process")
)

//Manager contains logic and info needed to fence and remediate master nodes
type Manager struct {
	nodeName            string
	nodeRole            peers.Role
	nodeNameRoleMapping map[string]peers.Role
	client              client.Client
	log                 logr.Logger
}

//NewManager inits a new Manager return nil if init fails
func NewManager(nodeName string, myClient client.Client) *Manager {
	return &Manager{
		nodeName:            nodeName,
		nodeNameRoleMapping: map[string]peers.Role{},
		client:              myClient,
		log:                 ctrl.Log.WithName("master").WithName("Manager"),
	}
}

func (manager *Manager) Start(ctx context.Context) error {
	if err := manager.initializeManager(); err != nil {
		return err
	}
	//TODO mshitrit remove later, only for debug
	manager.log.Info("[DEBUG] current node role is:", "role", manager.nodeRole)
	manager.log.Info("[DEBUG] node name -> role mapping: ", "mapping", manager.nodeNameRoleMapping)
	return nil
}

func (manager *Manager) IsMaster() bool {
	return manager.nodeRole == peers.Master
}

func (manager *Manager) IsMasterHealthy(workerPeerResponse peers.Response) bool {
	switch workerPeerResponse.Reason {
	//reported unhealthy by worker peers
	case peers.UnHealthyBecauseCRFound:
		return false
	case peers.UnHealthyBecauseNodeIsIsolated:
		//TODO mshitrit implement
		return false
	//reported healthy by worker peers
	case peers.HealthyBecauseErrorsThresholdNotReached, peers.HealthyBecauseCRNotFound:
		return true
	//master node has connection to most workers, we assume it's not isolated (or at least that the master node that does not have worker peers quorum will reboot)
	case peers.HealthyBecauseMostPeersCantAccessAPIServer:
		//TODO mshitrit error is ignored
		isHealthy, _ := manager.IsNoMasterHealthIssuesFound()
		return isHealthy
	case peers.HealthyBecauseNoPeersWereFound:
		if isHealthy, _ := manager.IsNoMasterHealthIssuesFound(); !isHealthy {
			return false
		}
		//TODO mshitrit implement in case can't connect other masters return false
		return false

	default:
		manager.log.Error(processError, "node is considered unhealthy by worker peers for an unknown reason", "reason", workerPeerResponse.Reason, "node name", manager.nodeName)
		return false
	}

}

func (manager *Manager) IsNoMasterHealthIssuesFound() (bool, error) {
	//TODO mshitrit implement
	return true, nil
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

	for _, node := range nodesList.Items {
		isNodeRoleFound := false
		for labelKey := range node.Labels {
			if labelKey == peers.MasterLabelName {
				manager.nodeNameRoleMapping[node.Name] = peers.Master
				isNodeRoleFound = true
				break
			} else if labelKey == peers.WorkerLabelName {
				manager.nodeNameRoleMapping[node.Name] = peers.Worker
				isNodeRoleFound = true
				break
			}
		}
		if !isNodeRoleFound {
			manager.log.Error(initError, "could not find role for node", "node name", node.Name)
			return initError
		}
	}

	if _, isFound := manager.nodeNameRoleMapping[manager.nodeName]; !isFound {
		manager.log.Error(initError, "could not find role for current node", "node name", manager.nodeName)
		return initError
	} else {
		manager.nodeRole = manager.nodeNameRoleMapping[manager.nodeName]
		return nil
	}

}
