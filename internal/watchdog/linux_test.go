package watchdog

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/go-logr/logr"
)

// writeTimeout creates <root>/<name>/timeout holding the given seconds
func writeTimeout(t *testing.T, root, name, seconds string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(root, name), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, name, "timeout"), []byte(seconds+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestTimeoutFindsDeviceBySysfsName checks that the timeout of the configured device is read from its own sysfs entry:
// the legacy watchdog node maps to watchdog0, watchdogN to watchdogN, and a symlink to the device it points to.
func TestTimeoutFindsDeviceBySysfsName(t *testing.T) {
	devDir := t.TempDir()
	sysfsDir := t.TempDir()
	for _, name := range []string{"watchdog", "watchdog0", "watchdog1"} {
		if err := os.WriteFile(filepath.Join(devDir, name), nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(filepath.Join(devDir, "watchdog1"), filepath.Join(devDir, "my-watchdog")); err != nil {
		t.Fatal(err)
	}
	writeTimeout(t, sysfsDir, "watchdog0", "60")
	writeTimeout(t, sysfsDir, "watchdog1", "90")

	t.Setenv("WATCHDOG_PATH", "")
	origDevice, origSysfs := watchdogDevice, sysfsWatchdogFolder
	t.Cleanup(func() { watchdogDevice, sysfsWatchdogFolder = origDevice, origSysfs })
	sysfsWatchdogFolder = sysfsDir

	testCases := []struct {
		device   string
		expected time.Duration
	}{
		{device: "watchdog", expected: 60 * time.Second},
		{device: "watchdog0", expected: 60 * time.Second},
		{device: "watchdog1", expected: 90 * time.Second},
		{device: "my-watchdog", expected: 90 * time.Second},
	}
	wd := &linuxWatchdog{log: logr.Discard()}
	for _, tc := range testCases {
		t.Run(tc.device, func(t *testing.T) {
			watchdogDevice = filepath.Join(devDir, tc.device)
			got, err := wd.timeout()
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if *got != tc.expected {
				t.Errorf("expected %s, got %s", tc.expected, *got)
			}
		})
	}
}

// TestTimeoutFailsWithoutSysfsEntry checks that a device without a readable sysfs timeout yields an error rather
// than a zero timeout.
func TestTimeoutFailsWithoutSysfsEntry(t *testing.T) {
	devDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(devDir, "watchdog2"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	origDevice, origSysfs := watchdogDevice, sysfsWatchdogFolder
	t.Cleanup(func() { watchdogDevice, sysfsWatchdogFolder = origDevice, origSysfs })
	sysfsWatchdogFolder = t.TempDir()

	wd := &linuxWatchdog{log: logr.Discard()}
	for _, device := range []string{"watchdog2", "missing"} {
		watchdogDevice = filepath.Join(devDir, device)
		if got, err := wd.timeout(); err == nil {
			t.Errorf("%s: expected an error, got timeout %s", device, *got)
		}
	}
}
