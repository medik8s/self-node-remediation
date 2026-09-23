package peers

import (
	"context"
	"errors"
	"reflect"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-logr/logr"

	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func TestMapNodesToPrimaryPodIPs(t *testing.T) {
	testCases := []struct {
		name        string
		nodes       v1.NodeList
		pods        v1.PodList
		expectedIPs []v1.PodIP // Expected list of *valid* IPs
		expectError bool
	}{
		{
			name:        "Empty lists",
			nodes:       v1.NodeList{},
			pods:        v1.PodList{},
			expectedIPs: []v1.PodIP{},
			expectError: false,
		},
		{
			name: "Nodes exist, no pods (should return error)",
			nodes: v1.NodeList{
				Items: []v1.Node{
					{ObjectMeta: metav1.ObjectMeta{Name: "node1"}},
					{ObjectMeta: metav1.ObjectMeta{Name: "node2"}},
				},
			},
			pods:        v1.PodList{}, // Empty pods list
			expectedIPs: []v1.PodIP{}, // No matching pods means no IPs can be found
			expectError: true,
		},
		{
			name: "Single node, single matching pod, one IP",
			nodes: v1.NodeList{
				Items: []v1.Node{
					{ObjectMeta: metav1.ObjectMeta{Name: "node1"}},
				},
			},
			pods: v1.PodList{
				Items: []v1.Pod{
					{
						ObjectMeta: metav1.ObjectMeta{Name: "pod1"},
						Spec:       v1.PodSpec{NodeName: "node1"},
						Status:     v1.PodStatus{PodIPs: []v1.PodIP{{IP: "10.0.0.1"}}},
					},
				},
			},
			expectedIPs: []v1.PodIP{{IP: "10.0.0.1"}},
			expectError: false,
		},
		{
			name: "Multiple nodes, all with a valid matching pod",
			nodes: v1.NodeList{
				Items: []v1.Node{
					{ObjectMeta: metav1.ObjectMeta{Name: "node1"}}, // Has matching pod
					{ObjectMeta: metav1.ObjectMeta{Name: "node2"}}, // Has matching pod
				},
			},
			pods: v1.PodList{
				Items: []v1.Pod{
					{ObjectMeta: metav1.ObjectMeta{Name: "pod1"}, Spec: v1.PodSpec{NodeName: "node1"}, Status: v1.PodStatus{PodIPs: []v1.PodIP{{IP: "10.0.0.1"}}}},
					{ObjectMeta: metav1.ObjectMeta{Name: "pod2"}, Spec: v1.PodSpec{NodeName: "node2"}, Status: v1.PodStatus{PodIPs: []v1.PodIP{{IP: "10.0.0.2"}}}},
				},
			},
			expectedIPs: []v1.PodIP{{IP: "10.0.0.1"}, {IP: "10.0.0.2"}},
			expectError: false,
		},
		{
			name: "Error: Multiple nodes, one has no matching pod (should return error and skip node)",
			nodes: v1.NodeList{
				Items: []v1.Node{
					{ObjectMeta: metav1.ObjectMeta{Name: "node1"}}, // Has matching pod
					{ObjectMeta: metav1.ObjectMeta{Name: "node2"}}, // No matching pod in pods list
				},
			},
			pods: v1.PodList{
				Items: []v1.Pod{
					{ObjectMeta: metav1.ObjectMeta{Name: "pod1"}, Spec: v1.PodSpec{NodeName: "node1"}, Status: v1.PodStatus{PodIPs: []v1.PodIP{{IP: "10.0.0.1"}}}},
				},
			},
			expectedIPs: []v1.PodIP{{IP: "10.0.0.1"}}, // Only the IP for node1 is returned
			expectError: true,
		},
		{
			name: "Error: Matching pod found, PodIPs is nil (should return error and skip node)",
			nodes: v1.NodeList{
				Items: []v1.Node{
					{ObjectMeta: metav1.ObjectMeta{Name: "node1"}},
				},
			},
			pods: v1.PodList{
				Items: []v1.Pod{
					{
						ObjectMeta: metav1.ObjectMeta{Name: "pod1-nil"},
						Spec:       v1.PodSpec{NodeName: "node1"},
						Status:     v1.PodStatus{PodIPs: nil}, // PodIPs is nil - should set error and skip
					},
				},
			},
			expectedIPs: []v1.PodIP{}, // No valid IPs found
			expectError: true,
		},
		{
			name: "Error: Matching pod found, PodIPs is empty slice (should return error and skip node)",
			nodes: v1.NodeList{
				Items: []v1.Node{
					{ObjectMeta: metav1.ObjectMeta{Name: "node1"}},
				},
			},
			pods: v1.PodList{
				Items: []v1.Pod{
					{
						ObjectMeta: metav1.ObjectMeta{Name: "pod1-empty"},
						Spec:       v1.PodSpec{NodeName: "node1"},
						Status:     v1.PodStatus{PodIPs: []v1.PodIP{}}, // PodIPs is empty slice - should set error and skip
					},
				},
			},
			expectedIPs: []v1.PodIP{}, // No valid IPs found
			expectError: true,
		},
		{
			name: "Error: Matching pod found, Primary IP is empty string (should return error and skip node)",
			nodes: v1.NodeList{
				Items: []v1.Node{
					{ObjectMeta: metav1.ObjectMeta{Name: "node1"}},
				},
			},
			pods: v1.PodList{
				Items: []v1.Pod{
					{
						ObjectMeta: metav1.ObjectMeta{Name: "pod1-emptyip"},
						Spec:       v1.PodSpec{NodeName: "node1"},
						Status:     v1.PodStatus{PodIPs: []v1.PodIP{{IP: ""}, {IP: "2.2.2.2"}}}, // Primary IP is empty string - should set error and skip
					},
				},
			},
			expectedIPs: []v1.PodIP{}, // No valid IPs found
			expectError: true,
		},
		{
			name: "Error: Multiple nodes, one has error (nil IPs), others are fine (should return error and valid IPs)",
			nodes: v1.NodeList{
				Items: []v1.Node{
					{ObjectMeta: metav1.ObjectMeta{Name: "nodeA"}}, // Has error pod
					{ObjectMeta: metav1.ObjectMeta{Name: "nodeB"}}, // Fine pod
					{ObjectMeta: metav1.ObjectMeta{Name: "nodeC"}}, // Fine pod
				},
			},
			pods: v1.PodList{
				Items: []v1.Pod{
					{ObjectMeta: metav1.ObjectMeta{Name: "podA-err"}, Spec: v1.PodSpec{NodeName: "nodeA"}, Status: v1.PodStatus{PodIPs: nil}},                         // Error
					{ObjectMeta: metav1.ObjectMeta{Name: "podB-ok"}, Spec: v1.PodSpec{NodeName: "nodeB"}, Status: v1.PodStatus{PodIPs: []v1.PodIP{{IP: "10.1.0.2"}}}}, // OK
					{ObjectMeta: metav1.ObjectMeta{Name: "podC-ok"}, Spec: v1.PodSpec{NodeName: "nodeC"}, Status: v1.PodStatus{PodIPs: []v1.PodIP{{IP: "10.1.0.3"}}}}, // OK
				},
			},
			expectedIPs: []v1.PodIP{{IP: "10.1.0.2"}, {IP: "10.1.0.3"}}, // Only valid IPs returned
			expectError: true,
		},
		{
			name: "Error: Multiple nodes, one has error (empty Primary IP), others are fine (should return error and valid IPs)",
			nodes: v1.NodeList{
				Items: []v1.Node{
					{ObjectMeta: metav1.ObjectMeta{Name: "nodeA"}}, // Fine pod
					{ObjectMeta: metav1.ObjectMeta{Name: "nodeB"}}, // Has error pod
					{ObjectMeta: metav1.ObjectMeta{Name: "nodeC"}}, // Fine pod
				},
			},
			pods: v1.PodList{
				Items: []v1.Pod{
					{ObjectMeta: metav1.ObjectMeta{Name: "podA-ok"}, Spec: v1.PodSpec{NodeName: "nodeA"}, Status: v1.PodStatus{PodIPs: []v1.PodIP{{IP: "10.1.0.1"}}}}, // OK
					{ObjectMeta: metav1.ObjectMeta{Name: "podB-err"}, Spec: v1.PodSpec{NodeName: "nodeB"}, Status: v1.PodStatus{PodIPs: []v1.PodIP{{IP: ""}}}},        // Error
					{ObjectMeta: metav1.ObjectMeta{Name: "podC-ok"}, Spec: v1.PodSpec{NodeName: "nodeC"}, Status: v1.PodStatus{PodIPs: []v1.PodIP{{IP: "10.1.0.3"}}}}, // OK
				},
			},
			expectedIPs: []v1.PodIP{{IP: "10.1.0.1"}, {IP: "10.1.0.3"}}, // Only valid IPs returned
			expectError: true,
		},
		{
			name: "Error: Multiple nodes, multiple errors (should return error and valid IPs)",
			nodes: v1.NodeList{
				Items: []v1.Node{
					{ObjectMeta: metav1.ObjectMeta{Name: "nodeA"}}, // Error 1 (nil IPs)
					{ObjectMeta: metav1.ObjectMeta{Name: "nodeB"}}, // Fine
					{ObjectMeta: metav1.ObjectMeta{Name: "nodeC"}}, // Error 2 (empty Primary IP)
				},
			},
			pods: v1.PodList{
				Items: []v1.Pod{
					{ObjectMeta: metav1.ObjectMeta{Name: "podA-err"}, Spec: v1.PodSpec{NodeName: "nodeA"}, Status: v1.PodStatus{PodIPs: nil}},                         // Error 1
					{ObjectMeta: metav1.ObjectMeta{Name: "podB-ok"}, Spec: v1.PodSpec{NodeName: "nodeB"}, Status: v1.PodStatus{PodIPs: []v1.PodIP{{IP: "10.1.0.2"}}}}, // OK
					{ObjectMeta: metav1.ObjectMeta{Name: "podC-err"}, Spec: v1.PodSpec{NodeName: "nodeC"}, Status: v1.PodStatus{PodIPs: []v1.PodIP{{IP: ""}}}},        // Error 2
				},
			},
			expectedIPs: []v1.PodIP{{IP: "10.1.0.2"}}, // Only valid IPs returned
			expectError: true,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			p := Peers{
				log: logr.Logger{},
			}
			actualIPs, actualErr := p.mapNodesToPrimaryPodIPs(tc.nodes, tc.pods)

			// Check if error expectation matches
			if tc.expectError {
				if actualErr == nil {
					t.Errorf("Expected an error but got none")
				}
			} else { // Not expecting an error
				if actualErr != nil {
					t.Errorf("Expected no error but got: %v", actualErr)
				}
			}

			// Check if the returned slice contains only valid IPs and matches the expected list
			// This assertion applies to BOTH success and error cases based on the intended behavior.
			if !reflect.DeepEqual(actualIPs, tc.expectedIPs) {
				t.Errorf("Result slice mismatch. Expected: %v, Got: %v", tc.expectedIPs, actualIPs)
			}

			// Additional check for the length requirement (length <= input node list length)
			if len(actualIPs) > len(tc.nodes.Items) {
				t.Errorf("Returned slice length (%d) is greater than input node list length (%d)", len(actualIPs), len(tc.nodes.Items))
			}

			// Additional check: Ensure no empty IPs are present in the returned slice
			for i, ip := range actualIPs {
				if ip.IP == "" {
					t.Errorf("Returned slice contains an empty IP at index %d: %v", i, actualIPs)
					break // No need to check further if one is found
				}
			}
		})
	}
}

// mockReaderWithPeers is a mock implementation that returns specific nodes and pods for testing
type mockReaderWithPeers struct {
	client.Reader
	getCallCount  atomic.Int32
	listCallCount atomic.Int32
	nodes         []v1.Node
	pods          []v1.Pod
	// ownNodeLabels, when set, replaces the default labels of the own node
	ownNodeLabels map[string]string
}

func (m *mockReaderWithPeers) Get(ctx context.Context, key types.NamespacedName, obj client.Object, opts ...client.GetOption) error {
	m.getCallCount.Add(1)

	// Return the own node on the first Get call (for initialization in Start)
	if node, ok := obj.(*v1.Node); ok {
		node.Name = "test-node"
		node.Labels = map[string]string{
			hostnameLabelName:                       "test-hostname",
			"node-role.kubernetes.io/control-plane": "",
		}
		if m.ownNodeLabels != nil {
			node.Labels = m.ownNodeLabels
		}
		return nil
	}

	return errors.New("mock get error")
}

func (m *mockReaderWithPeers) List(ctx context.Context, list client.ObjectList, opts ...client.ListOption) error {
	m.listCallCount.Add(1)

	switch l := list.(type) {
	case *v1.NodeList:
		// Filter nodes based on the selector
		if len(opts) > 0 {
			if selector, ok := opts[0].(client.MatchingLabelsSelector); ok {
				var filteredNodes []v1.Node
				for _, node := range m.nodes {
					if selector.Selector.Matches(labels.Set(node.Labels)) {
						filteredNodes = append(filteredNodes, node)
					}
				}
				l.Items = filteredNodes
			} else {
				l.Items = m.nodes
			}
		} else {
			l.Items = m.nodes
		}
	case *v1.PodList:
		l.Items = m.pods
	}

	return nil
}

// TestStartVerifiesPeerAddresses tests that Start correctly populates workerPeersAddresses
// and controlPlanePeersAddresses, handling missing pods and pods with empty IPs, and errors
// aren't returned by Start()
func TestStartVerifiesPeerAddresses(t *testing.T) {
	testCases := []struct {
		name                          string
		nodes                         []v1.Node
		pods                          []v1.Pod
		expectedWorkerPeerCount       int
		expectedControlPlanePeerCount int
		expectedWorkerIPs             []string
		expectedControlPlaneIPs       []string
		description                   string
	}{
		{
			name: "All worker nodes have valid pods",
			nodes: []v1.Node{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name: "worker-1",
						Labels: map[string]string{
							hostnameLabelName:                "worker-1",
							"node-role.kubernetes.io/worker": "",
						},
					},
				},
				{
					ObjectMeta: metav1.ObjectMeta{
						Name: "worker-2",
						Labels: map[string]string{
							hostnameLabelName:                "worker-2",
							"node-role.kubernetes.io/worker": "",
						},
					},
				},
			},
			pods: []v1.Pod{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name: "snr-pod-1",
						Labels: map[string]string{
							"app.kubernetes.io/name":      "self-node-remediation",
							"app.kubernetes.io/component": "agent",
						},
					},
					Spec:   v1.PodSpec{NodeName: "worker-1"},
					Status: v1.PodStatus{PodIPs: []v1.PodIP{{IP: "10.0.1.1"}}},
				},
				{
					ObjectMeta: metav1.ObjectMeta{
						Name: "snr-pod-2",
						Labels: map[string]string{
							"app.kubernetes.io/name":      "self-node-remediation",
							"app.kubernetes.io/component": "agent",
						},
					},
					Spec:   v1.PodSpec{NodeName: "worker-2"},
					Status: v1.PodStatus{PodIPs: []v1.PodIP{{IP: "10.0.1.2"}}},
				},
			},
			expectedWorkerPeerCount:       2,
			expectedControlPlanePeerCount: 0,
			expectedWorkerIPs:             []string{"10.0.1.1", "10.0.1.2"},
			expectedControlPlaneIPs:       []string{},
			description:                   "All worker nodes should have their IPs populated",
		},
		{
			name: "Worker node missing pod",
			nodes: []v1.Node{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name: "worker-1",
						Labels: map[string]string{
							hostnameLabelName:                "worker-1",
							"node-role.kubernetes.io/worker": "",
						},
					},
				},
				{
					ObjectMeta: metav1.ObjectMeta{
						Name: "worker-2",
						Labels: map[string]string{
							hostnameLabelName:                "worker-2",
							"node-role.kubernetes.io/worker": "",
						},
					},
				},
			},
			pods: []v1.Pod{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name: "snr-pod-1",
						Labels: map[string]string{
							"app.kubernetes.io/name":      "self-node-remediation",
							"app.kubernetes.io/component": "agent",
						},
					},
					Spec:   v1.PodSpec{NodeName: "worker-1"},
					Status: v1.PodStatus{PodIPs: []v1.PodIP{{IP: "10.0.1.1"}}},
				},
				// worker-2 has no matching pod
			},
			expectedWorkerPeerCount:       1,
			expectedControlPlanePeerCount: 0,
			expectedWorkerIPs:             []string{"10.0.1.1"},
			expectedControlPlaneIPs:       []string{},
			description:                   "Only worker-1 should have its IP, worker-2 is missing",
		},
		{
			name: "Worker pod with empty IP",
			nodes: []v1.Node{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name: "worker-1",
						Labels: map[string]string{
							hostnameLabelName:                "worker-1",
							"node-role.kubernetes.io/worker": "",
						},
					},
				},
				{
					ObjectMeta: metav1.ObjectMeta{
						Name: "worker-2",
						Labels: map[string]string{
							hostnameLabelName:                "worker-2",
							"node-role.kubernetes.io/worker": "",
						},
					},
				},
			},
			pods: []v1.Pod{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name: "snr-pod-1",
						Labels: map[string]string{
							"app.kubernetes.io/name":      "self-node-remediation",
							"app.kubernetes.io/component": "agent",
						},
					},
					Spec:   v1.PodSpec{NodeName: "worker-1"},
					Status: v1.PodStatus{PodIPs: []v1.PodIP{{IP: "10.0.1.1"}}},
				},
				{
					ObjectMeta: metav1.ObjectMeta{
						Name: "snr-pod-2",
						Labels: map[string]string{
							"app.kubernetes.io/name":      "self-node-remediation",
							"app.kubernetes.io/component": "agent",
						},
					},
					Spec:   v1.PodSpec{NodeName: "worker-2"},
					Status: v1.PodStatus{PodIPs: []v1.PodIP{{IP: ""}}}, // Empty IP
				},
			},
			expectedWorkerPeerCount:       1,
			expectedControlPlanePeerCount: 0,
			expectedWorkerIPs:             []string{"10.0.1.1"},
			expectedControlPlaneIPs:       []string{},
			description:                   "Only worker-1 should be included, worker-2 has empty IP",
		},
		{
			name: "Mixed worker and control plane nodes",
			nodes: []v1.Node{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name: "worker-1",
						Labels: map[string]string{
							hostnameLabelName:                "worker-1",
							"node-role.kubernetes.io/worker": "",
						},
					},
				},
				{
					ObjectMeta: metav1.ObjectMeta{
						Name: "cp-1",
						Labels: map[string]string{
							hostnameLabelName:                       "cp-1",
							"node-role.kubernetes.io/control-plane": "",
						},
					},
				},
				{
					ObjectMeta: metav1.ObjectMeta{
						Name: "cp-2",
						Labels: map[string]string{
							hostnameLabelName:                       "cp-2",
							"node-role.kubernetes.io/control-plane": "",
						},
					},
				},
			},
			pods: []v1.Pod{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name: "snr-pod-worker-1",
						Labels: map[string]string{
							"app.kubernetes.io/name":      "self-node-remediation",
							"app.kubernetes.io/component": "agent",
						},
					},
					Spec:   v1.PodSpec{NodeName: "worker-1"},
					Status: v1.PodStatus{PodIPs: []v1.PodIP{{IP: "10.0.1.1"}}},
				},
				{
					ObjectMeta: metav1.ObjectMeta{
						Name: "snr-pod-cp-1",
						Labels: map[string]string{
							"app.kubernetes.io/name":      "self-node-remediation",
							"app.kubernetes.io/component": "agent",
						},
					},
					Spec:   v1.PodSpec{NodeName: "cp-1"},
					Status: v1.PodStatus{PodIPs: []v1.PodIP{{IP: "10.0.2.1"}}},
				},
				{
					ObjectMeta: metav1.ObjectMeta{
						Name: "snr-pod-cp-2",
						Labels: map[string]string{
							"app.kubernetes.io/name":      "self-node-remediation",
							"app.kubernetes.io/component": "agent",
						},
					},
					Spec:   v1.PodSpec{NodeName: "cp-2"},
					Status: v1.PodStatus{PodIPs: []v1.PodIP{{IP: "10.0.2.2"}}},
				},
			},
			expectedWorkerPeerCount:       1,
			expectedControlPlanePeerCount: 2,
			expectedWorkerIPs:             []string{"10.0.1.1"},
			expectedControlPlaneIPs:       []string{"10.0.2.1", "10.0.2.2"},
			description:                   "Should separate worker and control plane peers correctly",
		},
		{
			name: "Control plane node with missing pod and empty IP",
			nodes: []v1.Node{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name: "cp-1",
						Labels: map[string]string{
							hostnameLabelName:                       "cp-1",
							"node-role.kubernetes.io/control-plane": "",
						},
					},
				},
				{
					ObjectMeta: metav1.ObjectMeta{
						Name: "cp-2",
						Labels: map[string]string{
							hostnameLabelName:                       "cp-2",
							"node-role.kubernetes.io/control-plane": "",
						},
					},
				},
				{
					ObjectMeta: metav1.ObjectMeta{
						Name: "cp-3",
						Labels: map[string]string{
							hostnameLabelName:                       "cp-3",
							"node-role.kubernetes.io/control-plane": "",
						},
					},
				},
			},
			pods: []v1.Pod{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name: "snr-pod-cp-1",
						Labels: map[string]string{
							"app.kubernetes.io/name":      "self-node-remediation",
							"app.kubernetes.io/component": "agent",
						},
					},
					Spec:   v1.PodSpec{NodeName: "cp-1"},
					Status: v1.PodStatus{PodIPs: []v1.PodIP{{IP: "10.0.2.1"}}},
				},
				{
					ObjectMeta: metav1.ObjectMeta{
						Name: "snr-pod-cp-2",
						Labels: map[string]string{
							"app.kubernetes.io/name":      "self-node-remediation",
							"app.kubernetes.io/component": "agent",
						},
					},
					Spec:   v1.PodSpec{NodeName: "cp-2"},
					Status: v1.PodStatus{PodIPs: []v1.PodIP{{IP: ""}}}, // Empty IP
				},
				// cp-3 has no pod
			},
			expectedWorkerPeerCount:       0,
			expectedControlPlanePeerCount: 1,
			expectedWorkerIPs:             []string{},
			expectedControlPlaneIPs:       []string{"10.0.2.1"},
			description:                   "Only cp-1 should be included (cp-2 has empty IP, cp-3 missing pod)",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			// Create a mock reader with the test data
			mockReader := &mockReaderWithPeers{
				nodes: tc.nodes,
				pods:  tc.pods,
			}

			p := New(
				"test-node",
				50*time.Millisecond,
				mockReader,
				logr.Discard(),
				5*time.Second,
			)

			// Run Start for a short time to allow peer updates
			ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
			defer cancel()

			err := p.Start(ctx)
			if err != nil {
				t.Errorf("Start() returned an error when it should not: %v", err)
			}

			// Give it a moment to populate the addresses
			time.Sleep(100 * time.Millisecond)

			// Get the peer addresses
			workerAddresses := p.GetPeersAddresses(Worker)
			controlPlaneAddresses := p.GetPeersAddresses(ControlPlane)

			// Verify worker peer count
			if len(workerAddresses) != tc.expectedWorkerPeerCount {
				t.Errorf("Expected %d worker peers, got %d. Addresses: %v",
					tc.expectedWorkerPeerCount, len(workerAddresses), workerAddresses)
			}

			// Verify control plane peer count
			if len(controlPlaneAddresses) != tc.expectedControlPlanePeerCount {
				t.Errorf("Expected %d control plane peers, got %d. Addresses: %v",
					tc.expectedControlPlanePeerCount, len(controlPlaneAddresses), controlPlaneAddresses)
			}

			// Verify worker IPs
			workerIPs := make([]string, len(workerAddresses))
			for i, addr := range workerAddresses {
				workerIPs[i] = addr.IP
			}
			if !reflect.DeepEqual(workerIPs, tc.expectedWorkerIPs) {
				t.Errorf("Worker IPs mismatch. Expected: %v, Got: %v", tc.expectedWorkerIPs, workerIPs)
			}

			// Verify control plane IPs
			cpIPs := make([]string, len(controlPlaneAddresses))
			for i, addr := range controlPlaneAddresses {
				cpIPs[i] = addr.IP
			}
			if !reflect.DeepEqual(cpIPs, tc.expectedControlPlaneIPs) {
				t.Errorf("Control plane IPs mismatch. Expected: %v, Got: %v", tc.expectedControlPlaneIPs, cpIPs)
			}

			// Verify no empty IPs in the results
			for _, addr := range workerAddresses {
				if addr.IP == "" {
					t.Errorf("Worker addresses contain empty IP: %v", workerAddresses)
					break
				}
			}
			for _, addr := range controlPlaneAddresses {
				if addr.IP == "" {
					t.Errorf("Control plane addresses contain empty IP: %v", controlPlaneAddresses)
					break
				}
			}

			t.Logf("Test passed: %s", tc.description)
		})
	}
}

func TestTopologyDomains(t *testing.T) {
	const topologyKey = "topology.kubernetes.io/zone"

	node := func(name, zone string) v1.Node {
		labels := map[string]string{hostnameLabelName: name, "node-role.kubernetes.io/worker": ""}
		if zone != "" {
			labels[topologyKey] = zone
		}
		return v1.Node{ObjectMeta: metav1.ObjectMeta{Name: name, Labels: labels}}
	}
	pod := func(nodeName, ip string) v1.Pod {
		return v1.Pod{
			ObjectMeta: metav1.ObjectMeta{Name: "snr-" + nodeName},
			Spec:       v1.PodSpec{NodeName: nodeName},
			Status:     v1.PodStatus{PodIPs: []v1.PodIP{{IP: ip}}},
		}
	}

	nodes := v1.NodeList{Items: []v1.Node{node("same", "zone-a"), node("other", "zone-b"), node("unlabeled", "")}}
	pods := v1.PodList{Items: []v1.Pod{pod("same", "10.0.0.1"), pod("other", "10.0.0.2"), pod("unlabeled", "10.0.0.3")}}
	addresses := []v1.PodIP{{IP: "10.0.0.1"}, {IP: "10.0.0.2"}, {IP: "10.0.0.3"}}

	testCases := []struct {
		name                 string
		topologyKey          string
		myDomain             string
		expectSameDomain     map[string]bool
		expectOutsideMyCount int
	}{
		{
			name:                 "feature disabled: nobody is in my domain, nobody is outside",
			topologyKey:          "",
			myDomain:             "",
			expectSameDomain:     map[string]bool{"10.0.0.1": false, "10.0.0.2": false, "10.0.0.3": false},
			expectOutsideMyCount: 0,
		},
		{
			name:                 "feature enabled but own node has no label: disabled on this node",
			topologyKey:          topologyKey,
			myDomain:             "",
			expectSameDomain:     map[string]bool{"10.0.0.1": false, "10.0.0.2": false, "10.0.0.3": false},
			expectOutsideMyCount: 0,
		},
		{
			name:                 "feature enabled: same-domain peer detected, other and unlabeled peers count as outside",
			topologyKey:          topologyKey,
			myDomain:             "zone-a",
			expectSameDomain:     map[string]bool{"10.0.0.1": true, "10.0.0.2": false, "10.0.0.3": false},
			expectOutsideMyCount: 2,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			p := New("me", time.Minute, nil, logr.Discard(), time.Second)
			p.SetTopologyKey(tc.topologyKey)
			p.myTopologyDomain = tc.myDomain
			p.workerPeersAddresses = addresses
			p.updateTopologyDomains(nodes, pods)

			snapshot := p.GetPeersSnapshot(Worker)
			if !reflect.DeepEqual(snapshot.Addresses, addresses) {
				t.Errorf("snapshot addresses = %v, expected %v", snapshot.Addresses, addresses)
			}
			for ip, expected := range tc.expectSameDomain {
				if got := snapshot.InMyDomain[ip]; got != expected {
					t.Errorf("InMyDomain[%s] = %v, expected %v", ip, got, expected)
				}
			}
			if snapshot.OutsideMyDomain != tc.expectOutsideMyCount {
				t.Errorf("OutsideMyDomain = %d, expected %d", snapshot.OutsideMyDomain, tc.expectOutsideMyCount)
			}
		})
	}

	t.Run("an update scoped to another role does not forget the peers of this role", func(t *testing.T) {
		p := New("me", time.Minute, nil, logr.Discard(), time.Second)
		p.SetTopologyKey(topologyKey)
		p.myTopologyDomain = "zone-a"
		p.workerPeersAddresses = addresses
		p.updateTopologyDomains(nodes, pods) // workers
		controlPlanes := v1.NodeList{Items: []v1.Node{node("cp", "zone-a")}}
		p.updateTopologyDomains(controlPlanes, pods) // control planes, same pod list, none of them on "cp"
		if !p.GetPeersSnapshot(Worker).InMyDomain["10.0.0.1"] {
			t.Error("expected the worker peer domain to survive a control plane peers update")
		}
	})

	t.Run("a peer that loses its label is forgotten on the next update", func(t *testing.T) {
		p := New("me", time.Minute, nil, logr.Discard(), time.Second)
		p.SetTopologyKey(topologyKey)
		p.myTopologyDomain = "zone-a"
		p.workerPeersAddresses = addresses
		p.updateTopologyDomains(nodes, pods)
		if !p.GetPeersSnapshot(Worker).InMyDomain["10.0.0.1"] {
			t.Fatal("expected 10.0.0.1 to be in my domain before the update")
		}
		relabeled := v1.NodeList{Items: []v1.Node{node("same", ""), node("other", "zone-b"), node("unlabeled", "")}}
		p.updateTopologyDomains(relabeled, pods)
		if p.GetPeersSnapshot(Worker).InMyDomain["10.0.0.1"] {
			t.Error("expected 10.0.0.1 to be forgotten after its node lost the label")
		}
	})
}

func TestControlPlaneDomains(t *testing.T) {
	const topologyKey = "topology.kubernetes.io/zone"

	controlPlane := func(name, zone string) v1.Node {
		labels := map[string]string{hostnameLabelName: name, "node-role.kubernetes.io/control-plane": ""}
		if zone != "" {
			labels[topologyKey] = zone
		}
		return v1.Node{ObjectMeta: metav1.ObjectMeta{Name: name, Labels: labels}}
	}
	nodes := v1.NodeList{Items: []v1.Node{
		controlPlane("cp-a", "zone-a"), controlPlane("cp-b", "zone-b"), controlPlane("cp-c", "zone-c"), controlPlane("cp-x", ""),
	}}

	testCases := []struct {
		name        string
		topologyKey string
		myDomain    string
		expected    ControlPlaneDomains
	}{
		{name: "feature disabled", topologyKey: "", myDomain: "", expected: ControlPlaneDomains{}},
		{name: "own node has no label", topologyKey: topologyKey, myDomain: "", expected: ControlPlaneDomains{}},
		{name: "spread over domains, unlabeled node reported apart", topologyKey: topologyKey, myDomain: "zone-a",
			expected: ControlPlaneDomains{InMyDomain: 1, InOtherDomain: 2, Unknown: 1}},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			p := New("me", time.Minute, nil, logr.Discard(), time.Second)
			p.SetTopologyKey(tc.topologyKey)
			p.myTopologyDomain = tc.myDomain
			p.updateControlPlaneNodeDomains(nodes)
			if got := p.GetControlPlaneDomains(); got != tc.expected {
				t.Errorf("GetControlPlaneDomains() = %+v, expected %+v", got, tc.expected)
			}
		})
	}

	t.Run("does not depend on agent pods running on the control plane nodes", func(t *testing.T) {
		p := New("me", time.Minute, nil, logr.Discard(), time.Second)
		p.SetTopologyKey(topologyKey)
		p.myTopologyDomain = "zone-a"
		p.updateTopologyDomains(nodes, v1.PodList{}) // no agent pod anywhere
		p.updateControlPlaneNodeDomains(nodes)
		if got := p.GetControlPlaneDomains(); got.InOtherDomain != 2 {
			t.Errorf("expected 2 control plane nodes in other domains without any agent pod, got %+v", got)
		}
	})

	t.Run("a refresh replaces the previous distribution", func(t *testing.T) {
		p := New("me", time.Minute, nil, logr.Discard(), time.Second)
		p.SetTopologyKey(topologyKey)
		p.myTopologyDomain = "zone-a"
		p.updateControlPlaneNodeDomains(nodes)
		p.updateControlPlaneNodeDomains(v1.NodeList{Items: []v1.Node{controlPlane("cp-a", "zone-a")}})
		if got := p.GetControlPlaneDomains(); got != (ControlPlaneDomains{InMyDomain: 1}) {
			t.Errorf("expected only the refreshed node to be known, got %+v", got)
		}
	})
}

// TestTopologyStateIsGuardedByTheMutex reads the failure domain state from another goroutine while Start
// initializes and refreshes it, as the API connectivity check does. Meant to be run with -race.
func TestTopologyStateIsGuardedByTheMutex(t *testing.T) {
	const topologyKey = "topology.kubernetes.io/zone"
	node := func(name, zone string, roles ...string) v1.Node {
		labels := map[string]string{hostnameLabelName: name, topologyKey: zone}
		for _, role := range roles {
			labels[role] = ""
		}
		return v1.Node{ObjectMeta: metav1.ObjectMeta{Name: name, Labels: labels}}
	}
	pod := func(nodeName, ip string) v1.Pod {
		return v1.Pod{
			ObjectMeta: metav1.ObjectMeta{Name: "snr-" + nodeName},
			Spec:       v1.PodSpec{NodeName: nodeName},
			Status:     v1.PodStatus{PodIPs: []v1.PodIP{{IP: ip}}},
		}
	}
	reader := &mockReaderWithPeers{
		ownNodeLabels: map[string]string{hostnameLabelName: "test-hostname", "node-role.kubernetes.io/worker": "", topologyKey: "zone-a"},
		nodes: []v1.Node{
			node("worker-b", "zone-b", "node-role.kubernetes.io/worker"),
			node("cp-a", "zone-a", "node-role.kubernetes.io/master", "node-role.kubernetes.io/control-plane"),
		},
		pods: []v1.Pod{pod("worker-b", "10.0.0.2"), pod("cp-a", "10.0.0.3")},
	}

	p := New("test-node", time.Millisecond, reader, logr.Discard(), time.Second)
	p.SetTopologyKey(topologyKey)

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	readerDone := make(chan struct{})
	go func() {
		defer close(readerDone)
		for ctx.Err() == nil {
			p.GetPeersSnapshot(Worker)
			p.GetControlPlaneDomains()
		}
	}()
	if err := p.Start(ctx); err != nil {
		t.Fatalf("Start() returned an error: %v", err)
	}
	<-readerDone

	if got := p.GetPeersSnapshot(Worker); got.OutsideMyDomain != 1 {
		t.Errorf("expected the worker peer to be outside of our domain, got %+v", got)
	}
	if got := p.GetControlPlaneDomains(); got != (ControlPlaneDomains{InMyDomain: 1}) {
		t.Errorf("expected one control plane node in our domain, got %+v", got)
	}
}
