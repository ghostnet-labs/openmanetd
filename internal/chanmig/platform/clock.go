package platform

import "time"

// SystemClock implements chanmig.Clock with the wall clock. Synced
// reports the kernel's NTP discipline state, which ntpd (busybox on
// OpenWrt, or chrony fed by GNSS) clears once it has locked; a node whose
// clock is not disciplined times plan switches relative to receipt.
type SystemClock struct {
	// Probe overrides the sync check (tests, or a deployment that knows
	// better). nil uses the kernel state.
	Probe func() bool
}

// Now implements chanmig.Clock.
func (c SystemClock) Now() time.Time { return time.Now() }

// Synced implements chanmig.Clock.
func (c SystemClock) Synced() bool {
	if c.Probe != nil {
		return c.Probe()
	}

	return kernelClockSynced()
}
