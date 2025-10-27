package controlplane

import "testing"

func TestIsControlPlaneHealthyFlagsIsolation(t *testing.T) {
	mgr := &Manager{}
	mgr.nodeName = "cp-1"

	if healthy := mgr.IsControlPlaneHealthy(EvaluationIsolation); healthy {
		t.Fatalf("expected isolation to mark node unhealthy when other control planes unreachable")
	}
}

func TestIsControlPlaneHealthyChecksDiagnosticsWhenPeersReportAPIFailure(t *testing.T) {
	mgr := &Manager{}
	mgr.nodeName = "cp-1"

	if healthy := mgr.IsControlPlaneHealthy(EvaluationGlobalOutage); healthy {
		t.Fatalf("expected diagnostics to influence decision when peers report API outage")
	}
}
