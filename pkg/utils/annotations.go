package utils

import (
	"context"
	"github.com/pkg/errors"
	v1 "k8s.io/api/core/v1"
	"os"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"strconv"
)

const (
	// IsRebootCapableAnnotation value is the key name for the node's annotation that will determine if node is reboot capable
	IsRebootCapableAnnotation = "is-reboot-capable.self-node.medik8s.io"
)

// UpdateNodeWithIsRebootCapableAnnotation updates the is-reboot-capable node annotation to be true if any kind
// of reboot is enabled and false if there isn't watchdog and software reboot is disabled
func UpdateNodeWithIsRebootCapableAnnotation(watchdogInitiated bool, nodeName string, mgr manager.Manager) error {
	node := &v1.Node{}
	key := client.ObjectKey{
		Name: nodeName,
	}

	if err := mgr.GetAPIReader().Get(context.Background(), key, node); err != nil {
		return errors.Wrapf(err, "failed to retrieve my node: "+nodeName)
	}

	softwareRebootEnabledEnv := os.Getenv("IS_SOFTWARE_REBOOT_ENABLED")
	softwareRebootEnabled, err := strconv.ParseBool(softwareRebootEnabledEnv)
	if err != nil {
		return errors.Wrapf(err, "failed to convert IS_SOFTWARE_REBOOT_ENABLED env valueto boolean. value is: %s", softwareRebootEnabledEnv)
	}

	if node.Annotations == nil {
		node.Annotations = map[string]string{}
	}

	if watchdogInitiated || softwareRebootEnabled {
		node.Annotations[IsRebootCapableAnnotation] = "true"
	} else {
		node.Annotations[IsRebootCapableAnnotation] = "false"
	}

	if err := mgr.GetClient().Update(context.Background(), node); err != nil {
		return errors.Wrapf(err, "failed to add node annotation to node: "+node.Name)
	}

	return nil
}
