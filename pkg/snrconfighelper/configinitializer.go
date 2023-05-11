package snrconfighelper

import (
	"context"

	"github.com/go-logr/logr"
	"github.com/pkg/errors"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"

	selfnoderemediationv1alpha1 "github.com/medik8s/self-node-remediation/api/v1alpha1"
	"github.com/medik8s/self-node-remediation/pkg/utils"
)

type ConfigInitializer struct {
	client client.Client
	log    logr.Logger
}

func New(c client.Client, log logr.Logger) *ConfigInitializer {
	return &ConfigInitializer{
		client: c,
		log:    log,
	}
}

func (configInitializer *ConfigInitializer) Start(ctx context.Context) error {
	return NewConfigIfNotExist(configInitializer.client)
}

// NewConfigIfNotExist creates a new SelfNodeRemediationConfig object
// to initialize the rest of the deployment objects creation.
func NewConfigIfNotExist(c client.Client) error {
	ns, err := utils.GetDeploymentNamespace()
	if err != nil {
		return errors.Wrap(err, "unable to get the deployment namespace")
	}

	config := selfnoderemediationv1alpha1.NewDefaultSelfNodeRemediationConfig()
	config.SetNamespace(ns)

	err = c.Create(context.Background(), &config, &client.CreateOptions{})
	if err != nil && !apierrors.IsAlreadyExists(err) {
		return errors.Wrap(err, "failed to create a default self node remediation config CR")
	}
	return nil
}
