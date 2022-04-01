package watchdog

import (
	"time"

	"github.com/go-logr/logr"
)

const (
	fakeTimeout = 1 * time.Second
)

var _ watchdogImpl = &fakeWatchdog{}

// fakeWatchdog provides the fake implementation of the watchdogImpl interface for tests
type fakeWatchdog struct{}

func NewFake(log logr.Logger) (Watchdog, error) {
	return newSynced(log, &fakeWatchdog{}), nil
}

func (f *fakeWatchdog) start() (*time.Duration, error) {
	t := fakeTimeout
	return &t, nil
}

func (f *fakeWatchdog) feed() error {
	return nil
}

func (f *fakeWatchdog) disarm() error {
	return nil
}
