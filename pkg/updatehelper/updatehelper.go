package updatehelper

import (
	"context"

	"github.com/go-logr/logr"
	"github.com/pkg/errors"

	appsv1 "k8s.io/api/apps/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/medik8s/self-node-remediation/pkg/utils"
)

const dsName = "self-node-remediation-ds"

type UpdateInitializer struct {
	client client.Client
	log    logr.Logger
}

func New(c client.Client, log logr.Logger) *UpdateInitializer {
	return &UpdateInitializer{
		client: c,
		log:    log,
	}
}

func (ui *UpdateInitializer) Start(ctx context.Context) error {
	ns, err := utils.GetDeploymentNamespace()
	if err != nil {
		return errors.Wrap(err, "unable to get the deployment namespace")
	}
	ds := &appsv1.DaemonSet{}
	key := types.NamespacedName{
		Namespace: ns,
		Name:      dsName,
	}
	if err := ui.client.Get(ctx, key, ds); err == nil {
		if err = ui.client.Delete(ctx, ds); err != nil {
			ui.log.Error(err, "snr update failed could not delete old damenoset")
			return errors.Wrap(err, "unable to delete old daemon set")
		}
		ui.log.Info("snr update old daemonset deleted")
		return nil

	} else if !apierrors.IsNotFound(err) {
		ui.log.Error(err, "snr install/update failed error when trying to fetch old damenoset")
		return errors.Wrap(err, "unable to fetch daemon set")
	}

	ui.log.Info("snr didn't find old daemonset to be deleted")
	return nil
}
