//go:build linux

package platform

import "golang.org/x/sys/unix"

// kernelClockSynced reads the kernel time status without modifying it
// (modes 0, permitted unprivileged). The clock counts as synced when the
// kernel reports no TIME_ERROR state and STA_UNSYNC is clear.
func kernelClockSynced() bool {
	var tx unix.Timex

	state, err := unix.Adjtimex(&tx)
	if err != nil {
		return false
	}

	return state != unix.TIME_ERROR && tx.Status&unix.STA_UNSYNC == 0
}
