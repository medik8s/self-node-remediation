package apicheck

import (
	"reflect"
	"testing"
	"time"
	"unsafe"

	corev1 "k8s.io/api/core/v1"

	selfnode "github.com/medik8s/self-node-remediation/api"
	"github.com/medik8s/self-node-remediation/pkg/peers"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
)

// helper to mutate unexported slice fields in peers.Peers for test purposes.
func setPeerAddresses(p *peers.Peers, field string, addrs []corev1.PodIP) {
	val := reflect.ValueOf(p).Elem().FieldByName(field)
	ptr := unsafe.Pointer(val.UnsafeAddr())
	slice := (*[]corev1.PodIP)(ptr)
	*slice = addrs
}

func TestWorkerEscalatesWhenControlPlanePeersUnavailable(t *testing.T) {
	workerAddr := []corev1.PodIP{{IP: "10.0.0.10"}}
	peerStore := &peers.Peers{}
	setPeerAddresses(peerStore, "workerPeersAddresses", workerAddr)
	setPeerAddresses(peerStore, "controlPlanePeersAddresses", nil)

	cfg := &ApiConnectivityCheckConfig{
		Log:                    logf.Log.WithName("test"),
		MyNodeName:             "worker-1",
		MyMachineName:          "machine-1",
		CheckInterval:          50 * time.Millisecond,
		FailureWindow:          50 * time.Millisecond,
		PeerQuorumTimeout:      100 * time.Millisecond,
		MaxErrorsThreshold:     1,
		Peers:                  peerStore,
		MinPeersForRemediation: 1,
	}

	check := New(cfg, nil)
	check.SetHealthStatusFunc(func(_ corev1.PodIP, results chan<- selfnode.HealthCheckResponseCode) {
		results <- selfnode.Unhealthy
	})

	tracker := check.ensureFailureTracker()
	tracker.RecordFailure(time.Now().Add(-cfg.FailureWindow))
	tracker.RecordFailure(time.Now().Add(-cfg.FailureWindow / 2))

	if healthy := check.isConsideredHealthy(); healthy {
		t.Fatalf("expected worker to treat lack of control-plane peers as unhealthy")
	}
}
