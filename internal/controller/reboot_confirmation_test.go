package controller

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	"github.com/medik8s/self-node-remediation/v5/api/v1alpha1"
)

func TestIsNodeRebootConfirmed(t *testing.T) {
	now := metav1.Now()
	testCases := []struct {
		name          string
		recorded      string
		confirmed     *metav1.Time
		current       string
		proofRequired bool
		expected      bool
	}{
		{name: "new boot ID", recorded: "boot-1", current: "boot-2", expected: true},
		{name: "same boot ID, the node did not reboot", recorded: "boot-1", current: "boot-1", expected: false},
		{name: "node reports no boot ID yet", recorded: "boot-1", current: "", expected: false},
		{name: "nothing recorded, release on time alone", recorded: "", current: "boot-1", expected: true},
		{name: "nothing recorded and nothing reported", recorded: "", current: "", expected: true},
		{name: "agent confirmed the reboot, boot ID unchanged", recorded: "boot-1", confirmed: &now, current: "boot-1", expected: true},
		{name: "agent confirmed the reboot, node not back yet", recorded: "boot-1", confirmed: &now, current: "", expected: true},
		{name: "proof required, new boot ID", recorded: "boot-1", current: "boot-2", proofRequired: true, expected: true},
		{name: "proof required, same boot ID", recorded: "boot-1", current: "boot-1", proofRequired: true, expected: false},
		{name: "proof required, nothing recorded, a boot ID reported now", recorded: "", current: "boot-1", proofRequired: true, expected: false},
		{name: "proof required, nothing recorded and nothing reported", recorded: "", current: "", proofRequired: true, expected: false},
		{name: "proof required, nothing recorded, agent confirmed the reboot", recorded: "", confirmed: &now, current: "", proofRequired: true, expected: true},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			status := v1alpha1.SelfNodeRemediationStatus{NodeBootID: tc.recorded, NodeRebootConfirmedAt: tc.confirmed}
			if got := isNodeRebootConfirmed(status, tc.current, tc.proofRequired); got != tc.expected {
				t.Errorf("recorded %q, confirmed %v, current %q, proof required %v: expected %v, got %v", tc.recorded, tc.confirmed != nil, tc.current, tc.proofRequired, tc.expected, got)
			}
		})
	}
}

func TestIsRemediatedNode(t *testing.T) {
	testCases := []struct {
		name     string
		recorded string
		current  string
		expected bool
	}{
		{name: "same UID, the node the reboot was prepared for", recorded: "uid-1", current: "uid-1", expected: true},
		{name: "another UID, a node that replaced it under the same name", recorded: "uid-1", current: "uid-2", expected: false},
		{name: "nothing recorded, prepared by an older manager", recorded: "", current: "uid-1", expected: true},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			status := v1alpha1.SelfNodeRemediationStatus{NodeUID: tc.recorded}
			node := &corev1.Node{}
			node.UID = types.UID(tc.current)
			if got := isRemediatedNode(status, node); got != tc.expected {
				t.Errorf("recorded %q, current %q: expected %v, got %v", tc.recorded, tc.current, tc.expected, got)
			}
		})
	}
}

func TestIsRebootProofRequired(t *testing.T) {
	boolPtr := func(b bool) *bool { return &b }
	testCases := []struct {
		name     string
		pinned   *bool
		armedNow bool
		expected bool
	}{
		{name: "not recorded, the option is off", pinned: nil, armedNow: false, expected: false},
		{name: "not recorded, the option is on", pinned: nil, armedNow: true, expected: true},
		{name: "prepared with the option on, turned off since", pinned: boolPtr(true), armedNow: false, expected: true},
		{name: "prepared with the option on, still on", pinned: boolPtr(true), armedNow: true, expected: true},
		{name: "prepared with the option off, turned on since", pinned: boolPtr(false), armedNow: true, expected: false},
		{name: "prepared with the option off, still off", pinned: boolPtr(false), armedNow: false, expected: false},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			status := v1alpha1.SelfNodeRemediationStatus{RebootProofRequired: tc.pinned}
			snrConfig := &v1alpha1.SelfNodeRemediationConfig{}
			snrConfig.Spec.ArmWatchdogOnlyForReboot = tc.armedNow
			if got := isRebootProofRequired(status, snrConfig); got != tc.expected {
				t.Errorf("pinned %v, armed now %v: expected %v, got %v", tc.pinned, tc.armedNow, tc.expected, got)
			}
		})
	}
}
