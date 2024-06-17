package utils

import (
	"context"
	"math"
	"os"
	"strconv"
	"time"

	"github.com/pkg/errors"

	v1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/manager"
)

const (
	// IsRebootCapableAnnotation value is the key name for the node's annotation that will determine if node is reboot capable
	IsRebootCapableAnnotation = "is-reboot-capable.self-node-remediation.medik8s.io"
	// WatchdogTimeoutSecondsAnnotation value is the key name for the node's annotation that will hold the watchdog timeout in seconds
	WatchdogTimeoutSecondsAnnotation = "self-node-remediation.medik8s.io/watchdog-timeout"
	IsSoftwareRebootEnabledEnvVar    = "IS_SOFTWARE_REBOOT_ENABLED"
)

// UpdateNodeAnnotations updates the is-reboot-capable and watchdog timeout node annotations
func UpdateNodeAnnotations(watchdogInitiated bool, watchdogTimeout time.Duration, nodeName string, mgr manager.Manager) error {
	node := &v1.Node{}
	key := client.ObjectKey{
		Name: nodeName,
	}

	if err := mgr.GetAPIReader().Get(context.Background(), key, node); err != nil {
		return errors.Wrapf(err, "failed to retrieve my node: "+nodeName)
	}

	// the node is reboot capable if either watchdog was initialized or software reboot is enabled
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

	// Set the watchdog timeout, will be used by manager for safe time to reboot calculation.
	// When no watchdog was initialized it will be 0.
	// Always round up in case we have fractions of seconds.
	intTimeout := int(math.Ceil(watchdogTimeout.Seconds()))
	node.Annotations[WatchdogTimeoutSecondsAnnotation] = strconv.Itoa(intTimeout)

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

func GetWatchdogTimeout(node *v1.Node) (time.Duration, error) {
	if node.Annotations == nil {
		return 0, errors.New("node has no annotations")
	}

	timeout, err := strconv.Atoi(node.Annotations[WatchdogTimeoutSecondsAnnotation])
	if err != nil {
		return 0, errors.Wrapf(err, "failed to convert watchdog timeout to int. value is: %s", node.Annotations[WatchdogTimeoutSecondsAnnotation])
	}

	return time.Duration(timeout) * time.Second, nil
}
