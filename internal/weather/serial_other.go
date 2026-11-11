//go:build !linux

package weather

import "errors"

// OpenSerial is only implemented on Linux, where nodes run.
func OpenSerial(_ string, _ int) (Port, error) {
	return nil, errors.New("serial: not supported on this platform")
}
