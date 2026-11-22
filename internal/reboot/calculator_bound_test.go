package reboot

import (
	"context"
	"strconv"
	"testing"
	"time"

	"github.com/go-logr/logr"
	commonlabels "github.com/medik8s/common/pkg/labels"

	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

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

// newTestCalculator returns a calculator over a fake cluster of nrOfWorkers worker nodes
func newTestCalculator(spec v1alpha1.SelfNodeRemediationConfigSpec, nrOfWorkers int) *calculator {
	workers := make([]client.Object, 0, nrOfWorkers)
	for i := 0; i < nrOfWorkers; i++ {
		node := &v1.Node{}
		node.Name = "worker-" + strconv.Itoa(i)
		node.Labels = map[string]string{commonlabels.WorkerRole: ""}
		workers = append(workers, node)
	}
	return &calculator{
		k8sClient: fake.NewClientBuilder().WithObjects(workers...).Build(),
		log:       logr.Discard(),
		snrConfig: &v1alpha1.SelfNodeRemediationConfig{Spec: spec},
	}
}

// TestMinimumRebootDurationCoversWorstCase checks that the calculated minimum is at least the simulated worst case
// for every cluster size from 3 to 300 nodes, and equal to it for the sizes that need the most batches.
func TestMinimumRebootDurationCoversWorstCase(t *testing.T) {
	spec := defaultSpec(3, 10*time.Minute)
	c := newTestCalculator(spec, 0)

	tight := 0
	previous := time.Duration(0)
	for nrOfNodes := 3; nrOfNodes <= 300; nrOfNodes++ {
		got := c.calculateMinimumRebootDuration(minWatchdogTimeout, utils.MaxNrOfBatchesUpTo(nrOfNodes))
		want := worstCaseRebootDuration(spec, nrOfNodes, minWatchdogTimeout)
		if got < want {
			t.Errorf("%d nodes: minimum reboot duration %s is below the worst case %s", nrOfNodes, got, want)
		}
		if got < previous {
			t.Errorf("%d nodes: minimum reboot duration %s is below the one for fewer nodes %s", nrOfNodes, got, previous)
		}
		if got == want {
			tight++
		}
		previous = got
	}
	if tight == 0 {
		t.Errorf("the minimum reboot duration is not reached by any cluster size, the bound is too loose")
	}
	// the bound saturates at MaxNrOfBatches
	if got, want := c.calculateMinimumRebootDuration(minWatchdogTimeout, utils.MaxNrOfBatchesUpTo(300)), c.calculateMinimumRebootDuration(minWatchdogTimeout, utils.MaxNrOfBatches); got != want {
		t.Errorf("300 nodes: expected the largest bound %s, got %s", want, got)
	}
}

// TestMinimumRebootDurationValues pins the calculated minimum for the CRD defaults and for a 10 minute window at
// the default and at a raised API error threshold.
func TestMinimumRebootDurationValues(t *testing.T) {
	testCases := []struct {
		threshold int
		window    time.Duration
		workers   int
		expected  time.Duration
	}{
		// 3 * 20 + 14 * 12 + 600 + 20 + 14 * 12 + 60 + 30
		{threshold: 3, window: 10 * time.Minute, workers: 100, expected: 1106 * time.Second},
		// 41 * 20 + 14 * 12 + 600 + 20 + 14 * 12 + 60 + 30
		{threshold: 41, window: 10 * time.Minute, workers: 100, expected: 1866 * time.Second},
		// 3 * 20 + 14 * 12 + 30 + 20 + 14 * 12 + 60 + 30
		{threshold: 3, window: 30 * time.Second, workers: 42, expected: 536 * time.Second},
		// 3 * 20 + 1 * 12 + 30 + 20 + 1 * 12 + 60 + 30: a small cluster needs one batch
		{threshold: 3, window: 30 * time.Second, workers: 3, expected: 224 * time.Second},
	}
	for _, tc := range testCases {
		got := newTestCalculator(defaultSpec(tc.threshold, tc.window), tc.workers).calculateMinimumRebootDuration(minWatchdogTimeout, utils.MaxNrOfBatchesUpTo(tc.workers))
		if got != tc.expected {
			t.Errorf("threshold %d, window %s, %d workers: expected %s, got %s", tc.threshold, tc.window, tc.workers, tc.expected, got)
		}
	}
}

// TestRebootDurationWatchdogFloor checks that a missing, unparsable, zero or too low watchdog annotation yields the
// minimum watchdog timeout, and that a higher annotation is used as is.
func TestRebootDurationWatchdogFloor(t *testing.T) {
	spec := defaultSpec(3, 10*time.Minute)
	c := newTestCalculator(spec, 3)
	expected := c.calculateMinimumRebootDuration(minWatchdogTimeout, utils.MaxNrOfBatchesUpTo(3))

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
		if expected := c.calculateMinimumRebootDuration(90*time.Second, utils.MaxNrOfBatchesUpTo(3)); got != expected {
			t.Errorf("expected %s, got %s", expected, got)
		}
	})
}
