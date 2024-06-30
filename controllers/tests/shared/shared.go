package shared

import (
	"context"
	"errors"
	"time"

	. "github.com/onsi/gomega"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	selfnoderemediationv1alpha1 "github.com/medik8s/self-node-remediation/api/v1alpha1"
	"github.com/medik8s/self-node-remediation/pkg/reboot"
)

const (
	PeerUpdateInterval = 30 * time.Second
	ApiCheckInterval   = 1 * time.Second
	MaxErrorThreshold  = 1
	// CalculatedRebootDuration is the mock calculator's result
	CalculatedRebootDuration = 3 * time.Second
	Namespace                = "self-node-remediation"
	UnhealthyNodeName        = "node1"
	PeerNodeName             = "node2"
	DsDummyImageName         = "dummy-image"
)

type K8sClientWrapper struct {
	client.Client
	Reader                         client.Reader
	ShouldSimulateFailure          bool
	ShouldSimulatePodDeleteFailure bool
	SimulatedFailureMessage        string
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

func GenerateTestConfig() *selfnoderemediationv1alpha1.SelfNodeRemediationConfig {
	return &selfnoderemediationv1alpha1.SelfNodeRemediationConfig{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "self-node-remediation.medik8s.io/v1alpha1",
			Kind:       "SelfNodeRemediationConfig",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      selfnoderemediationv1alpha1.ConfigCRName,
			Namespace: Namespace,
		},
		Spec: selfnoderemediationv1alpha1.SelfNodeRemediationConfigSpec{},
	}
}

var _ reboot.Calculator = &MockRebootDurationCalculator{}

type MockRebootDurationCalculator struct{}

func (m MockRebootDurationCalculator) GetRebootDuration(_ context.Context, _ *corev1.Node) (time.Duration, error) {
	return CalculatedRebootDuration, nil
}

func (m MockRebootDurationCalculator) SetConfig(_ *selfnoderemediationv1alpha1.SelfNodeRemediationConfig) {
	// no-op
}

func VerifySNRStatusExist(k8sClient client.Client, snr *selfnoderemediationv1alpha1.SelfNodeRemediation, statusType string, conditionStatus metav1.ConditionStatus) {
	Eventually(func(g Gomega) {
		tmpSNR := &selfnoderemediationv1alpha1.SelfNodeRemediation{}
		g.Expect(k8sClient.Get(context.Background(), client.ObjectKeyFromObject(snr), tmpSNR)).To(Succeed())
		g.Expect(meta.IsStatusConditionPresentAndEqual(tmpSNR.Status.Conditions, statusType, conditionStatus)).To(BeTrue())
	}, 10*time.Second, 250*time.Millisecond).Should(Succeed())
}
