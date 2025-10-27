package apicheck

import (
	"testing"
	"time"
)

func TestFailureTrackerEscalatesAfterWindow(t *testing.T) {
	tracker := NewFailureTracker()
	window := 3 * time.Second
	start := time.Unix(0, 0)

	tracker.RecordFailure(start)
	if tracker.ShouldEscalate(start, window) {
		t.Fatalf("unexpected escalation after initial failure")
	}

	tracker.RecordFailure(start.Add(1 * time.Second))
	if tracker.ShouldEscalate(start.Add(1*time.Second), window) {
		t.Fatalf("unexpected escalation before window elapsed")
	}

	tracker.RecordFailure(start.Add(window))
	if !tracker.ShouldEscalate(start.Add(window), window) {
		t.Fatalf("expected escalation after sustained failures")
	}
}

func TestFailureTrackerResetClearsState(t *testing.T) {
	tracker := NewFailureTracker()
	window := 2 * time.Second
	start := time.Unix(0, 0)

	tracker.RecordFailure(start)
	tracker.RecordFailure(start.Add(1 * time.Second))
	if tracker.ShouldEscalate(start.Add(1*time.Second), window) {
		t.Fatalf("unexpected escalation before reset")
	}

	tracker.Reset()

	tracker.RecordFailure(start.Add(3 * time.Second))
	if tracker.ShouldEscalate(start.Add(3*time.Second), window) {
		t.Fatalf("reset should clear escalation state")
	}
}

func TestFailureTrackerIntermittentFailuresDoNotEscalate(t *testing.T) {
	tracker := NewFailureTracker()
	window := 2 * time.Second
	start := time.Unix(0, 0)

	tracker.RecordFailure(start)
	if tracker.ShouldEscalate(start, window) {
		t.Fatalf("unexpected escalation on first intermittent failure")
	}

	// next failure occurs after the window, so escalation should not trigger
	tracker.RecordFailure(start.Add(3 * time.Second))
	if tracker.ShouldEscalate(start.Add(3*time.Second), window) {
		t.Fatalf("intermittent failures should not escalate")
	}
}
