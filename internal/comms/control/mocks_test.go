package control

import (
	"errors"
)

// fakeDisconnectHIDDevice returns queued reports, then a disconnect error.
// Channel synchronization keeps the fake safe without shared mutable fields.
type fakeDisconnectHIDDevice struct {
	reports <-chan []byte
}

func (d *fakeDisconnectHIDDevice) Read(buf []byte) (int, error) {
	report, ok := <-d.reports
	if !ok {
		return 0, errors.New("USB device disconnected")
	}

	return copy(buf, report), nil
}

func (*fakeDisconnectHIDDevice) Close() error { return nil }
