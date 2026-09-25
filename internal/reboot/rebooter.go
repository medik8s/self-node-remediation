package reboot

import (
	"errors"
	"os/exec"
	"time"

	"github.com/go-logr/logr"

	"github.com/medik8s/self-node-remediation/v5/internal/watchdog"
)

const TimeToAssumeRebootHasStarted = time.Second * 30

type Rebooter interface {
	// Reboot triggers a node reboot
	Reboot() error
}

var _ Rebooter = &watchdogRebooter{}
var _ Rebooter = &disabledRebooter{}

// disabledRebooter fails closed when watchdog initialization fails and
// software reboot is explicitly disabled.
type disabledRebooter struct {
	err error
}

// NewDisabledRebooter returns a rebooter that never attempts a reboot.
func NewDisabledRebooter(err error) Rebooter {
	return &disabledRebooter{err: err}
}

func (r *disabledRebooter) Reboot() error {
	return r.err
}

// watchdogRebooter uses a watchdog for triggering reboots
type watchdogRebooter struct {
	wd                 watchdog.Watchdog
	log                logr.Logger
	softwareRebootHook func() error
}

func NewWatchdogRebooter(wd watchdog.Watchdog, log logr.Logger) Rebooter {
	wdRebooter := &watchdogRebooter{
		wd:  wd,
		log: log,
	}
	wdRebooter.softwareRebootHook = wdRebooter.softwareReboot
	return wdRebooter
}

func (r *watchdogRebooter) Reboot() error {
	if r.wd == nil {
		r.log.Info("no watchdog is present on this host, trying software reboot")
		//we couldn't init a watchdog so far but requested to be rebooted. we issue a software reboot
		return r.softwareRebootHook()
	} else if r.wd.Status() == watchdog.Malfunction {
		r.log.Info("watchdog is malfunctioning on this host, trying software reboot")
		return r.softwareRebootHook()
	}

	//Watch dog is rebooting, wait to make sure watchdog is rebooting properly otherwise intervene with software reboot
	switch r.wd.Status() {
	case watchdog.Triggered:
		r.log.Info("watchdog is triggered, waiting for watchdog reboot to commence")
		if r.isWatchdogRebootStuck() {
			return r.softwareRebootHook()
		}
		return nil
	case watchdog.Disarmed:
		r.log.Info("watchdog failed to start, trying software reboot")
		return r.softwareRebootHook()
	case watchdog.Armed:
		// we stop feeding the watchdog for a reboot
		r.wd.Stop()
		r.log.Info("watchdog feeding has stopped, waiting for reboot to commence")
		return nil
	default:
		err := errors.New("unexpected watchdog status")
		r.log.Error(err, err.Error(), "watchDog status", r.wd.Status())
		return err
	}
}

// softwareReboot attempts multiple reboot methods in order, stopping at the
// first that succeeds. reboot(2)-based methods are tried before sysrq because
// they respect PID namespace isolation; sysrq bypasses it and is a last resort.
func (r *watchdogRebooter) softwareReboot() error {
	r.log.Info("about to try software reboot")
	cmds := [][]string{
		{"systemctl", "reboot", "--force", "--force"},
		{"nsenter", "-m/proc/1/ns/mnt", "--", "systemctl", "reboot", "--force", "--force"},
		{"reboot", "-f"},
		{"nsenter", "-m/proc/1/ns/mnt", "--", "reboot", "-f"},
		{"nsenter", "-m/proc/1/ns/mnt", "/bin/bash", "-c", "echo b > /proc/sysrq-trigger"},
	}
	for _, cmd := range cmds {
		r.log.Info("trying reboot command", "command", cmd)
		if err := exec.Command(cmd[0], cmd[1:]...).Run(); err != nil {
			r.log.Error(err, "reboot command failed, trying next", "command", cmd)
			continue
		}
		return nil
	}
	return nil
}

func (r *watchdogRebooter) isWatchdogRebootStuck() bool {
	lastFoodTime := r.wd.LastFoodTime()
	timeElapsedSinceLastFeed := time.Now().Sub(lastFoodTime)
	var isStuck bool
	if isStuck = timeElapsedSinceLastFeed > TimeToAssumeRebootHasStarted; isStuck {
		r.log.Info("watchdog reboot is stuck, too long has passed since last feed time", "time passed since last feed", timeElapsedSinceLastFeed)
	}

	return isStuck
}
