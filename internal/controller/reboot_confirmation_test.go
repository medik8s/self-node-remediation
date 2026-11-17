package controller

import (
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/medik8s/self-node-remediation/v5/api/v1alpha1"
)

func TestIsNodeRebootConfirmed(t *testing.T) {
	now := metav1.Now()
	testCases := []struct {
		name      string
		recorded  string
		confirmed *metav1.Time
		current   string
		expected  bool
	}{
		{name: "new boot ID", recorded: "boot-1", current: "boot-2", expected: true},
		{name: "same boot ID, the node did not reboot", recorded: "boot-1", current: "boot-1", expected: false},
		{name: "node reports no boot ID yet", recorded: "boot-1", current: "", expected: false},
		{name: "nothing recorded, release on time alone", recorded: "", current: "boot-1", expected: true},
		{name: "nothing recorded and nothing reported", recorded: "", current: "", expected: true},
		{name: "agent confirmed the reboot, boot ID unchanged", recorded: "boot-1", confirmed: &now, current: "boot-1", expected: true},
		{name: "agent confirmed the reboot, node not back yet", recorded: "boot-1", confirmed: &now, current: "", expected: true},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			status := v1alpha1.SelfNodeRemediationStatus{NodeBootID: tc.recorded, NodeRebootConfirmedAt: tc.confirmed}
			if got := isNodeRebootConfirmed(status, tc.current); got != tc.expected {
				t.Errorf("recorded %q, confirmed %v, current %q: expected %v, got %v", tc.recorded, tc.confirmed != nil, tc.current, tc.expected, got)
			}
		})
	}
}
