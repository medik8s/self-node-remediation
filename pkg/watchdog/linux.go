package watchdog

import (
	"fmt"
	"io/ioutil"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"
	"unsafe"

	"github.com/go-logr/logr"
	"github.com/pkg/errors"
	. "golang.org/x/sys/unix"
)

const (
	watchdogsFolder = "/dev"
	watchdogPrefix  = "watchdog"
)

var (
	watchdogDevice = os.Getenv("WATCHDOG_PATH")
)

// ensure we only have 1 instance
var mutex sync.Mutex
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

func enableSoftdog() error {
	enableSoftdogCmd := exec.Command("/usr/bin/nsenter", "-m/proc/1/ns/mnt", "modprobe", "softdog")
	return enableSoftdogCmd.Run()
}

func checkWatchdogExists(watchdogFilePath string) error {
	if _, err := os.Stat(watchdogFilePath); err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("watchdog device not found: %v", err)
		}
		return fmt.Errorf("failed to check for watchdog device: %v", err)
	}
	return nil
}

func NewLinux(log logr.Logger) (Watchdog, error) {
	mutex.Lock()
	if linuxWatchDogInstantiated {
		mutex.Unlock()
		return nil, fmt.Errorf("linux watchdog already instantiated")
	}

	linuxWatchDogInstantiated = true
	mutex.Unlock()

	if err := checkWatchdogExists(watchdogDevice); err != nil {
		log.Error(err, "watchdog file path couldn't be accessed")
		log.Info("trying to enable softdog")

		if err := enableSoftdog(); err != nil {
			log.Error(err, "failed to enable softdog")
			return nil, err
		}

		newWatchdogDevice, err := getLastModifiedWatchdog()

		if err != nil {
			log.Error(err, "failed to find softdog path")
			return nil, err
		}

		log.Info("auto detected softdog path", "path", newWatchdogDevice)

		if err := checkWatchdogExists(newWatchdogDevice); err != nil {
			log.Error(err, "softdog file path couldn't be accessed")
			return nil, err
		}

		watchdogDevice = newWatchdogDevice
	}

	wd := &linuxWatchdog{
		log: log,
	}

	return newSynced(log, wd), nil
}

// this func returns watchdog path with the latest modification time assuming that
// after softdog enablement
func getLastModifiedWatchdog() (string, error) {
	files, err := ioutil.ReadDir(watchdogsFolder)
	if err != nil {
		return "", errors.Wrap(err, "failed to list watchdogs folder: "+watchdogsFolder)
	}

	maxModTime := time.Time{}
	latestWatchdog := ""
	for _, file := range files {
		if file.IsDir() {
			continue
		}

		if !strings.HasPrefix(file.Name(), watchdogPrefix) {
			continue
		}

		if file.ModTime().After(maxModTime) || file.ModTime().Equal(maxModTime) {
			maxModTime = file.ModTime()
			latestWatchdog = filepath.Join(watchdogsFolder, file.Name())
		}
	}

	if latestWatchdog == "" { //didn't find any watchdog
		return "", errors.New("failed to find softdog path")
	}

	return latestWatchdog, nil
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

// Disarm closes the LinuxWatchdog without triggering reboots, even if the LinuxWatchdog will not be fed any more
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
