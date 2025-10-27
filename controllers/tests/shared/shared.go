package shared

import (
	"context"
	"crypto/rand"
	"errors"
	"net"
	"sync"
	"time"

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
	"github.com/medik8s/self-node-remediation/pkg/peers"
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
	*apicheck.ApiConnectivityCheck
	ShouldSimulatePeerResponses bool

	responsesMu              sync.Mutex
	simulatedPeerResponses   []selfNodeRemediation.HealthCheckResponseCode
	peersOverride            PeersOverrideFunc
	workerLastResponse       time.Time
	controlPlaneLastResponse time.Time
	baselineResponses        []selfNodeRemediation.HealthCheckResponseCode
}

// PeersOverrideFunc allows tests to supply synthetic peer address lists without
// altering the production wiring. Phase 0 only stores the function; upcoming
// phases decide how the ApiConnectivityCheck consumes it.
type PeersOverrideFunc func(role peers.Role) []corev1.PodIP

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
		randomIP := GetRandomIpAddress()
		pods.Items[i].Status.PodIP = randomIP
		pods.Items[i].Status.PodIPs = []corev1.PodIP{{IP: randomIP}}
	}

	return
}

func GetRandomIpAddress() (randomIP string) {
	// Generate a Unique Local Address (fd00::/8). This keeps addresses valid
	// while avoiding collisions with routable ranges.
	bytes := make([]byte, net.IPv6len)
	bytes[0] = 0xfd
	if _, err := rand.Read(bytes[1:]); err != nil {
		panic(err)
	}
	randomIP = net.IP(bytes).String()

	return
}

func NewApiConnectivityCheckWrapper(ck *apicheck.ApiConnectivityCheckConfig, controlPlaneManager *controlplane.Manager) (ckw *ApiConnectivityCheckWrapper) {
	inner := apicheck.New(ck, controlPlaneManager)
	ckw = &ApiConnectivityCheckWrapper{
		ApiConnectivityCheck:        inner,
		ShouldSimulatePeerResponses: false,
		simulatedPeerResponses:      []selfNodeRemediation.HealthCheckResponseCode{},
	}

	inner.SetHealthStatusFunc(func(endpointIp corev1.PodIP, results chan<- selfNodeRemediation.HealthCheckResponseCode) {
		if ckw.ShouldSimulatePeerResponses {
			// The caller expects exactly one response per peer; the helper enforces
			// that contract to keep the bounded channel writes non-blocking.
			resp := ckw.nextSimulatedPeerResponse()
			results <- resp
			return
		}

		ckw.GetDefaultPeerHealthCheckFunc()(endpointIp, results)
	})

	return
}

func (ckw *ApiConnectivityCheckWrapper) nextSimulatedPeerResponse() selfNodeRemediation.HealthCheckResponseCode {
	ckw.responsesMu.Lock()
	defer ckw.responsesMu.Unlock()

	if len(ckw.simulatedPeerResponses) == 0 {
		return selfNodeRemediation.RequestFailed
	}

	code := ckw.simulatedPeerResponses[0]
	if len(ckw.simulatedPeerResponses) > 1 {
		ckw.simulatedPeerResponses = append([]selfNodeRemediation.HealthCheckResponseCode{}, ckw.simulatedPeerResponses[1:]...)
	}

	return code
}

func (ckw *ApiConnectivityCheckWrapper) AppendSimulatedPeerResponse(code selfNodeRemediation.HealthCheckResponseCode) {
	ckw.responsesMu.Lock()
	defer ckw.responsesMu.Unlock()
	ckw.simulatedPeerResponses = append(ckw.simulatedPeerResponses, code)
}

func (ckw *ApiConnectivityCheckWrapper) ClearSimulatedPeerResponses() {
	ckw.responsesMu.Lock()
	defer ckw.responsesMu.Unlock()
	ckw.simulatedPeerResponses = nil
}

func (ckw *ApiConnectivityCheckWrapper) SnapshotSimulatedPeerResponses() []selfNodeRemediation.HealthCheckResponseCode {
	ckw.responsesMu.Lock()
	defer ckw.responsesMu.Unlock()
	if len(ckw.simulatedPeerResponses) == 0 {
		return nil
	}
	snapshot := make([]selfNodeRemediation.HealthCheckResponseCode, len(ckw.simulatedPeerResponses))
	copy(snapshot, ckw.simulatedPeerResponses)
	return snapshot
}

func (ckw *ApiConnectivityCheckWrapper) RestoreSimulatedPeerResponses(codes []selfNodeRemediation.HealthCheckResponseCode) {
	ckw.responsesMu.Lock()
	defer ckw.responsesMu.Unlock()
	if len(codes) == 0 {
		ckw.simulatedPeerResponses = nil
		return
	}
	ckw.simulatedPeerResponses = make([]selfNodeRemediation.HealthCheckResponseCode, len(codes))
	copy(ckw.simulatedPeerResponses, codes)
}

func (ckw *ApiConnectivityCheckWrapper) RememberSimulatedPeerResponses() {
	ckw.responsesMu.Lock()
	defer ckw.responsesMu.Unlock()
	if len(ckw.simulatedPeerResponses) == 0 {
		ckw.baselineResponses = nil
		return
	}
	ckw.baselineResponses = make([]selfNodeRemediation.HealthCheckResponseCode, len(ckw.simulatedPeerResponses))
	copy(ckw.baselineResponses, ckw.simulatedPeerResponses)
}

func (ckw *ApiConnectivityCheckWrapper) RestoreBaselineSimulatedResponses() {
	ckw.responsesMu.Lock()
	defer ckw.responsesMu.Unlock()
	if len(ckw.baselineResponses) == 0 {
		return
	}
	ckw.simulatedPeerResponses = make([]selfNodeRemediation.HealthCheckResponseCode, len(ckw.baselineResponses))
	copy(ckw.simulatedPeerResponses, ckw.baselineResponses)
}

func (ckw *ApiConnectivityCheckWrapper) ClearBaselineSimulatedResponses() {
	ckw.responsesMu.Lock()
	defer ckw.responsesMu.Unlock()
	ckw.baselineResponses = nil
}

// SetPeersOverride registers a custom provider for peer address lists.
func (ckw *ApiConnectivityCheckWrapper) SetPeersOverride(fn PeersOverrideFunc) {
	ckw.responsesMu.Lock()
	ckw.peersOverride = fn
	ckw.responsesMu.Unlock()

	var wrapped apicheck.PeersOverrideFunc
	if fn != nil {
		wrapped = func(role peers.Role) []corev1.PodIP {
			return fn(role)
		}
	}
	ckw.ApiConnectivityCheck.SetPeersOverride(wrapped)
}

// ClearPeersOverride removes any previously configured peer provider.
func (ckw *ApiConnectivityCheckWrapper) ClearPeersOverride() {
	ckw.SetPeersOverride(nil)
}

// PeersOverride exposes the currently configured override, if any.
func (ckw *ApiConnectivityCheckWrapper) PeersOverride() PeersOverrideFunc {
	ckw.responsesMu.Lock()
	defer ckw.responsesMu.Unlock()
	return ckw.peersOverride
}

// RecordWorkerPeerResponse updates the last time a worker-role peer responded.
func (ckw *ApiConnectivityCheckWrapper) RecordWorkerPeerResponse(t time.Time) {
	ckw.responsesMu.Lock()
	defer ckw.responsesMu.Unlock()
	ckw.workerLastResponse = t
}

// WorkerPeerLastResponse returns the timestamp captured via RecordWorkerPeerResponse.
func (ckw *ApiConnectivityCheckWrapper) WorkerPeerLastResponse() time.Time {
	ckw.responsesMu.Lock()
	defer ckw.responsesMu.Unlock()
	return ckw.workerLastResponse
}

// RecordControlPlanePeerResponse updates the last time a control-plane peer responded.
func (ckw *ApiConnectivityCheckWrapper) RecordControlPlanePeerResponse(t time.Time) {
	ckw.responsesMu.Lock()
	defer ckw.responsesMu.Unlock()
	ckw.controlPlaneLastResponse = t
}

// ControlPlanePeerLastResponse returns the timestamp captured via RecordControlPlanePeerResponse.
func (ckw *ApiConnectivityCheckWrapper) ControlPlanePeerLastResponse() time.Time {
	ckw.responsesMu.Lock()
	defer ckw.responsesMu.Unlock()
	return ckw.controlPlaneLastResponse
}

func (ckw *ApiConnectivityCheckWrapper) ResetPeerTimers() {
	ckw.ApiConnectivityCheck.ResetPeerTimers()
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
