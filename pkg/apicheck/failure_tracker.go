package apicheck

import "time"

// FailureTracker keeps lightweight state about recent API failures so callers can
// decide when to escalate to peer queries. The initial implementation is a stub
// and will be fleshed out in subsequent phases.
type FailureTracker struct {
	consecutive  int
	firstFailure time.Time
}

// NewFailureTracker creates an empty tracker.
func NewFailureTracker() *FailureTracker {
	return &FailureTracker{}
}

// RecordFailure notes that a failure occurred at the supplied time.
func (ft *FailureTracker) RecordFailure(_ time.Time) {
	ft.consecutive++
}

// Reset clears any recorded failures.
func (ft *FailureTracker) Reset() {
	ft.consecutive = 0
	ft.firstFailure = time.Time{}
}

// ShouldEscalate reports whether the accumulated failures warrant escalation.
// The stub always returns false; behaviour will be implemented in later phases.
func (ft *FailureTracker) ShouldEscalate(_ time.Time, _ time.Duration) bool {
	return false
}
