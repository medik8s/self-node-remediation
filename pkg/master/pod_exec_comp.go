package master

import (
	"bytes"
	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/scheme"
	restclient "k8s.io/client-go/rest"
	"k8s.io/client-go/tools/remotecommand"
)

var (
	kClient         *kubernetes.Clientset
	k8sClientConfig *restclient.Config
)

// podCommandExecuter executes commands on container within the pod
type podCommandExecuter interface {
	execCmdOnPod(command []string, pod *corev1.Pod, containerName string) (stdout, stderr string, err error)
}

type basePodCommandExecuter struct {
	Log logr.Logger
}

// execCmdOnPod exec command on specific pod and wait the command's output.
func (r *basePodCommandExecuter) execCmdOnPod(command []string, pod *corev1.Pod, containerName string) (stdout, stderr string, err error) {
	if err := r.buildK8sClient(); err != nil {
		return "", "", err
	}

	var (
		stdoutBuf bytes.Buffer
		stderrBuf bytes.Buffer
	)

	req := kClient.CoreV1().RESTClient().Post().
		Resource("pods").
		Name(pod.Name).
		Namespace(pod.Namespace).
		SubResource("exec").
		Param("container", containerName)

	req.VersionedParams(&corev1.PodExecOptions{
		Container: containerName,
		Command:   command,
		Stdin:     false,
		Stdout:    true,
		Stderr:    true,
		TTY:       false,
	}, scheme.ParameterCodec)
	execSPDY, err := remotecommand.NewSPDYExecutor(k8sClientConfig, "POST", req.URL())
	if err != nil {
		r.Log.Error(err, "failed building SPDY (shell) executor")
		return "", "", err
	}
	err = execSPDY.Stream(remotecommand.StreamOptions{
		Stdout: &stdoutBuf,
		Stderr: &stderrBuf,
		Tty:    false,
	})
	if err != nil {
		r.Log.Error(err, "Failed to run exec command", "stdout", stdoutBuf.String(), "stderr", stderrBuf.String())
	}
	return stdoutBuf.String(), stderrBuf.String(), err
}

func (r *basePodCommandExecuter) buildK8sClient() error {
	//client was already built stop here
	if kClient != nil {
		return nil
	}

	if config, err := restclient.InClusterConfig(); err != nil {
		r.Log.Error(err, "failed getting cluster config")
		return err
	} else {
		k8sClientConfig = config

		if clientSet, err := kubernetes.NewForConfig(k8sClientConfig); err != nil {
			r.Log.Error(err, "failed building k8s client")
			return err
		} else {
			kClient = clientSet
		}
	}
	return nil
}
