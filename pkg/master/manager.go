package master

import (
	"context"
	"errors"
	"fmt"
	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
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

type manager struct {
	currentNodeRole     role
	nodeNameRoleMapping map[string]role
	client              client.Client
	log                 logr.Logger
}

func newManager(nodeName string, myClient client.Client, log logr.Logger) (*manager, error) {

	managerLog := log.WithName("master manager")
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
	return &manager{
		currentNodeRole:nodeNameRoleMapping[nodeName],
		nodeNameRoleMapping: nodeNameRoleMapping,
		client: myClient,
		log: managerLog,
	}, nil
}

func wrapWithInitError(err error) error {
	return fmt.Errorf(initErrorText+" [%w]", err)
}
