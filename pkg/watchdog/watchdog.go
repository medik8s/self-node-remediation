package watchdog

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"
	"syscall"
	"time"
	"unsafe"

	"github.com/go-logr/logr"
	. "golang.org/x/sys/unix"

	"k8s.io/apimachinery/pkg/util/wait"
)

const (
	watchdogDevice = "/dev/watchdog1"
	fakeTimeout    = 1 * time.Second
)

var _ Watchdog = &linuxWatchdog{}

type linuxWatchdog struct {
	fd           int
	info         *watchdogInfo
	timeout      time.Duration
	started      bool
	stop         context.CancelFunc
	stopped      bool
	mutex        sync.Mutex
	lastFoodTime time.Time
	log          logr.Logger
	fake         bool
}

type watchdogInfo struct {
	options         uint32
	firmwareVersion uint32
	identity        [32]byte
}

func NewFake(log logr.Logger) (Watchdog, error) {
	wd := &linuxWatchdog{
		mutex: sync.Mutex{},
		log:   log,
		fake:  true,
	}
	return wd, nil
}

func New(log logr.Logger) (Watchdog, error) {

	if _, err := os.Stat(watchdogDevice); err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("watchdog device not found: %v", err)
		}
		return nil, fmt.Errorf("failed to check for watchdog device: %v", err)
	}

	wd := &linuxWatchdog{
		mutex: sync.Mutex{},
		log:   log,
		fake:  false,
	}

	return wd, nil
}

func (wd *linuxWatchdog) Start(ctx context.Context) error {
	wd.mutex.Lock()
	if wd.started {
		wd.mutex.Unlock()
		return errors.New("watchdog was started more than once. This is likely to be caused by being added to a manager multiple times")
	}
	if err := wd.start(); err != nil {
		wd.mutex.Unlock()
		// TODO or return the error and fail the pod's start?
		return nil
	}
	wd.started = true
	wd.mutex.Unlock()

	feedCtx, cancel := context.WithCancel(context.Background())
	wd.stop = cancel

	// feed until stopped
	go wait.NonSlidingUntilWithContext(feedCtx, func(feedCtx context.Context) {
		wd.mutex.Lock()
		defer wd.mutex.Unlock()
		// this should not happen because the context is cancelled already.. but just in case
		if wd.stopped {
			return
		}
		if err := wd.feed(); err != nil {
			wd.log.Error(err, "failed to feed watchdog!")
		} else {
			wd.lastFoodTime = time.Now()
		}
	}, wd.timeout/3)

	wd.log.Info("watchdog started")

	<-ctx.Done()

	// pod is being stopped, disarm!
	wd.mutex.Lock()
	defer wd.mutex.Unlock()
	if wd.started && !wd.stopped {
		if err := wd.disarm(); err != nil {
			wd.log.Error(err, "failed to disarm watchdog!")
		} else {
			wd.log.Info("disarmed watchdog")
			// we can stop feeding after disarm
			wd.stop()
			wd.stopped = true
		}
	}
	return nil
}

func (wd *linuxWatchdog) start() error {
	if wd.fake {
		wd.timeout = fakeTimeout
		return nil
	}

	wdFd, err := openDevice()
	if err != nil {
		// Only log the error! Else the pod won't start at all. Users need to check the started flag!
		wd.log.Error(err, fmt.Sprintf("failed to open LinuxWatchdog device %s", watchdogDevice))
		return err
	}

	wd.fd = wdFd
	wd.info = getInfo(wdFd)

	timeout, err := wd.getTimeout()
	if err != nil {
		// no feeding without timeout, so disarm
		_ = wd.disarm()
		// Only log the error! Else the pod won't start at all. Users need to check the started flag!
		wd.log.Error(err, fmt.Sprintf("failed to get timeout of watchdog, disarmed: %s", watchdogDevice))
		return err
	}
	wd.timeout = *timeout
	return nil
}

func (wd *linuxWatchdog) IsStarted() bool {
	wd.mutex.Lock()
	defer wd.mutex.Unlock()
	return wd.started
}

func (wd *linuxWatchdog) Stop() {
	wd.mutex.Lock()
	defer wd.mutex.Unlock()
	if !wd.started || wd.stopped {
		return
	}
	if wd.started {
		wd.stop()
		wd.stopped = true
	}
}

func (wd *linuxWatchdog) LastFoodTime() time.Time {
	wd.mutex.Lock()
	defer wd.mutex.Unlock()
	return wd.lastFoodTime
}

func (wd *linuxWatchdog) getTimeout() (*time.Duration, error) {
	timeout, err := IoctlGetInt(wd.fd, WDIOC_GETTIMEOUT)
	if err != nil {
		return nil, err
	}
	timeoutDuration := time.Duration(timeout) * time.Second
	return &timeoutDuration, nil
}

func (wd *linuxWatchdog) GetTimeout() time.Duration {
	return wd.timeout
}

func (wd *linuxWatchdog) feed() error {
	if wd.fake {
		return nil
	}

	food := []byte("a")
	_, err := Write(wd.fd, food)

	return err
}

//Disarm closes the LinuxWatchdog without triggering reboots, even if the LinuxWatchdog will not be fed any more
func (wd *linuxWatchdog) disarm() error {
	if wd.fake {
		return nil
	}

	b := []byte("V") // "V" is a special char for signaling LinuxWatchdog disarm
	_, err := Write(wd.fd, b)

	if err != nil {
		return err
	}

	return Close(wd.fd)
}

func getInfo(fd int) *watchdogInfo {
	info := watchdogInfo{}

	_, _, errNo := syscall.Syscall(
		syscall.SYS_IOCTL, uintptr(fd),
		WDIOC_GETSUPPORT, uintptr(unsafe.Pointer(&info)))

	if errNo != 0 {
		return nil
	}

	return &info
}

func openDevice() (int, error) {
	return Open(watchdogDevice, O_WRONLY, 0644)
}
