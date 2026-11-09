package reboot

import (
	"context"
	"strconv"
	"testing"
	"time"

	"github.com/go-logr/logr"

	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/medik8s/self-node-remediation/v5/api/v1alpha1"
	"github.com/medik8s/self-node-remediation/v5/internal/utils"
)

// defaultSpec returns the CRD defaults with the given API error threshold and no-peers-response window
func defaultSpec(threshold int, window time.Duration) v1alpha1.SelfNodeRemediationConfigSpec {
	return v1alpha1.SelfNodeRemediationConfigSpec{
		ApiCheckInterval:          &metav1.Duration{Duration: 15 * time.Second},
		ApiServerTimeout:          &metav1.Duration{Duration: 5 * time.Second},
		MaxApiErrorThreshold:      threshold,
		PeerDialTimeout:           &metav1.Duration{Duration: 5 * time.Second},
		PeerRequestTimeout:        &metav1.Duration{Duration: 5 * time.Second},
		MaxTimeForNoPeersResponse: &metav1.Duration{Duration: window},
	}
}

// worstCaseRebootDuration simulates the slowest path of an agent that lost the API server in a cluster of nrOfNodes
// worker nodes: MaxApiErrorThreshold failed checks, a full sweep of the peers ending with an "api error" answer
// that restarts the window, the window, one check cycle and one full sweep that straddle the end of the window,
// then the watchdog and the time for the node to go down.
func worstCaseRebootDuration(spec v1alpha1.SelfNodeRemediationConfigSpec, nrOfNodes int, watchdogTimeout time.Duration) time.Duration {
	check := spec.ApiCheckInterval.Duration + spec.ApiServerTimeout.Duration
	sweep := time.Duration(utils.GetNrOfBatches(nrOfNodes)) * (spec.PeerDialTimeout.Duration + effectivePeerRequestTimeout(spec))
	return time.Duration(spec.MaxApiErrorThreshold)*check + sweep + spec.MaxTimeForNoPeersResponse.Duration + check + sweep + watchdogTimeout + rebootBuffer
}

func newTestCalculator(spec v1alpha1.SelfNodeRemediationConfigSpec) *calculator {
	return &calculator{
		log:       logr.Discard(),
		snrConfig: &v1alpha1.SelfNodeRemediationConfig{Spec: spec},
	}
}

func TestMinimumRebootDurationCoversWorstCase(t *testing.T) {
	spec := defaultSpec(3, 10*time.Minute)
	got := newTestCalculator(spec).calculateMinimumRebootDuration(minWatchdogTimeout)

	tight := false
	for nrOfNodes := 3; nrOfNodes <= 300; nrOfNodes++ {
		want := worstCaseRebootDuration(spec, nrOfNodes, minWatchdogTimeout)
		if got < want {
			t.Errorf("%d nodes: minimum reboot duration %s is below the worst case %s", nrOfNodes, got, want)
		}
		if got == want {
			tight = true
		}
	}
	if !tight {
		t.Errorf("minimum reboot duration %s is not reached by any cluster size, the bound is too loose", got)
	}
}

func TestMinimumRebootDurationValues(t *testing.T) {
	testCases := []struct {
		threshold int
		window    time.Duration
		expected  time.Duration
	}{
		// 3 * 20 + 14 * 12 + 600 + 20 + 14 * 12 + 60 + 30
		{threshold: 3, window: 10 * time.Minute, expected: 1106 * time.Second},
		// 41 * 20 + 14 * 12 + 600 + 20 + 14 * 12 + 60 + 30
		{threshold: 41, window: 10 * time.Minute, expected: 1866 * time.Second},
		// 3 * 20 + 14 * 12 + 30 + 20 + 14 * 12 + 60 + 30
		{threshold: 3, window: 30 * time.Second, expected: 536 * time.Second},
	}
	for _, tc := range testCases {
		got := newTestCalculator(defaultSpec(tc.threshold, tc.window)).calculateMinimumRebootDuration(minWatchdogTimeout)
		if got != tc.expected {
			t.Errorf("threshold %d, window %s: expected %s, got %s", tc.threshold, tc.window, tc.expected, got)
		}
	}
}

func TestRebootDurationWatchdogFloor(t *testing.T) {
	spec := defaultSpec(3, 10*time.Minute)
	c := newTestCalculator(spec)
	expected := c.calculateMinimumRebootDuration(minWatchdogTimeout)

	testCases := []struct {
		name        string
		annotations map[string]string
	}{
		{name: "no annotation", annotations: nil},
		{name: "unparsable annotation", annotations: map[string]string{utils.WatchdogTimeoutSecondsAnnotation: "soon"}},
		{name: "zero timeout, no watchdog device", annotations: map[string]string{utils.WatchdogTimeoutSecondsAnnotation: "0"}},
		{name: "timeout below the minimum", annotations: map[string]string{utils.WatchdogTimeoutSecondsAnnotation: "10"}},
		{name: "timeout at the minimum", annotations: map[string]string{utils.WatchdogTimeoutSecondsAnnotation: strconv.Itoa(int(minWatchdogTimeout.Seconds()))}},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			node := &v1.Node{}
			node.Name = "node"
			node.Annotations = tc.annotations
			got, err := c.GetRebootDuration(context.Background(), node)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != expected {
				t.Errorf("expected %s, got %s", expected, got)
			}
		})
	}

	t.Run("timeout above the minimum", func(t *testing.T) {
		node := &v1.Node{}
		node.Name = "node"
		node.Annotations = map[string]string{utils.WatchdogTimeoutSecondsAnnotation: "90"}
		got, err := c.GetRebootDuration(context.Background(), node)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if expected := c.calculateMinimumRebootDuration(90 * time.Second); got != expected {
			t.Errorf("expected %s, got %s", expected, got)
		}
	})
}
