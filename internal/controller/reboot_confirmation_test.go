package controller

import "testing"

func TestIsNodeRebootConfirmed(t *testing.T) {
	testCases := []struct {
		name      string
		recorded  string
		current   string
		confirmed bool
	}{
		{name: "new boot ID", recorded: "boot-1", current: "boot-2", confirmed: true},
		{name: "same boot ID, the node did not reboot", recorded: "boot-1", current: "boot-1", confirmed: false},
		{name: "node reports no boot ID yet", recorded: "boot-1", current: "", confirmed: false},
		{name: "nothing recorded, release on time alone", recorded: "", current: "boot-1", confirmed: true},
		{name: "nothing recorded and nothing reported", recorded: "", current: "", confirmed: true},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isNodeRebootConfirmed(tc.recorded, tc.current); got != tc.confirmed {
				t.Errorf("recorded %q, current %q: expected %v, got %v", tc.recorded, tc.current, tc.confirmed, got)
			}
		})
	}
}
