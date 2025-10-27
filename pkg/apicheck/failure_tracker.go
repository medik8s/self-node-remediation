package apicheck

import "time"

// FailureTracker keeps lightweight state about recent API failures so callers can
// decide when to escalate to peer queries. It tracks the timestamp of the first
// failure in the current observation window and the spacing between subsequent
// failures so intermittent blips do not trigger escalation.
type FailureTracker struct {
	consecutive  int
	firstFailure time.Time
	lastFailure  time.Time
	lastGap      time.Duration
}

// NewFailureTracker creates an empty tracker.
func NewFailureTracker() *FailureTracker {
	return &FailureTracker{}
}

// RecordFailure notes that a failure occurred at the supplied time.
func (ft *FailureTracker) RecordFailure(at time.Time) {
	if ft == nil {
		return
	}

	if ft.consecutive == 0 {
		ft.consecutive = 1
		ft.firstFailure = at
		ft.lastFailure = at
		ft.lastGap = 0
		return
	}

	if ft.lastFailure.IsZero() {
		ft.lastFailure = at
	}

	gap := at.Sub(ft.lastFailure)
	if gap < 0 {
		gap = 0
	}

	ft.lastGap = gap
	ft.lastFailure = at
	ft.consecutive++
}

// Reset clears any recorded failures.
func (ft *FailureTracker) Reset() {
	if ft == nil {
		return
	}

	ft.consecutive = 0
	ft.firstFailure = time.Time{}
	ft.lastFailure = time.Time{}
	ft.lastGap = 0
}

// ShouldEscalate reports whether the accumulated failures warrant escalation.
// When the spacing between the most recent failures exceeds the observation window
// the tracker resets itself, treating the latest failure as the start of a new
// window so intermittent blips do not trip escalation.
func (ft *FailureTracker) ShouldEscalate(now time.Time, window time.Duration) bool {
	if ft == nil || ft.consecutive == 0 {
		return false
	}

	if window <= 0 {
		return true
	}

	// If the most recent failure occurred after a large gap reset the window so
	// intermittent failures outside the observation window do not escalate. Allow a
	// small buffer above the configured window to tolerate scheduling jitter.
	threshold := window + window/2
	if threshold <= 0 {
		threshold = 0
	}
	if threshold > 0 && ft.lastGap >= threshold {
		ft.firstFailure = ft.lastFailure
		ft.consecutive = 1
		ft.lastGap = 0
	}

	if now.Before(ft.firstFailure) {
		return false
	}

	return now.Sub(ft.firstFailure) >= window
}
