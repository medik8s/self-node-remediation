package cleanup

import (
	"context"
	"github.com/go-logr/logr"
	selfnoderemediationv1alpha1 "github.com/medik8s/self-node-remediation/api/v1alpha1"
	"github.com/medik8s/self-node-remediation/pkg/utils"
	"github.com/pkg/errors"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

type templateCleaner struct {
	client client.Client
	log    logr.Logger
}

func New(c client.Client) *templateCleaner {
	return &templateCleaner{
		client: c,
		log:    ctrl.Log.WithName("setup").WithName("templateCleaner"),
	}
}

func (t *templateCleaner) Start(ctx context.Context) error {
	return t.verifyDeprecatedTemplatesAreDeleted()
}

func (t *templateCleaner) verifyDeprecatedTemplatesAreDeleted() error {
	ns, err := utils.GetDeploymentNamespace()
	if err != nil {
		return errors.Wrap(err, "unable to get the deployment namespace")
	}
	nodeDeletionTemplate := &selfnoderemediationv1alpha1.SelfNodeRemediationTemplate{}
	key := client.ObjectKey{
		Name:      selfnoderemediationv1alpha1.DeprecatedNodeDeletionTemplateName,
		Namespace: ns,
	}
	if err := t.client.Get(context.Background(), key, nodeDeletionTemplate); err != nil {
		if apierrors.IsNotFound(err) {
			return nil
		}
		t.log.Error(err, "failed to fetch self node remediation template CR")
		return errors.Wrap(err, "failed to fetch self node remediation template CR")
	} else {
		if err := t.client.Delete(context.Background(), nodeDeletionTemplate); err != nil {
			t.log.Error(err, "failed to delete deprecated node deletion self node remediation template CR")
			return errors.Wrap(err, "failed to delete deprecated node deletion self node remediation template CR")
		}
		return nil
	}
}
