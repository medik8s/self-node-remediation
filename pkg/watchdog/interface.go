package watchdog

import (
	"context"
	"time"
)

// Watchdog is the public facing interface for the watchdog
type Watchdog interface {
	// Start should be called by the manager and block on the given context
	Start(ctx context.Context) error
	// IsStarted return if the watchdog is running
	IsStarted() bool
	// IsRebooting return if the watchdog has already stopped feeding and currently about to reboot the node
	IsRebooting() bool
	// Stop stops feeding the watchdog, which results in a reboot of the node
	Stop()
	// GetTimeout returns the watchdog timeout when it reboots the node without feeding
	GetTimeout() time.Duration
	// LastFoodTime return the last time the watchdog was fed
	LastFoodTime() time.Time
}

// watchdogImpl is the internal interface providing the implementation specific methods of a watchdog
type watchdogImpl interface {
	start() (*time.Duration, error)
	feed() error
	disarm() error
}
