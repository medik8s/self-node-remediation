package reboot

import (
	"errors"
	"os/exec"
	"time"

	"github.com/go-logr/logr"
	"github.com/medik8s/self-node-remediation/pkg/watchdog"
)

const TimeToAssumeRebootHasStarted = time.Second * 30

type Rebooter interface {
	// Reboot triggers a node reboot
	Reboot() error
}

var _ Rebooter = &WatchdogRebooter{}

// WatchdogRebooter uses a watchdog for triggering reboots
type WatchdogRebooter struct {
	wd  watchdog.Watchdog
	log logr.Logger
}

func NewWatchdogRebooter(wd watchdog.Watchdog, log logr.Logger) Rebooter {
	return &WatchdogRebooter{
		wd:  wd,
		log: log,
	}
}

func (r *WatchdogRebooter) Reboot() error {
	if r.wd == nil {
		r.log.Info("no watchdog is present on this host, trying software reboot")
		//we couldn't init a watchdog so far but requested to be rebooted. we issue a software reboot
		return r.softwareReboot()
	}

	if r.isWatchdogRebootStuck() {
		return r.softwareReboot()
	}
	//Watch dog is rebooting, wait to make sure watchdog is rebooting properly otherwise intervene with software reboot
	switch r.wd.Status() {
	case watchdog.Triggered:
		r.log.Info("watchdog is triggered, waiting for watchdog reboot to commence")
		return nil
	case watchdog.Disarmed:
		r.log.Info("watchdog failed to start, trying software reboot")
		return r.softwareReboot()
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

// softwareReboot performs software reboot by running systemctl reboot
func (r *WatchdogRebooter) softwareReboot() error {
	r.log.Info("about to try software reboot")
	// hostPID: true and privileged:true required to run this
	rebootCmd := exec.Command("/usr/bin/nsenter", "-m/proc/1/ns/mnt", "/bin/systemctl", "reboot", "--force", "--force")

	if err := rebootCmd.Run(); err != nil {
		r.log.Error(err, "failed to run reboot command")
		// TODO retry because of this?
	}
	return nil
}

func (r *WatchdogRebooter) isWatchdogRebootStuck() bool {
	lastFoodTime := r.wd.LastFoodTime()
	timeElapsedSinceLastFeed := time.Now().Sub(lastFoodTime)
	var isStuck bool
	if isStuck = timeElapsedSinceLastFeed > TimeToAssumeRebootHasStarted; isStuck {
		r.log.Info("watchdog reboot is stuck, too long has passed since last feed time", "time passed since last feed", timeElapsedSinceLastFeed)
	}

	return isStuck
}
