package watchdog

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/go-logr/logr"

	"k8s.io/apimachinery/pkg/util/wait"
)

var _ Watchdog = &synchronizedWatchdog{}

const (
	Disarmed watchdogStatus = iota
	Armed
	Triggered
)

type watchdogStatus uint8

// synchronizedWatchdog implements the Watchdog interface with synchronized calls of the implementation specific methods
type synchronizedWatchdog struct {
	impl         watchdogImpl
	timeout      time.Duration
	status       watchdogStatus
	stop         context.CancelFunc
	mutex        sync.Mutex
	lastFoodTime time.Time
	log          logr.Logger
}

func newSynced(log logr.Logger, impl watchdogImpl) *synchronizedWatchdog {
	return &synchronizedWatchdog{
		impl:   impl,
		log:    log,
		status: Disarmed,
	}
}

func (swd *synchronizedWatchdog) Start(ctx context.Context) error {
	swd.mutex.Lock()
	defer swd.mutex.Unlock()
	if swd.status != Disarmed {
		return errors.New("watchdog was started more than once. This is likely to be caused by being added to a manager multiple times")
	}
	timeout, err := swd.impl.start()
	if err != nil {
		// TODO or return the error and fail the pod's start?

		return nil
	}
	swd.timeout = *timeout
	swd.status = Armed
	swd.log.Info("watchdog started")
	swd.mutex.Unlock()

	feedCtx, cancel := context.WithCancel(context.Background())
	swd.stop = cancel
	// feed until stopped
	go wait.NonSlidingUntilWithContext(feedCtx, func(feedCtx context.Context) {
		swd.mutex.Lock()
		defer swd.mutex.Unlock()
		// prevent feeding of a disarmed watchdog in case the context isn't cancelled yet
		if swd.status != Armed {
			return
		}
		if err := swd.impl.feed(); err != nil {
			swd.log.Error(err, "failed to feed watchdog!")
		} else {
			swd.lastFoodTime = time.Now()
		}
	}, swd.timeout/3)

	<-ctx.Done()

	// pod is being stopped, disarm!
	swd.mutex.Lock()
	if swd.status == Armed {
		if err := swd.impl.disarm(); err != nil {
			swd.log.Error(err, "failed to disarm watchdog!")
		} else {
			swd.log.Info("disarmed watchdog")
			// we can stop feeding after disarm
			swd.stop()
			swd.status = Disarmed
		}
	}

	return nil
}

func (swd *synchronizedWatchdog) Stop() {
	swd.mutex.Lock()
	defer swd.mutex.Unlock()
	if swd.status == Armed {
		swd.stop()
		swd.status = Triggered
	}
}

func (swd *synchronizedWatchdog) GetTimeout() time.Duration {
	swd.mutex.Lock()
	defer swd.mutex.Unlock()
	return swd.timeout
}

func (swd *synchronizedWatchdog) LastFoodTime() time.Time {
	swd.mutex.Lock()
	defer swd.mutex.Unlock()
	return swd.lastFoodTime
}

func (swd *synchronizedWatchdog) Status() watchdogStatus {
	swd.mutex.Lock()
	defer swd.mutex.Unlock()
	return swd.status
}
