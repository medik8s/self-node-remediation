package watchdog

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/go-logr/logr"

	"k8s.io/apimachinery/pkg/util/wait"
)

// Watchdog is the public facing interface for the watchdog
type Watchdog interface {
	Start(ctx context.Context) error
	IsStarted() bool
	Stop()
	GetTimeout() time.Duration
	LastFoodTime() time.Time
}

// watchdogImpl is the internal interface providing the implementation specific methods of a watchdog
type watchdogImpl interface {
	start() (*time.Duration, error)
	feed() error
	disarm() error
}

var _ Watchdog = &synchronizedWatchdog{}

// synchronizedWatchdog implements the Watchdog interface with synchronized calls of the implementation specific methods
type synchronizedWatchdog struct {
	impl         watchdogImpl
	timeout      time.Duration
	started      bool
	stop         context.CancelFunc
	stopped      bool
	mutex        sync.Mutex
	lastFoodTime time.Time
	log          logr.Logger
}

func newSynced(log logr.Logger, impl watchdogImpl) *synchronizedWatchdog {
	return &synchronizedWatchdog{
		impl: impl,
		log:  log,
	}
}

func (swd *synchronizedWatchdog) Start(ctx context.Context) error {
	swd.mutex.Lock()
	if swd.started {
		swd.mutex.Unlock()
		return errors.New("watchdog was started more than once. This is likely to be caused by being added to a manager multiple times")
	}
	timeout, err := swd.impl.start()
	if err != nil {
		swd.mutex.Unlock()
		// TODO or return the error and fail the pod's start?
		return nil
	}
	swd.timeout = *timeout
	swd.started = true
	swd.log.Info("watchdog started")
	swd.mutex.Unlock()

	feedCtx, cancel := context.WithCancel(context.Background())
	swd.stop = cancel
	// feed until stopped
	go wait.NonSlidingUntilWithContext(feedCtx, func(feedCtx context.Context) {
		swd.mutex.Lock()
		defer swd.mutex.Unlock()
		// this should not happen because the context is cancelled already.. but just in case
		if swd.stopped {
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
	defer swd.mutex.Unlock()
	if swd.started && !swd.stopped {
		if err := swd.impl.disarm(); err != nil {
			swd.log.Error(err, "failed to disarm watchdog!")
		} else {
			swd.log.Info("disarmed watchdog")
			// we can stop feeding after disarm
			swd.stop()
			swd.stopped = true
		}
	}

	return nil
}

func (swd *synchronizedWatchdog) IsStarted() bool {
	swd.mutex.Lock()
	defer swd.mutex.Unlock()
	return swd.started
}

func (swd *synchronizedWatchdog) Stop() {
	swd.mutex.Lock()
	defer swd.mutex.Unlock()
	if !swd.started || swd.stopped {
		return
	}
	if swd.started {
		swd.stop()
		swd.stopped = true
	}
}

func (swd *synchronizedWatchdog) GetTimeout() time.Duration {
	return swd.timeout
}

func (swd *synchronizedWatchdog) LastFoodTime() time.Time {
	swd.mutex.Lock()
	defer swd.mutex.Unlock()
	return swd.lastFoodTime
}
