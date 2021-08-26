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
	isRebootCapableLabel = "is-reboot-capable"
)

// updateLabel updates the pod's label (key) to the given value
func updateLabel(labelKey string, labelValue bool, pod *v1.Pod, c client.Client) error {
	if pod.Labels == nil {
		pod.Labels = map[string]string{}
	}
	pod.Labels[labelKey] = strconv.FormatBool(labelValue)
	//update in yaml
	err := c.Update(context.Background(), pod)
	return err
}

// UpdatePodIsRebootCapableLabel updates the is-reboot-capable label to be true if any kind
// of reboot is enabled and false if there isn't watchdog and software reboot is disabled
func UpdatePodIsRebootCapableLabel(watchdogInitiated bool, nodeName string, mgr manager.Manager) error {
	//get pod in order to update its label
	pod, err := GetPoisonPillAgentPod(nodeName, mgr.GetAPIReader())
	if err != nil {
		return errors.Wrapf(err, "failed to list poison pill agent pods")
	}

	softwareRebootEnabledEnv := os.Getenv("IS_SOFTWARE_REBOOT_ENABLED")
	softwareRebootEnabled, err := strconv.ParseBool(softwareRebootEnabledEnv)
	if err != nil {
		return errors.Wrapf(err, "failed to convert IS_SOFTWARE_REBOOT_ENABLED env valueto boolean. value is: %s", softwareRebootEnabledEnv)
	}
	if watchdogInitiated || softwareRebootEnabled {
		err = updateLabel(isRebootCapableLabel, true, pod, mgr.GetClient())
		return err
	}

	err = updateLabel(isRebootCapableLabel, false, pod, mgr.GetClient())
	return err
}

func GetLabelValue(pod *v1.Pod, labelKey string) string {
	var labelVal string
	if pod.Labels == nil {
		labelVal = "pod.Labels is nil (label doesn't exist)"
		return labelVal
	}

	return pod.Labels[labelKey]
}