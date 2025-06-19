package peers

import (
	"reflect"
	"testing"

	"github.com/go-logr/logr"

	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
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
