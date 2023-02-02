package watchdog

import (
	"errors"
	"time"

	ctrl "sigs.k8s.io/controller-runtime"
)

const (
	fakeTimeout = 1 * time.Second
)

var FakeWD *fakeWatchdog

// fakeWatchdog provides the fake implementation of the watchdogImpl interface for tests
type fakeWatchdog struct {
	IsStartSuccessful bool
}

func NewFake(isStartSuccessful bool) (Watchdog, error) {
	FakeWD = &fakeWatchdog{IsStartSuccessful: isStartSuccessful}
	return newSynced(ctrl.Log.WithName("fake watchdog"), FakeWD), nil
}

func (f *fakeWatchdog) start() (*time.Duration, error) {
	if !f.IsStartSuccessful {
		return nil, errors.New("fakeWatchdog crash on start")
	}
	t := fakeTimeout
	return &t, nil
}

func (f *fakeWatchdog) feed() error {
	return nil
}

func (f *fakeWatchdog) disarm() error {
	return nil
}
