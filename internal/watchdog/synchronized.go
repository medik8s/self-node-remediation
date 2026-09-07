package watchdog

import (
	"context"
	"os"
	"strconv"
	"sync"
	"time"

	"github.com/go-logr/logr"
	"github.com/pkg/errors"

	"k8s.io/apimachinery/pkg/util/wait"
)

var _ Watchdog = &synchronizedWatchdog{}

const (
	Disarmed watchdogStatus = iota
	Armed
	Triggered
	Malfunction
)

const (
	IsSoftwareRebootEnabledEnvVar = "IS_SOFTWARE_REBOOT_ENABLED"
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
	started      chan struct{}
}

func newSynced(log logr.Logger, impl watchdogImpl) *synchronizedWatchdog {
	return &synchronizedWatchdog{
		impl:    impl,
		log:     log,
		status:  Disarmed,
		started: make(chan struct{}),
	}
}

func (swd *synchronizedWatchdog) Start(ctx context.Context) error {
	if err := swd.init(); err != nil {
		return err
	}
	if swd.Status() != Armed {
		return nil
	}

	swd.startFeeding()

	<-ctx.Done()

	// pod is being stopped, disarm!
	swd.mutex.Lock()
	defer swd.mutex.Unlock()

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

func (swd *synchronizedWatchdog) init() error {
	swd.mutex.Lock()
	defer swd.mutex.Unlock()

	select {
	case <-swd.started:
		return errors.New("watchdog was started more than once. This is likely to be caused by being added to a manager multiple times")
	default:
	}

	defer close(swd.started)
	timeout, startErr := swd.impl.start()
	if startErr != nil {
		//In case can't use software reboot return an error
		isSoftwareRebootEnabled, err := IsSoftwareRebootEnabled()
		if err != nil {
			return errors.Wrapf(err, "software reboot check failed while handling watchdog start failure: %v", startErr)
		}

		if !isSoftwareRebootEnabled {
			return errors.Wrapf(startErr, "failed to start watchdog, software reboot is disabled")
		} else {
			swd.status = Malfunction
			swd.log.Error(startErr, "error while starting watchdog, reverting to software reboot")
			return nil
		}
	}
	swd.timeout = *timeout
	swd.status = Armed
	swd.log.Info("watchdog started")
	return nil
}

func (swd *synchronizedWatchdog) startFeeding() {
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
}

func (swd *synchronizedWatchdog) Started() <-chan struct{} {
	return swd.started
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

func IsSoftwareRebootEnabled() (bool, error) {
	softwareRebootEnabledEnv := os.Getenv(IsSoftwareRebootEnabledEnvVar)
	softwareRebootEnabled, err := strconv.ParseBool(softwareRebootEnabledEnv)
	if err != nil {
		return false, errors.Wrapf(err, "failed to convert IS_SOFTWARE_REBOOT_ENABLED env value to boolean. value is: %s", softwareRebootEnabledEnv)
	}
	return softwareRebootEnabled, nil
}
