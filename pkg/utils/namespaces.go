package utils

import (
	"context"
	"fmt"
	"os"

	"github.com/go-logr/logr"

	"k8s.io/api/core/v1"

	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	//psa labels
	psaPrivileges = "pod-security.kubernetes.io/enforce"
	psaLabelSync  = "security.openshift.io/scc.podSecurityLabelSync"
)

// GetDeploymentNamespace returns the Namespace this operator is deployed on.
func GetDeploymentNamespace() (string, error) {
	// deployNamespaceEnvVar is the constant for env variable DEPLOYMENT_NAMESPACE
	// which specifies the Namespace to watch.
	// An empty value means the operator is running with cluster scope.
	var deployNamespaceEnvVar = "DEPLOYMENT_NAMESPACE"

	ns, found := os.LookupEnv(deployNamespaceEnvVar)
	if !found {
		return "", fmt.Errorf("%s must be set", deployNamespaceEnvVar)
	}
	return ns, nil
}

type labelSetter struct {
	nsName    string
	k8sClient client.Client
	log       logr.Logger
}

func NewNsLabelSetter(nsName string, client client.Client, log logr.Logger) *labelSetter {
	return &labelSetter{k8sClient: client, nsName: nsName, log: log}
}

func (ls *labelSetter) Start(ctx context.Context) error {
	return ls.setPsaLabels()
}

func (ls *labelSetter) setPsaLabels() error {
	ns := &v1.Namespace{}
	if err := ls.k8sClient.Get(context.Background(), client.ObjectKey{Name: ls.nsName}, ns); err != nil {
		ls.log.Error(err, "failed to fetch namespace")
		return err
	}

	ns.Labels[psaPrivileges] = "privileged"
	ns.Labels[psaLabelSync] = "false"

	if err := ls.k8sClient.Update(context.Background(), ns); err != nil {
		ls.log.Error(err, "failed to update namespace with pod security access labels")
		return err
	}

	return nil
}
