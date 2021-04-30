package watchdog

import "time"

type Watchdog interface {
	Feed() error
	GetTimeout() (*time.Duration, error)
	SetTimeout(seconds time.Duration) error
	Disarm() error
}
