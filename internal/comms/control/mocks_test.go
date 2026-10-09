package control

import (
	"errors"
	"sync"

	"github.com/openmanet/openmanetd/internal/comms/device"
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

// fakeDeviceBinder satisfies DeviceBinder with a fixed Selection. bindErr
// injects a Bind failure. released is signaled (non-blocking) on every
// Release so tests can wait for a loss without polling.
type fakeDeviceBinder struct {
	mu       sync.Mutex
	sel      device.Selection
	bindErr  error
	binds    int
	releases int
	released chan struct{}
}

func newFakeDeviceBinder(hidPath string, card int) *fakeDeviceBinder {
	b := &fakeDeviceBinder{released: make(chan struct{}, 1)}
	b.sel.Device = device.CM108Descriptor{
		HIDPath:     hidPath,
		ALSACardIdx: card,
		SysPath:     "bus/usb/devices/1-1.3",
		VID:         OpenVLMVendorID,
		PID:         OpenVLMProductID,
		IsOpenVLM:   true,
	}

	return b
}

func (b *fakeDeviceBinder) Bind() (device.Selection, error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	b.binds++

	return b.sel, b.bindErr
}

func (b *fakeDeviceBinder) Release(device.Selection) {
	b.mu.Lock()
	b.releases++
	b.mu.Unlock()

	select {
	case b.released <- struct{}{}:
	default:
	}
}

func (b *fakeDeviceBinder) bindCount() int {
	b.mu.Lock()
	defer b.mu.Unlock()

	return b.binds
}

func (b *fakeDeviceBinder) releaseCount() int {
	b.mu.Lock()
	defer b.mu.Unlock()

	return b.releases
}

// fakeEnv is an in-memory device.Environment so tests never touch the real
// process environment.
type fakeEnv struct {
	mu   sync.Mutex
	vars map[string]string
}

func newFakeEnv(kv ...string) *fakeEnv {
	e := &fakeEnv{vars: make(map[string]string, len(kv)/2)}
	for i := 0; i+1 < len(kv); i += 2 {
		e.vars[kv[i]] = kv[i+1]
	}

	return e
}

func (e *fakeEnv) Getenv(k string) string {
	e.mu.Lock()
	defer e.mu.Unlock()

	return e.vars[k]
}

func (e *fakeEnv) Setenv(k, v string) error {
	e.mu.Lock()
	defer e.mu.Unlock()

	e.vars[k] = v

	return nil
}

func (e *fakeEnv) Unsetenv(k string) error {
	e.mu.Lock()
	defer e.mu.Unlock()

	delete(e.vars, k)

	return nil
}
