package reboot

import (
	"os/exec"
	ctrl "sigs.k8s.io/controller-runtime"
	"time"

	"github.com/go-logr/logr"
	"github.com/medik8s/self-node-remediation/pkg/watchdog"
)

type Rebooter interface {
	// Reboot triggers a node reboot
	Reboot() (ctrl.Result, error)
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

func (r *WatchdogRebooter) Reboot() (ctrl.Result, error) {
	if r.isWatchdogRebootStuck() {
		return r.softwareReboot()
	}
	//Watch dog is rebooting, wait to make sure watchdog is rebooting properly otherwise intervene with software reboot
	if r.wd.IsRebooting() {
		r.log.Info("watchdog is waiting for reboot to commence, waiting for 10 seconds")
		return ctrl.Result{RequeueAfter: time.Second * 10}, nil
	}

	if r.wd == nil || !r.wd.IsStarted() {
		r.log.Info("no watchdog is present on this host, trying software reboot")
		//we couldn't init a watchdog so far but requested to be rebooted. we issue a software reboot
		return r.softwareReboot()
	}
	// we stop feeding the watchdog for a reboot
	r.wd.Stop()
	r.log.Info("watchdog feeding has stopped, waiting for reboot to commence")
	return ctrl.Result{RequeueAfter: time.Second * 10}, nil
}

// softwareReboot performs software reboot by running systemctl reboot
func (r *WatchdogRebooter) softwareReboot() (ctrl.Result, error) {
	// hostPID: true and privileged:true required to run this
	rebootCmd := exec.Command("/usr/bin/nsenter", "-m/proc/1/ns/mnt", "/bin/systemctl", "reboot", "--force", "--force")

	if err := rebootCmd.Run(); err != nil {
		r.log.Error(err, "failed to run reboot command")
		// TODO retry because of this?
	}
	return ctrl.Result{}, nil
}

func (r *WatchdogRebooter) isWatchdogRebootStuck() bool {
	timeToAssumeRebootHasStarted := time.Second * 30
	lastFoodTime := r.wd.LastFoodTime()
	var isStuck bool
	now := time.Now()
	if isStuck = now.After(lastFoodTime.Add(timeToAssumeRebootHasStarted)); isStuck {
		r.log.Info("watchdog reboot is stuck, about to try software reboot")
	}

	return isStuck
}
