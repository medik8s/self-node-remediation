package shared

import (
	"context"
	"errors"
	"time"

	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	PeerUpdateInterval = 30 * time.Second
	ApiCheckInterval   = 1 * time.Second
	MaxErrorThreshold  = 1
	Namespace          = "self-node-remediation"
	UnhealthyNodeName  = "node1"
	PeerNodeName       = "node2"
)

type K8sClientWrapper struct {
	client.Client
	Reader                         client.Reader
	ShouldSimulateFailure          bool
	ShouldSimulatePodDeleteFailure bool
	SimulatedFailureMessage        string
}

type MockCalculator struct {
	MockTimeToAssumeNodeRebooted time.Duration
	IsAgentVar                   bool
}

func (kcw *K8sClientWrapper) List(ctx context.Context, list client.ObjectList, opts ...client.ListOption) error {
	if kcw.ShouldSimulateFailure {
		return errors.New("simulation of client error")
	} else if kcw.ShouldSimulatePodDeleteFailure {
		if _, ok := list.(*corev1.NamespaceList); ok {
			return errors.New(kcw.SimulatedFailureMessage)
		}
	}
	return kcw.Client.List(ctx, list, opts...)
}

func (m *MockCalculator) GetTimeToAssumeNodeRebooted() (time.Duration, error) {
	return m.MockTimeToAssumeNodeRebooted, nil
}

func (m *MockCalculator) SetTimeToAssumeNodeRebooted(timeToAssumeNodeRebooted time.Duration) {
	m.MockTimeToAssumeNodeRebooted = timeToAssumeNodeRebooted
}

func (m *MockCalculator) IsAgent() bool {
	return m.IsAgentVar
}

//goland:noinspection GoUnusedParameter
func (m *MockCalculator) Start(ctx context.Context) error {
	return nil
}
