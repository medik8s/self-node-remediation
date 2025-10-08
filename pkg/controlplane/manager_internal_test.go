package controlplane

import (
	"testing"

	"github.com/medik8s/self-node-remediation/pkg/peers"
)

type fakePeerResponse struct {
	response peers.Response
	canReach bool
}

func TestIsControlPlaneHealthyFlagsIsolation(t *testing.T) {
	mgr := &Manager{}
	mgr.nodeName = "cp-1"

	resp := peers.Response{IsHealthy: false, Reason: peers.UnHealthyBecauseNodeIsIsolated}
	if healthy := mgr.IsControlPlaneHealthy(resp, false); healthy {
		t.Fatalf("expected isolation to mark node unhealthy when other control planes unreachable")
	}
}

func TestIsControlPlaneHealthyChecksDiagnosticsWhenPeersReportAPIFailure(t *testing.T) {
	mgr := &Manager{}
	mgr.nodeName = "cp-1"

	resp := peers.Response{IsHealthy: true, Reason: peers.HealthyBecauseMostPeersCantAccessAPIServer}
	if healthy := mgr.IsControlPlaneHealthy(resp, true); healthy {
		t.Fatalf("expected diagnostics to influence decision when peers report API outage")
	}
}
