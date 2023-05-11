package utils

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strings"

	"github.com/go-logr/logr"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/apimachinery/pkg/labels"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
)

// this code is mostly from https://github.com/openshift-kni/performance-addon-operators/tree/master/functests/utils
// it uses the MachineConfigDaemon pods for running commands on a node
// so we don't need to create a new pod for this

const (
	// namespaceMachineConfigOperator contains the namespace of the machine-config-opereator
	namespaceMachineConfigOperator = "openshift-machine-config-operator"
	// containerMachineConfigDaemon contains the name of the machine-config-daemon container
	containerMachineConfigDaemon = "machine-config-daemon"
)

var logger logr.Logger

func init() {
	logger = logf.Log
}

// ExecCommandOnNode executes given command on given node and returns the result
func ExecCommandOnNode(c client.Client, cmd []string, node *corev1.Node, ctx context.Context) (string, error) {
	out, err := execCommandOnMachineConfigDaemon(c, node, cmd, ctx)
	if err != nil {
		return "", err
	}
	return strings.Trim(string(out), "\n"), nil
}

// execCommandOnMachineConfigDaemon returns the output of the command execution on the machine-config-daemon pod that runs on the specified node
func execCommandOnMachineConfigDaemon(c client.Client, node *corev1.Node, command []string, ctx context.Context) ([]byte, error) {
	mcd, err := getMachineConfigDaemonByNode(c, node)
	if err != nil {
		return nil, err
	}
	logger.Info("found mcd for node\n", "mcd name", mcd.Name, "node name", node.Name)

	initialArgs := []string{
		"exec",
		"-i",
		"-n", namespaceMachineConfigOperator,
		"-c", containerMachineConfigDaemon,
		"--request-timeout", "600",
		mcd.Name,
		"--",
	}
	initialArgs = append(initialArgs, command...)
	return execAndLogCommand(ctx, "oc", initialArgs...)
}

// getMachineConfigDaemonByNode returns the machine-config-daemon pod that runs on the specified node
func getMachineConfigDaemonByNode(c client.Client, node *corev1.Node) (*corev1.Pod, error) {

	listOptions := &client.ListOptions{
		Namespace:     namespaceMachineConfigOperator,
		FieldSelector: fields.SelectorFromSet(fields.Set{"spec.nodeName": node.Name}),
		LabelSelector: labels.SelectorFromSet(labels.Set{"k8s-app": "machine-config-daemon"}),
	}

	mcds := &corev1.PodList{}
	if err := c.List(context.Background(), mcds, listOptions); err != nil {
		return nil, err
	}

	if len(mcds.Items) < 1 {
		return nil, fmt.Errorf("failed to get machine-config-daemon pod for the node %q", node.Name)
	}
	return &mcds.Items[0], nil
}

func execAndLogCommand(ctx context.Context, name string, arg ...string) ([]byte, error) {
	outData, _, err := execAndLogCommandWithStderr(ctx, name, arg...)
	return outData, err
}

func execAndLogCommandWithStderr(ctx context.Context, name string, arg ...string) ([]byte, []byte, error) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, name, arg...)
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	outData := stdout.Bytes()
	errData := stderr.Bytes()

	logger.Info("run command\n", "command", name, "args", arg, "error", err, "stdout", string(outData), "stderr", string(errData))

	// We want to check the context error to see if the timeout was executed.
	if ctx.Err() == context.DeadlineExceeded {
		return nil, nil, fmt.Errorf("deadline exceeded")
	}
	return outData, errData, err
}
