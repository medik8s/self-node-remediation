package shared

import (
	"context"
	"errors"
	"net"
	"time"

	"github.com/google/uuid"
	. "github.com/onsi/gomega"
	"github.com/onsi/gomega/gcustom"
	types2 "github.com/onsi/gomega/types"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	selfNodeRemediation "github.com/medik8s/self-node-remediation/api"
	selfnoderemediationv1alpha1 "github.com/medik8s/self-node-remediation/api/v1alpha1"
	"github.com/medik8s/self-node-remediation/pkg/apicheck"
	"github.com/medik8s/self-node-remediation/pkg/controlplane"
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
	Peer2NodeName            = "node3"
	Peer3NodeName            = "node4"

	SnrPodName1      = "self-node-remediation"
	SnrPodName2      = "self-node-remediation-2"
	SnrPodName3      = "self-node-remediation-3"
	DsDummyImageName = "dummy-image"

	K8sClientReturnRandomPodIPAddressesByDefault = false

	MinPeersForRemediationConfigDefaultValue = 1
)

type K8sClientWrapper struct {
	client.Client
	Reader                         client.Reader
	ShouldSimulateFailure          bool
	ShouldSimulatePodDeleteFailure bool
	SimulatedFailureMessage        string
	ShouldReturnRandomPodIPs       bool
}

type ApiConnectivityCheckWrapper struct {
	apicheck.ApiConnectivityCheck
	ShouldSimulatePeerResponses bool

	// store responses that we should override for any peer responses
	SimulatePeerResponses []selfNodeRemediation.HealthCheckResponseCode
}

func (kcw *K8sClientWrapper) List(ctx context.Context, list client.ObjectList, opts ...client.ListOption) (err error) {
	switch {
	case kcw.ShouldSimulateFailure:
		err = errors.New("simulation of client error")
		return
	case kcw.ShouldSimulatePodDeleteFailure:
		if _, ok := list.(*corev1.NamespaceList); ok {
			err = errors.New(kcw.SimulatedFailureMessage)
			return
		}
		fallthrough
	default:
		err = kcw.Client.List(ctx, list, opts...)
	}

	if kcw.ShouldReturnRandomPodIPs {
		logf.Log.Info("Returning random IP addresses for all the pods because ShouldReturnRandomPodIPs is true")

		if podList, ok := list.(*corev1.PodList); ok {
			assignRandomIpAddressesPods(podList)
		}
	}

	return
}

func assignRandomIpAddressesPods(pods *corev1.PodList) {
	for i := range pods.Items {
		pods.Items[i].Status.PodIPs = []corev1.PodIP{{IP: GetRandomIpAddress()}}
	}

	return
}

func GetRandomIpAddress() (randomIP string) {
	u := uuid.New()
	ip := net.IP(u[:net.IPv6len])
	randomIP = ip.String()

	return
}

func NewApiConnectivityCheckWrapper(ck *apicheck.ApiConnectivityCheckConfig, controlPlaneManager *controlplane.Manager) (ckw *ApiConnectivityCheckWrapper) {
	ckw = &ApiConnectivityCheckWrapper{
		ApiConnectivityCheck:        *apicheck.New(ck, controlPlaneManager),
		ShouldSimulatePeerResponses: false,
		SimulatePeerResponses:       []selfNodeRemediation.HealthCheckResponseCode{},
	}

	ckw.ApiConnectivityCheck.SetHealthStatusFunc(func(endpointIp corev1.PodIP, results chan<- selfNodeRemediation.HealthCheckResponseCode) {
		switch {
		case ckw.ShouldSimulatePeerResponses:
			for _, code := range ckw.SimulatePeerResponses {
				results <- code
			}

			return
		default:
			ckw.ApiConnectivityCheck.GetDefaultPeerHealthCheckFunc()(endpointIp, results)
			break
		}
	})

	return
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
		Spec: selfnoderemediationv1alpha1.SelfNodeRemediationConfigSpec{MinPeersForRemediation: 1},
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

type K8sErrorTestingFunc func(err error) bool

// Matches one of the k8s error types that the user wants to ignore
func IsIgnoredK8sError(k8sErrorsToIgnore []K8sErrorTestingFunc) types2.GomegaMatcher {
	return gcustom.MakeMatcher(func(errorToTest error) (matches bool, err error) {
		if errorToTest == nil {
			matches = false
			return
		}

		for _, testingFunc := range k8sErrorsToIgnore {
			if testingFunc(errorToTest) {
				matches = true
				return
			}
		}

		matches = false

		return
	})
}

func IsK8sNotFoundError() types2.GomegaMatcher {
	return gcustom.MakeMatcher(func(errorToTest error) (matches bool, err error) {
		if errorToTest == nil {
			matches = false
			return
		}

		if apierrors.IsNotFound(errorToTest) {
			matches = true
			return
		}

		matches = false

		return
	})
}
