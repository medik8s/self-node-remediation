package watchdog

import (
	"fmt"
	"os"
	"sync"
	"syscall"
	"time"
	"unsafe"

	. "golang.org/x/sys/unix"

	"github.com/go-logr/logr"
)

const (
	watchdogDevice = "/dev/watchdog1"
)

// ensure we only have 1 instance
var once sync.Once
var linuxWatchDogInstantiated = false

var _ watchdogImpl = &linuxWatchdog{}

// linuxWatchdog provides the linux specific implementation of the watchdogImpl interface
type linuxWatchdog struct {
	fd   int
	info *watchdogInfo
	log  logr.Logger
}

type watchdogInfo struct {
	options         uint32
	firmwareVersion uint32
	identity        [32]byte
}

func NewLinux(log logr.Logger) (Watchdog, error) {

	once.Do(func() { linuxWatchDogInstantiated = true })
	if linuxWatchDogInstantiated {
		return nil, fmt.Errorf("linux watchdog already instantiated")
	}

	if _, err := os.Stat(watchdogDevice); err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("watchdog device not found: %v", err)
		}
		return nil, fmt.Errorf("failed to check for watchdog device: %v", err)
	}

	wd := &linuxWatchdog{
		log: log,
	}

	return newSynced(log, wd), nil
}

func (wd *linuxWatchdog) start() (*time.Duration, error) {
	wdFd, err := openDevice()
	if err != nil {
		// Only log the error! Else the pod won't start at all. Users need to check the isStarted flag!
		wd.log.Error(err, fmt.Sprintf("failed to open LinuxWatchdog device %s", watchdogDevice))
		return nil, err
	}

	wd.fd = wdFd
	wd.info = getInfo(wdFd)

	timeout, err := wd.getTimeout()
	if err != nil {
		// no feeding without timeout, so disarm
		_ = wd.disarm()
		// Only log the error! Else the pod won't start at all. Users need to check the isStarted flag!
		wd.log.Error(err, fmt.Sprintf("failed to get timeout of watchdog, disarmed: %s", watchdogDevice))
		return nil, err
	}
	return timeout, nil
}

func (wd *linuxWatchdog) getTimeout() (*time.Duration, error) {
	timeout, err := IoctlGetInt(wd.fd, WDIOC_GETTIMEOUT)
	if err != nil {
		return nil, err
	}
	timeoutDuration := time.Duration(timeout) * time.Second
	return &timeoutDuration, nil
}

func (wd *linuxWatchdog) feed() error {
	food := []byte("a")
	_, err := Write(wd.fd, food)

	return err
}

//Disarm closes the LinuxWatchdog without triggering reboots, even if the LinuxWatchdog will not be fed any more
func (wd *linuxWatchdog) disarm() error {
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
