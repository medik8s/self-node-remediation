package watchdog

import (
	"errors"
	"time"

	ctrl "sigs.k8s.io/controller-runtime"
)

const (
	fakeTimeout = 1 * time.Second
)

// fakeWatchdogImpl provides the fake implementation of the watchdogImpl interface for tests
type fakeWatchdogImpl struct {
	IsStartSuccessful bool
}

func NewFake(isStartSuccessful bool) Watchdog {
	fakeWDImpl := &fakeWatchdogImpl{IsStartSuccessful: isStartSuccessful}
	return newSynced(ctrl.Log.WithName("fake watchdog"), fakeWDImpl)
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
