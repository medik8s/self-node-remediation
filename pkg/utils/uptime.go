package utils

import (
	"golang.org/x/sys/unix"
	"time"
)

// GetLinuxUptime returns the uptime of a linux host
func GetLinuxUptime() (time.Duration, error) {
	si := &unix.Sysinfo_t{}

	err := unix.Sysinfo(si)
	if err != nil {
		return 0, err
	}

	uptime := time.Duration(si.Uptime) * time.Second
	return uptime, nil
}
