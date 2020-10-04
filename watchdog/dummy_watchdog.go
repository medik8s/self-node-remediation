package watchdog

import "time"

type DummyWatchdog struct{}

var LastFoodTime time.Time

func (DummyWatchdog) Feed() error {
	LastFoodTime = time.Now()
	return nil
}

func (DummyWatchdog) GetTimeout() (*time.Duration, error) {
	d := time.Second * 15
	return &d, nil
}

func (DummyWatchdog) SetTimeout(seconds time.Duration) error {
	panic("implement me")
}

func (DummyWatchdog) Disarm() error {
	panic("implement me")
}

func (DummyWatchdog) GetLastFoodTime() time.Time {
	return LastFoodTime
}
