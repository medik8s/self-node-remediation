package master

import (
	"context"
	"errors"
	"fmt"
	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

type role int8

const (
	worker role = iota
	master
)

const (
	masterLabelKey = "node-role.kubernetes.io/master"
	workerLabelKey = "node-role.kubernetes.io/worker"
	initErrorText  = "error initializing master handler"
)

var (
	initError = errors.New(initErrorText)
)

//Manager contains logic and info needed to fence and remediate master nodes
type Manager struct {
	nodeName            string
	nodeRole            role
	nodeNameRoleMapping map[string]role
	client              client.Client
	log                 logr.Logger
}

//NewManager inits a new Manager return nil if init fails
func NewManager(nodeName string, myClient client.Client) *Manager {
	return &Manager{
		nodeName:            nodeName,
		nodeNameRoleMapping: map[string]role{},
		client:              myClient,
		log:                 ctrl.Log.WithName("master").WithName("Manager"),
	}
}

func wrapWithInitError(err error) error {
	return fmt.Errorf(initErrorText+" [%w]", err)
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

func (manager *Manager) initializeManager() error {
	nodesList := &corev1.NodeList{}
	if err := manager.client.List(context.TODO(), nodesList, &client.ListOptions{}); err != nil {
		manager.log.Error(err, "could not retrieve nodes")
		return wrapWithInitError(err)
	}

	for _, node := range nodesList.Items {
		isNodeRoleFound := false
		for labelKey := range node.Labels {
			if labelKey == masterLabelKey {
				manager.nodeNameRoleMapping[node.Name] = master
				isNodeRoleFound = true
				break
			} else if labelKey == workerLabelKey {
				manager.nodeNameRoleMapping[node.Name] = worker
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
