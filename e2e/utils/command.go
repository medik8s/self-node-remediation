package utils

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/remotecommand"
	"k8s.io/utils/pointer"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/config"
)

const (
	// this is time need to execute a command on the node, including potentially pod creation time
	nodeExecTimeout = 300 * time.Second

	// timeout for waiting for pod ready
	podReadyTimeout = 120 * time.Second

	// additional timeout (after podDeletedTimeout) when the node should be rebooted
	nodeRebootedTimeout = 10 * time.Minute
)

var (
	log = ctrl.Log.WithName("testutils")
)

// GetBootID returns the boot ID of the node from the Kubernetes Node API.
// Boot ID is a kernel-generated UUID that changes on every reboot.
func GetBootID(ctx context.Context, c *kubernetes.Clientset, node *corev1.Node) string {
	var bootID string
	EventuallyWithOffset(1, func() error {
		n, err := c.CoreV1().Nodes().Get(ctx, node.GetName(), metav1.GetOptions{})
		if err != nil {
			return err
		}
		bootID = n.Status.NodeInfo.BootID
		if bootID == "" {
			return fmt.Errorf("boot ID is empty for node %s", node.GetName())
		}
		return nil
	}, 1*time.Minute, 5*time.Second).ShouldNot(HaveOccurred(), "Could not get boot ID on node %s", node.GetName())
	return bootID
}

func CheckReboot(ctx context.Context, c *kubernetes.Clientset, node *corev1.Node, oldBootID string) {
	By("checking reboot")
	log.Info("boot ID", "old", oldBootID)
	EventuallyWithOffset(1, func() string {
		n, err := c.CoreV1().Nodes().Get(ctx, node.GetName(), metav1.GetOptions{})
		if err != nil {
			log.Info("failed to get node for boot ID, will retry", "error", err)
			return oldBootID
		}
		newBootID := n.Status.NodeInfo.BootID
		log.Info("boot ID", "new", newBootID)
		return newBootID
	}, nodeRebootedTimeout, 10*time.Second).ShouldNot(Equal(oldBootID))
}

func CheckNoReboot(ctx context.Context, c *kubernetes.Clientset, node *corev1.Node, oldBootID string) {
	By("checking no reboot")
	log.Info("boot ID", "old", oldBootID)
	ConsistentlyWithOffset(1, func() string {
		n, err := c.CoreV1().Nodes().Get(ctx, node.GetName(), metav1.GetOptions{})
		if err != nil {
			log.Error(err, "failed to get node for boot ID")
			return oldBootID
		}
		newBootID := n.Status.NodeInfo.BootID
		log.Info("boot ID", "new", newBootID)
		return newBootID
	}, nodeRebootedTimeout, 1*time.Minute).Should(Equal(oldBootID))
}

// RunCommandInCluster runs a command in a new pod in the cluster and returns the output
func RunCommandInCluster(ctx context.Context, c *kubernetes.Clientset, nodeName string, ns string, command string) (string, error) {

	// create a pod and wait that it's running
	pod := getPod(nodeName)
	pod, err := c.CoreV1().Pods(ns).Create(ctx, pod, metav1.CreateOptions{})
	if err != nil {
		return "", err
	}

	err = waitForCondition(ctx, c, pod, corev1.PodReady, corev1.ConditionTrue, podReadyTimeout)
	if err != nil {
		return "", err
	}

	log.Info("helper pod is running, going to execute command")
	return RunCommandInPod(ctx, c, pod, command)
}

// RunCommandInPod runs a command in a given pod and returns the output
func RunCommandInPod(ctx context.Context, c *kubernetes.Clientset, pod *corev1.Pod, command string) (string, error) {
	cmd := []string{"sh", "-c", command}
	bytes, err := execCommandOnPod(ctx, c, pod, cmd)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(bytes)), nil
}

// execCommandOnPod runs command in the pod and returns buffer output
func execCommandOnPod(ctx context.Context, c *kubernetes.Clientset, pod *corev1.Pod, command []string) ([]byte, error) {
	var outputBuf bytes.Buffer
	var errorBuf bytes.Buffer

	req := c.CoreV1().RESTClient().
		Post().
		Namespace(pod.Namespace).
		Resource("pods").
		Name(pod.Name).
		SubResource("exec").
		VersionedParams(&corev1.PodExecOptions{
			Container: pod.Spec.Containers[0].Name,
			Command:   command,
			Stdin:     true,
			Stdout:    true,
			Stderr:    true,
			TTY:       true,
		}, scheme.ParameterCodec)

	cfg, err := config.GetConfig()
	if err != nil {
		return nil, err
	}

	exec, err := remotecommand.NewSPDYExecutor(cfg, "POST", req.URL())
	if err != nil {
		return nil, err
	}

	err = exec.StreamWithContext(ctx, remotecommand.StreamOptions{
		Stdin:  os.Stdin,
		Stdout: &outputBuf,
		Stderr: &errorBuf,
		Tty:    true,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to run command %v: error: %v, outputStream %s; errorStream %s", command, err, outputBuf.String(), errorBuf.String())
	}

	if errorBuf.Len() != 0 {
		return nil, fmt.Errorf("failed to run command %v: output %s; error %s", command, outputBuf.String(), errorBuf.String())
	}

	return outputBuf.Bytes(), nil
}

// waitForCondition waits until the pod will have specified condition type with the expected status
func waitForCondition(ctx context.Context, c *kubernetes.Clientset, pod *corev1.Pod, conditionType corev1.PodConditionType, conditionStatus corev1.ConditionStatus, timeout time.Duration) error {
	return wait.PollImmediateWithContext(ctx, time.Second, timeout, func(ctx context.Context) (bool, error) {
		updatedPod := &corev1.Pod{}
		var err error
		if updatedPod, err = c.CoreV1().Pods(pod.Namespace).Get(ctx, pod.Name, metav1.GetOptions{}); err != nil {
			return false, nil
		}
		for _, c := range updatedPod.Status.Conditions {
			if c.Type == conditionType && c.Status == conditionStatus {
				return true, nil
			}
		}
		return false, nil
	})
}

func getPod(nodeName string) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			GenerateName: "nhc-test-",
			Labels: map[string]string{
				"test": "",
			},
		},
		Spec: corev1.PodSpec{
			NodeName:    nodeName,
			HostNetwork: true,
			HostPID:     true,
			SecurityContext: &corev1.PodSecurityContext{
				RunAsUser:  pointer.Int64(0),
				RunAsGroup: pointer.Int64(0),
			},
			RestartPolicy: corev1.RestartPolicyNever,
			Containers: []corev1.Container{
				{
					Name:  "test",
					Image: "registry.access.redhat.com/ubi9/ubi:latest",
					SecurityContext: &corev1.SecurityContext{
						Privileged: pointer.Bool(true),
					},
					Command: []string{"sleep", "10m"},
				},
			},
			Tolerations: []corev1.Toleration{
				{
					Effect:   corev1.TaintEffectNoExecute,
					Operator: corev1.TolerationOpExists,
				},
				{
					Effect:   corev1.TaintEffectNoSchedule,
					Operator: corev1.TolerationOpExists,
				},
			},
			TerminationGracePeriodSeconds: pointer.Int64(600),
		},
	}
}
