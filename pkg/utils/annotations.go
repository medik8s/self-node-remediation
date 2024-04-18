package utils

import (
	"context"
	"os"
	"strconv"

	"github.com/pkg/errors"

	v1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/manager"
)

const (
	// IsRebootCapableAnnotation value is the key name for the node's annotation that will determine if node is reboot capable
	IsRebootCapableAnnotation     = "is-reboot-capable.self-node-remediation.medik8s.io"
	IsSoftwareRebootEnabledEnvVar = "IS_SOFTWARE_REBOOT_ENABLED"
	MinSafeTimeAnnotation         = "minimum-safe-reboot-sec.self-node-remediation.medik8s.io"
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

	var softwareRebootEnabled bool
	var err error
	if softwareRebootEnabled, err = IsSoftwareRebootEnabled(); err != nil {
		return err
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

func IsSoftwareRebootEnabled() (bool, error) {
	softwareRebootEnabledEnv := os.Getenv(IsSoftwareRebootEnabledEnvVar)
	softwareRebootEnabled, err := strconv.ParseBool(softwareRebootEnabledEnv)
	if err != nil {
		return false, errors.Wrapf(err, "failed to convert IS_SOFTWARE_REBOOT_ENABLED env value to boolean. value is: %s", softwareRebootEnabledEnv)
	}
	return softwareRebootEnabled, nil
}
