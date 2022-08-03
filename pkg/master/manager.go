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
	currentNodeRole     role
	nodeNameRoleMapping map[string]role
	client              client.Client
	log                 logr.Logger
}

//NewManager inits a new Manager return nil if init fails
func NewManager(nodeName string, myClient client.Client) (*Manager, error) {
	managerLog := ctrl.Log.WithName("master").WithName("Manager")
	nodesList := &corev1.NodeList{}
	if err := myClient.List(context.TODO(), nodesList, &client.ListOptions{}); err != nil {
		managerLog.Error(err, "could not retrieve nodes")
		return nil, wrapWithInitError(err)
	}

	nodeNameRoleMapping := map[string]role{}
	for _, node := range nodesList.Items {
		isNodeRoleFound := false
		for labelKey := range node.Labels {
			if labelKey == masterLabelKey {
				nodeNameRoleMapping[node.Name] = master
				isNodeRoleFound = true
				break
			} else if labelKey == workerLabelKey {
				nodeNameRoleMapping[node.Name] = worker
				isNodeRoleFound = true
				break
			}
		}
		if !isNodeRoleFound {
			managerLog.Error(initError, "could not find role for node", "node name", node.Name)
			return nil, initError
		}
	}

	if _, isFound := nodeNameRoleMapping[nodeName]; !isFound {
		managerLog.Error(initError, "could not find role for current node", "node name", nodeName)
		return nil, initError
	}
	return &Manager{
		currentNodeRole:     nodeNameRoleMapping[nodeName],
		nodeNameRoleMapping: nodeNameRoleMapping,
		client:              myClient,
		log:                 managerLog,
	}, nil
}

func wrapWithInitError(err error) error {
	return fmt.Errorf(initErrorText+" [%w]", err)
}

//TODO mshitrit remove later, only for debug
func (manager *Manager) Start(ctx context.Context) error {
	manager.log.Info("[DEBUG] current node role is:", "role", manager.currentNodeRole)
	manager.log.Info("[DEBUG] node name -> role mapping: ", "mapping", manager.nodeNameRoleMapping)
	return nil
}
