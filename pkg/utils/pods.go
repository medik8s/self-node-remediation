package utils

import (
	"context"
	"fmt"

	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/selection"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func GetSelfNodeRemediationAgentPod(nodeName string, r client.Reader) (*v1.Pod, error) {
	podList := &v1.PodList{}

	selector := labels.NewSelector()
	nameRequirement, _ := labels.NewRequirement("app.kubernetes.io/name", selection.Equals, []string{"self-node-remediation"})
	componentRequirement, _ := labels.NewRequirement("app.kubernetes.io/component", selection.Equals, []string{"agent"})
	selector = selector.Add(*nameRequirement, *componentRequirement)

	err := r.List(context.Background(), podList, &client.ListOptions{LabelSelector: selector})
	if err != nil {
		err = fmt.Errorf("failed to retrieve the self-node-remediation agent pod: %w", err)
		return nil, err
	}

	for _, pod := range podList.Items {
		if pod.Spec.NodeName == nodeName {
			return &pod, nil
		}
	}

	return nil, fmt.Errorf("failed to find self node remediation pod matching the given node (%s)", nodeName)
}
