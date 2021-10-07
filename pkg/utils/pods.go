package utils

import (
	"context"
	"errors"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/selection"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func GetPoisonPillAgentPod(nodeName string, r client.Reader) (*v1.Pod, error) {
	podList := &v1.PodList{}

	selector := labels.NewSelector()
	requirement, _ := labels.NewRequirement("app", selection.Equals, []string{"poison-pill-agent"})
	selector = selector.Add(*requirement)

	err := r.List(context.Background(), podList, &client.ListOptions{LabelSelector: selector})
	if err != nil {
		return nil, err
	}

	for _, pod := range podList.Items {
		if pod.Spec.NodeName == nodeName {
			return &pod, nil
		}
	}

	return nil, errors.New("failed to find poison pill pod matching the given node")
}
