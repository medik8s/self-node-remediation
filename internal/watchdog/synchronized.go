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
	// Standby: the device was found but is kept closed; it is armed, and never fed, only to reboot the node
	Standby
)

const (
	IsSoftwareRebootEnabledEnvVar = "IS_SOFTWARE_REBOOT_ENABLED"
	// ArmWatchdogOnlyForRebootEnvVar carries SelfNodeRemediationConfig.Spec.ArmWatchdogOnlyForReboot to the agent
	ArmWatchdogOnlyForRebootEnvVar = "ARM_WATCHDOG_ONLY_FOR_REBOOT"
)

type watchdogStatus uint8

// synchronizedWatchdog implements the Watchdog interface with synchronized calls of the implementation specific methods
type synchronizedWatchdog struct {
	impl watchdogImpl
	// armOnlyForReboot keeps the device closed until Stop is called, see Standby
	armOnlyForReboot bool
	timeout          time.Duration
	status           watchdogStatus
	stop             context.CancelFunc
	mutex            sync.Mutex
	lastFoodTime     time.Time
	log              logr.Logger
	started          chan struct{}
}

func newSynced(log logr.Logger, impl watchdogImpl, armOnlyForReboot bool) *synchronizedWatchdog {
	return &synchronizedWatchdog{
		impl:             impl,
		armOnlyForReboot: armOnlyForReboot,
		log:              log,
		status:           Disarmed,
		started:          make(chan struct{}),
	}
}

func (swd *synchronizedWatchdog) Start(ctx context.Context) error {
	if swd.armOnlyForReboot {
		if err := swd.standby(); err != nil {
			return err
		}
		// nothing to feed and nothing to disarm: the device stays closed until Stop arms it for a reboot
		<-ctx.Done()
		return nil
	}

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

// standby publishes the device timeout without opening the device, see Standby
func (swd *synchronizedWatchdog) standby() error {
	swd.mutex.Lock()
	defer swd.mutex.Unlock()

	select {
	case <-swd.started:
		return errors.New("watchdog was started more than once. This is likely to be caused by being added to a manager multiple times")
	default:
	}

	defer close(swd.started)
	timeout, err := swd.impl.timeout()
	if err != nil {
		// the manager falls back to its minimum watchdog timeout when the published one is 0
		swd.log.Error(err, "failed to read the watchdog timeout, publishing none")
	} else {
		swd.timeout = *timeout
	}
	swd.status = Standby
	swd.log.Info("watchdog on standby, it will be armed only to reboot the node", "timeout", swd.timeout)
	return nil
}

// arm opens the device without ever feeding it, so that it reboots the node after its timeout; must hold the mutex
func (swd *synchronizedWatchdog) arm() {
	timeout, err := swd.impl.start()
	if err != nil {
		swd.status = Malfunction
		swd.log.Error(err, "failed to arm the watchdog for the reboot")
		return
	}
	if timeout != nil {
		swd.timeout = *timeout
	}
	// the device counts from the open, which is the last time it was "fed"
	swd.lastFoodTime = time.Now()
	swd.status = Triggered
	swd.log.Info("watchdog armed and not fed, waiting for it to reboot the node", "timeout", swd.timeout)
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
	switch swd.status {
	case Armed:
		swd.stop()
		swd.status = Triggered
	case Standby:
		swd.arm()
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

// IsArmOnlyForRebootEnabled tells whether the watchdog must stay closed until a reboot, from the agent environment;
// an unset variable means no, so that an agent rendered from an older configuration keeps the current behavior
func IsArmOnlyForRebootEnabled() (bool, error) {
	value := os.Getenv(ArmWatchdogOnlyForRebootEnvVar)
	if value == "" {
		return false, nil
	}
	enabled, err := strconv.ParseBool(value)
	if err != nil {
		return false, errors.Wrapf(err, "failed to convert %s env value to boolean. value is: %s", ArmWatchdogOnlyForRebootEnvVar, value)
	}
	return enabled, nil
}

func IsSoftwareRebootEnabled() (bool, error) {
	softwareRebootEnabledEnv := os.Getenv(IsSoftwareRebootEnabledEnvVar)
	softwareRebootEnabled, err := strconv.ParseBool(softwareRebootEnabledEnv)
	if err != nil {
		return false, errors.Wrapf(err, "failed to convert IS_SOFTWARE_REBOOT_ENABLED env value to boolean. value is: %s", softwareRebootEnabledEnv)
	}
	return softwareRebootEnabled, nil
}
