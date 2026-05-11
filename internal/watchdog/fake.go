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
}

// fakeWatchdogImpl provides the fake implementation of the watchdogImpl interface for tests
type fakeWatchdogImpl struct {
	IsStartSuccessful bool
	*synchronizedWatchdog
}

func NewFake(isStartSuccessful bool) FakeWatchdog {
	fakeWDImpl := &fakeWatchdogImpl{IsStartSuccessful: isStartSuccessful}
	syncedWD := newSynced(ctrl.Log.WithName("fake watchdog"), fakeWDImpl)
	fakeWDImpl.synchronizedWatchdog = syncedWD
	return fakeWDImpl
}

func (f *fakeWatchdogImpl) start() (*time.Duration, error) {
	if !f.IsStartSuccessful {
		return nil, errors.New("fakeWatchdogImpl crash on start")
	}
	t := fakeTimeout
	return &t, nil
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
	if swd.status != Armed {
		swd.stop()
		swd.status = Armed
		swd.startFeeding()
	}
}
