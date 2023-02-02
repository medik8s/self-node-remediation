package utils

import (
	"bytes"
	"context"
	"io"
	"time"

	. "github.com/onsi/gomega"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/kubernetes"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// GetLogs returns logs of the specified pod
func GetLogs(c *kubernetes.Clientset, pod *corev1.Pod) (string, error) {
	logStream, err := c.CoreV1().Pods(pod.Namespace).GetLogs(pod.Name, &corev1.PodLogOptions{}).Stream(context.Background())
	if err != nil {
		return "", err
	}
	defer logStream.Close()

	buf := new(bytes.Buffer)
	if _, err := io.Copy(buf, logStream); err != nil {
		return "", err
	}

	return buf.String(), nil
}

func WaitForPodReady(c client.Client, pod *corev1.Pod) {
	EventuallyWithOffset(1, func() corev1.ConditionStatus {
		ExpectWithOffset(1, c.Get(context.Background(), client.ObjectKeyFromObject(pod), pod)).ToNot(HaveOccurred())
		for _, cond := range pod.Status.Conditions {
			if cond.Type == corev1.PodReady {
				return cond.Status
			}
		}
		return corev1.ConditionUnknown
	}, 20*time.Minute, 10*time.Second).Should(Equal(corev1.ConditionTrue), "pod did not get ready in time")
}
