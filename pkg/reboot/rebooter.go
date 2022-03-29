package reboot

import (
	"os/exec"

	"github.com/go-logr/logr"
	"github.com/medik8s/poison-pill/pkg/watchdog"
)

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
	if r.wd == nil || !r.wd.IsStarted() {
		r.log.Info("no watchdog is present on this host, trying software reboot")
		//we couldn't init a watchdog so far but requested to be rebooted. we issue a software reboot
		if err := r.softwareReboot(); err != nil {
			r.log.Error(err, "failed to run reboot command")
			// TODO retry because of this?
			//return err
			return nil
		}
		return nil
	}
	// we stop feeding the watchdog for a reboot
	r.wd.Stop()
	r.log.Info("watchdog feeding has stopped, waiting for reboot to commence")
	return nil
}

// softwareReboot performs software reboot by running systemctl reboot
func (r *WatchdogRebooter) softwareReboot() error {
	// hostPID: true and privileged:true required to run this
	rebootCmd := exec.Command("/usr/bin/nsenter", "-m/proc/1/ns/mnt", "/bin/systemctl", "reboot", "--force", "--force")
	return rebootCmd.Run()
}
