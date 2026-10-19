package utils

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/remotecommand"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/config"
)

const (
	// additional timeout (after podDeletedTimeout) when the node should be rebooted
	nodeRebootedTimeout = 10 * time.Minute

	rebootCheckEnvVar             = "E2E_REBOOT_CHECK"
	rebootCheckBootID             = "boot-id"
	rebootCheckContainerStartTime = "container-start-time"
)

var (
	log = ctrl.Log.WithName("testutils")
)

func rebootCheckMode() string {
	mode := strings.ToLower(strings.TrimSpace(os.Getenv(rebootCheckEnvVar)))
	if mode == "" {
		return rebootCheckBootID
	}
	return mode
}

func containerTool() string {
	tool := os.Getenv("CONTAINER_TOOL")
	if tool == "" || tool == "podman-machine" {
		return "podman"
	}
	return tool
}

func getNodeBootID(ctx context.Context, c *kubernetes.Clientset, node *corev1.Node) (string, error) {
	n, err := c.CoreV1().Nodes().Get(ctx, node.GetName(), metav1.GetOptions{})
	if err != nil {
		return "", err
	}
	if n.Status.NodeInfo.BootID == "" {
		return "", fmt.Errorf("boot ID is empty for node %s", node.GetName())
	}
	return n.Status.NodeInfo.BootID, nil
}

func getContainerStartTime(ctx context.Context, node *corev1.Node) (string, error) {
	output, err := exec.CommandContext(ctx, containerTool(), "inspect", node.GetName(),
		"--format", "{{.State.StartedAt}}").Output()
	if err != nil {
		return "", fmt.Errorf("inspect container for node %s: %w", node.GetName(), err)
	}
	startTime := strings.TrimSpace(string(output))
	if startTime == "" {
		return "", fmt.Errorf("container start time is empty for node %s", node.GetName())
	}
	return startTime, nil
}

func getRebootMarker(ctx context.Context, c *kubernetes.Clientset, node *corev1.Node) (string, error) {
	switch rebootCheckMode() {
	case rebootCheckBootID:
		return getNodeBootID(ctx, c, node)
	case rebootCheckContainerStartTime:
		return getContainerStartTime(ctx, node)
	default:
		return "", fmt.Errorf("unsupported %s value %q; use %q or %q",
			rebootCheckEnvVar, rebootCheckMode(), rebootCheckBootID, rebootCheckContainerStartTime)
	}
}

// GetBootID returns the boot ID of the node from the Kubernetes Node API.
// Boot ID is a kernel-generated UUID that changes on every kernel reboot.
func GetBootID(ctx context.Context, c *kubernetes.Clientset, node *corev1.Node) string {
	var bootID string
	EventuallyWithOffset(1, func() error {
		var err error
		bootID, err = getNodeBootID(ctx, c, node)
		return err
	}, 1*time.Minute, 5*time.Second).ShouldNot(HaveOccurred(), "Could not get boot ID on node %s", node.GetName())
	return bootID
}

// GetRebootMarker returns the value used to detect a reboot. Real nodes use
// boot ID; Kind nodes can use the container start time because restarting a
// Kind node container does not reboot the shared host kernel.
func GetRebootMarker(ctx context.Context, c *kubernetes.Clientset, node *corev1.Node) string {
	var marker string
	EventuallyWithOffset(1, func() error {
		var err error
		marker, err = getRebootMarker(ctx, c, node)
		return err
	}, 1*time.Minute, 5*time.Second).ShouldNot(HaveOccurred(), "Could not get reboot marker for node %s", node.GetName())
	return marker
}

func CheckReboot(ctx context.Context, c *kubernetes.Clientset, node *corev1.Node, oldMarker string) {
	By("checking reboot")
	log.Info("reboot marker", "node", node.GetName(), "mode", rebootCheckMode(), "old", oldMarker)
	EventuallyWithOffset(1, func() string {
		newMarker, err := getRebootMarker(ctx, c, node)
		if err != nil {
			log.Info("failed to get reboot marker, will retry", "node", node.GetName(), "error", err)
			return oldMarker
		}
		if newMarker != oldMarker {
			log.Info("reboot marker changed", "node", node.GetName(), "mode", rebootCheckMode(), "old", oldMarker, "new", newMarker)
		} else {
			log.Info("reboot marker unchanged, waiting for reboot", "node", node.GetName(), "mode", rebootCheckMode(), "current", newMarker)
		}
		return newMarker
	}, nodeRebootedTimeout, 10*time.Second).ShouldNot(Equal(oldMarker))
}

func CheckNoReboot(ctx context.Context, c *kubernetes.Clientset, node *corev1.Node, oldMarker string) {
	By("checking no reboot")
	log.Info("reboot marker", "node", node.GetName(), "mode", rebootCheckMode(), "old", oldMarker)
	ConsistentlyWithOffset(1, func() string {
		newMarker, err := getRebootMarker(ctx, c, node)
		if err != nil {
			log.Error(err, "failed to get reboot marker", "node", node.GetName())
			return oldMarker
		}
		if newMarker != oldMarker {
			log.Info("reboot marker changed unexpectedly", "node", node.GetName(), "mode", rebootCheckMode(), "old", oldMarker, "new", newMarker)
		} else {
			log.Info("reboot marker unchanged", "node", node.GetName(), "mode", rebootCheckMode(), "current", newMarker)
		}
		return newMarker
	}, nodeRebootedTimeout, 1*time.Minute).Should(Equal(oldMarker))
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
