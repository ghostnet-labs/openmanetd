//go:build !linux

package platform

// kernelClockSynced is false off Linux: there is no portable discipline
// probe, and an unsynced node falls back to receipt-relative timing.
func kernelClockSynced() bool { return false }
