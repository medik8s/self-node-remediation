package watchdog

import (
	"errors"
	"time"

	ctrl "sigs.k8s.io/controller-runtime"
)

const (
	fakeTimeout = 1 * time.Second
)

type FakeWatchdog interface {
	Watchdog
	Reset()
	// StartCount returns how many times the device was opened
	StartCount() int
}

// fakeWatchdogImpl provides the fake implementation of the watchdogImpl interface for tests
type fakeWatchdogImpl struct {
	IsStartSuccessful bool
	// IsTimeoutReadable tells whether the device timeout can be read while the device is closed
	IsTimeoutReadable bool
	startCount        int
	*synchronizedWatchdog
}

// NewFake returns a fake watchdog that is armed at start, like the agent's default
func NewFake(isStartSuccessful bool) FakeWatchdog {
	return newFake(isStartSuccessful, false)
}

// NewStandbyFake returns a fake watchdog that is armed only to reboot, see Standby
func NewStandbyFake(isStartSuccessful bool) FakeWatchdog {
	return newFake(isStartSuccessful, true)
}

// NewStandbyFakeWithoutTimeout returns a fake watchdog that is armed only to reboot, and whose timeout cannot be
// read while it is closed
func NewStandbyFakeWithoutTimeout() FakeWatchdog {
	fake := newFake(true, true)
	fake.(*fakeWatchdogImpl).IsTimeoutReadable = false
	return fake
}

func newFake(isStartSuccessful, armOnlyForReboot bool) FakeWatchdog {
	fakeWDImpl := &fakeWatchdogImpl{IsStartSuccessful: isStartSuccessful, IsTimeoutReadable: true}
	syncedWD := newSynced(ctrl.Log.WithName("fake watchdog"), fakeWDImpl, armOnlyForReboot)
	fakeWDImpl.synchronizedWatchdog = syncedWD
	return fakeWDImpl
}

func (f *fakeWatchdogImpl) start() (*time.Duration, error) {
	f.startCount++
	if !f.IsStartSuccessful {
		return nil, errors.New("fakeWatchdogImpl crash on start")
	}
	t := fakeTimeout
	return &t, nil
}

func (f *fakeWatchdogImpl) timeout() (*time.Duration, error) {
	if !f.IsTimeoutReadable {
		return nil, errors.New("fakeWatchdogImpl cannot read the timeout")
	}
	t := fakeTimeout
	return &t, nil
}

func (f *fakeWatchdogImpl) StartCount() int {
	swd := f.synchronizedWatchdog
	swd.mutex.Lock()
	defer swd.mutex.Unlock()
	return f.startCount
}

func (f *fakeWatchdogImpl) feed() error {
	return nil
}

func (f *fakeWatchdogImpl) disarm() error {
	return nil
}

func (f *fakeWatchdogImpl) Reset() {
	swd := f.synchronizedWatchdog
	swd.mutex.Lock()
	defer swd.mutex.Unlock()
	if swd.armOnlyForReboot {
		// back to the closed device, as if the pod had restarted
		if swd.status != Standby {
			if swd.stop != nil {
				swd.stop()
			}
			swd.status = Standby
		}
		return
	}
	if swd.status != Armed {
		swd.stop()
		swd.status = Armed
		swd.startFeeding()
	}
}
