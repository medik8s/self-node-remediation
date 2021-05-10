package watchdog

import (
	"context"
	"time"
)

type Watchdog interface {
	Start(ctx context.Context) error
	IsStarted() bool
	Stop()
	GetTimeout() time.Duration
	LastFoodTime() time.Time
}
