package watchdog

import (
	"errors"
	"fmt"
	"os"
	"syscall"
	"time"
	"unsafe"
)
import . "golang.org/x/sys/unix"

const (
	watchdogDevice = "/dev/watchdog1"
)

type LinuxWatchdog struct {
	fd   int
	info *watchdogInfo
}

type watchdogInfo struct {
	options         uint32
	firmwareVersion uint32
	identity        [32]byte
}

func IsWatchdogAvailable() bool {
	_, err := os.Stat(watchdogDevice)
	return !os.IsNotExist(err)
}

func StartWatchdog() (*LinuxWatchdog, error) {
	wdFd, err := openDevice()
	if err != nil {
		return nil, fmt.Errorf("failed to open LinuxWatchdog device %s: %v", watchdogDevice, err)
	}

	wd := &LinuxWatchdog{fd: wdFd,
		info: getInfo(wdFd),
	}

	return wd, nil
}

func (wd *LinuxWatchdog) GetTimeout() (*time.Duration, error) {
	timeout, err := IoctlGetInt(wd.fd, WDIOC_GETTIMEOUT)

	if err != nil {
		return nil, err
	}

	timeoutDuration := time.Duration(timeout) * time.Second
	return &timeoutDuration, nil
}

func (wd *LinuxWatchdog) SetTimeout(seconds time.Duration) error {
	if !wd.hasFeature(WDIOF_SETTIMEOUT) {
		return errors.New("LinuxWatchdog device doesn't support timeout changes")
	}

	return IoctlSetPointerInt(
		wd.fd, WDIOC_SETTIMEOUT,
		int(seconds/time.Second))
}

func (wd *LinuxWatchdog) Feed() error {
	food := []byte("a")
	_, err := Write(wd.fd, food)

	return err
}

//Disarm closes the LinuxWatchdog without triggering reboots, even if the LinuxWatchdog will not be fed any more
func (wd *LinuxWatchdog) Disarm() error {
	b := []byte("V") // "V" is a special char for signaling LinuxWatchdog disarm
	_, err := Write(wd.fd, b)

	if err != nil {
		return err
	}

	return Close(wd.fd)
}

func (wd *LinuxWatchdog) hasFeature(value uint32) bool {
	return wd.info != nil && wd.info.options&value == value
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
