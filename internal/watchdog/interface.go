package watchdog

import (
	"context"
	"time"
)

// Watchdog is the public facing interface for the watchdog
type Watchdog interface {
	// Start should be called by the manager and block on the given context
	Start(ctx context.Context) error
	// Returns a channel that is closed when initialization is complete,
	// regardless of outcome. Callers should check `Status()` to determine
	// whether the watchdog armed successfully.
	Started() <-chan struct{}
	// Status shows current watchdog status
	Status() watchdogStatus
	// Stop makes the watchdog reboot the node: an armed watchdog is no longer fed, a watchdog on standby is armed
	// and never fed
	Stop()
	// GetTimeout returns the watchdog timeout when it reboots the node without feeding
	GetTimeout() time.Duration
	// LastFoodTime return the last time the watchdog was fed
	LastFoodTime() time.Time
}

// watchdogImpl is the internal interface providing the implementation specific methods of a watchdog
type watchdogImpl interface {
	// start opens (arms) the device and returns its timeout
	start() (*time.Duration, error)
	feed() error
	disarm() error
	// timeout returns the device timeout without opening the device
	timeout() (*time.Duration, error)
}
