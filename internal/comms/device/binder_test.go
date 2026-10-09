package device_test

import (
	"sync"
	"testing"
	"testing/fstest"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/openmanet/openmanetd/internal/comms/device"
)

func newTestBinder(fsys fstest.MapFS, probe *fakeProbe, env *fakeEnv) *device.Binder {
	return device.NewBinder(device.BinderConfig{FS: fsys, Probe: probe.read, Env: env})
}

// fired reports whether ch is closed.
func fired(ch <-chan struct{}) bool {
	select {
	case <-ch:
		return true
	default:
		return false
	}
}

func TestBinder_pairsHIDAndCardFromOneUSBParent(t *testing.T) {
	// A generic CM108 headset sits on a lower USB path with its own card;
	// the OpenVLM's hidraw and card must come from its own parent.
	fsys := fakeSysFS(
		sysDevice{name: "1-1.1", product: "013c", serial: "GEN", hidraw: 0, card: 1},
		sysDevice{name: "1-1.3", product: "0012", serial: "VLM", hidraw: 2, card: 3},
	)
	env := newFakeEnv()
	b := newTestBinder(fsys, newFakeProbe(hidraw(2)), env)

	sel, err := b.Bind()
	require.NoError(t, err)
	assert.Equal(t, "bus/usb/devices/1-1.3", sel.Device.SysPath)
	assert.Equal(t, hidraw(2), sel.Device.HIDPath)
	assert.Equal(t, 3, sel.Device.ALSACardIdx)
	assert.True(t, sel.Device.IsOpenVLM)
	assert.Equal(t, "3", env.Getenv(device.ALSACardEnv))
}

func TestBinder_stickyAcrossRescanAndRenumber(t *testing.T) {
	fsys := fakeSysFS(sysDevice{name: "1-1.3", product: "0012", serial: "VLM", hidraw: 2, card: 3})
	env := newFakeEnv()
	probe := newFakeProbe(hidraw(2))
	b := newTestBinder(fsys, probe, env)

	_, err := b.Bind()
	require.NoError(t, err)

	changed := b.Changed()

	// A second OpenVLM appears on a lower path: the binding must not jump.
	for k, v := range fakeSysFS(sysDevice{name: "1-1.1", product: "0012", serial: "VLM2", hidraw: 0, card: 1}) {
		fsys[k] = v
	}

	probe.setStrapped(hidraw(0), hidraw(2))

	sel, err := b.Bind()
	require.NoError(t, err)
	assert.Equal(t, "bus/usb/devices/1-1.3", sel.Device.SysPath)
	assert.False(t, fired(changed), "unchanged binding must not signal")

	// Same parent, renumbered card (kernel re-enumeration).
	delete(fsys, "bus/usb/devices/1-1.3/1-1.3:1.0/sound/card3/id")
	fsys["bus/usb/devices/1-1.3/1-1.3:1.0/sound/card6/id"] = &fstest.MapFile{Data: []byte("Device\n")}

	sel, err = b.Bind()
	require.NoError(t, err)
	assert.Equal(t, 6, sel.Device.ALSACardIdx)
	assert.True(t, fired(changed), "renumbered card must signal")
	assert.Equal(t, "6", env.Getenv(device.ALSACardEnv))
}

func TestBinder_lossClearsAutoCardAndRebinds(t *testing.T) {
	fsys := fakeSysFS(sysDevice{name: "1-1.3", product: "0012", serial: "VLM", hidraw: 2, card: 3})
	env := newFakeEnv()
	b := newTestBinder(fsys, newFakeProbe(hidraw(2), hidraw(5)), env)

	sel, err := b.Bind()
	require.NoError(t, err)

	changed := b.Changed()

	b.Release(sel)

	_, bound := b.Current()
	assert.False(t, bound)
	assert.True(t, fired(changed))
	assert.Empty(t, env.Getenv(device.ALSACardEnv), "a stale auto-selected card must be cleared")

	// Unplugged: Bind fails and env stays clear.
	for k := range fsys {
		delete(fsys, k)
	}

	_, err = b.Bind()
	require.ErrorIs(t, err, device.ErrNoCM108)
	assert.Empty(t, env.Getenv(device.ALSACardEnv))

	// Replugged with new hidraw and card numbers.
	for k, v := range fakeSysFS(sysDevice{name: "1-1.3", product: "0012", serial: "VLM", hidraw: 5, card: 7}) {
		fsys[k] = v
	}

	sel, err = b.Bind()
	require.NoError(t, err)
	assert.Equal(t, hidraw(5), sel.Device.HIDPath)
	assert.Equal(t, "7", env.Getenv(device.ALSACardEnv))

	var snap device.BindingSnapshot

	b.Snapshot(&snap)
	assert.True(t, snap.Bound)
	assert.Equal(t, 7, snap.ALSACard)
	assert.Equal(t, int64(2), snap.Binds)
	assert.Equal(t, int64(1), snap.Losses)
	assert.Equal(t, int64(1), snap.BindFailures)
}

func TestBinder_bindFailureWhileBoundCountsAsLoss(t *testing.T) {
	fsys := fakeSysFS(sysDevice{name: "1-1.3", product: "0012", serial: "VLM", hidraw: 2, card: 3})
	env := newFakeEnv()
	b := newTestBinder(fsys, newFakeProbe(hidraw(2)), env)

	_, err := b.Bind()
	require.NoError(t, err)

	for k := range fsys {
		delete(fsys, k)
	}

	_, err = b.Bind()
	require.Error(t, err)

	_, bound := b.Current()
	assert.False(t, bound)
	assert.Empty(t, env.Getenv(device.ALSACardEnv))
}

func TestBinder_differentDeviceOnSamePortIsReprobed(t *testing.T) {
	fsys := fakeSysFS(sysDevice{name: "1-1.3", product: "0012", serial: "VLM", hidraw: 2, card: 3})
	probe := newFakeProbe(hidraw(2))
	b := newTestBinder(fsys, probe, newFakeEnv())

	_, err := b.Bind()
	require.NoError(t, err)

	// A generic CM108 with another serial replaces it on the same port
	// without the HID reader noticing (roip mode): the stale identity must
	// not be reused.
	for k := range fsys {
		delete(fsys, k)
	}

	for k, v := range fakeSysFS(sysDevice{name: "1-1.3", product: "013c", serial: "GEN", hidraw: 2, card: 3}) {
		fsys[k] = v
	}

	probe.setStrapped()

	sel, err := b.Bind()
	require.NoError(t, err)
	assert.False(t, sel.Device.IsOpenVLM)
	assert.Equal(t, "GEN", sel.Device.Serial)
}

func TestBinder_operatorOverrideIsNeverTouched(t *testing.T) {
	fsys := fakeSysFS(
		sysDevice{name: "1-1.1", product: "013c", serial: "GEN", hidraw: 0, card: 1},
		sysDevice{name: "1-1.3", product: "0012", serial: "VLM", hidraw: 2, card: 3},
	)
	env := newFakeEnv(device.ALSACardEnv, "1")
	b := newTestBinder(fsys, newFakeProbe(hidraw(2)), env)

	raw, ok := b.CardOverride()
	assert.True(t, ok)
	assert.Equal(t, "1", raw)

	sel, err := b.Bind()
	require.NoError(t, err)
	assert.True(t, sel.PinnedCard)
	assert.Equal(t, hidraw(0), sel.Device.HIDPath, "HID pairs with the card the operator pinned")

	b.Release(sel)
	assert.Equal(t, "1", env.Getenv(device.ALSACardEnv))
}

func TestBinder_releaseOfStaleSelectionIsIgnored(t *testing.T) {
	fsys := fakeSysFS(sysDevice{name: "1-1.3", product: "0012", serial: "VLM", hidraw: 2, card: 3})
	b := newTestBinder(fsys, newFakeProbe(hidraw(2)), newFakeEnv())

	_, err := b.Bind()
	require.NoError(t, err)

	other := device.Selection{}
	other.Device.SysPath = "bus/usb/devices/9-9"

	b.Release(other)

	_, bound := b.Current()
	assert.True(t, bound)
}

func TestBinder_concurrentBindRelease(t *testing.T) {
	fsys := fakeSysFS(sysDevice{name: "1-1.3", product: "0012", serial: "VLM", hidraw: 2, card: 3})
	probe := newFakeProbe(hidraw(2))
	b := newTestBinder(fsys, probe, newFakeEnv())

	var wg sync.WaitGroup

	for range 4 {
		wg.Add(1)

		go func() {
			defer wg.Done()

			var snap device.BindingSnapshot

			for range 50 {
				if sel, err := b.Bind(); err == nil {
					b.Release(sel)
				}

				b.Snapshot(&snap)
				_ = b.Changed()
			}
		}()
	}

	wg.Wait()
	assert.Positive(t, probe.callCount())
}

func TestBinder_snapshotZeroAlloc(t *testing.T) {
	fsys := fakeSysFS(sysDevice{name: "1-1.3", product: "0012", serial: "VLM", hidraw: 2, card: 3})
	b := newTestBinder(fsys, newFakeProbe(hidraw(2)), newFakeEnv())

	_, err := b.Bind()
	require.NoError(t, err)

	var snap device.BindingSnapshot

	allocs := testing.AllocsPerRun(100, func() { b.Snapshot(&snap) })
	assert.Zero(t, allocs)
	assert.Equal(t, "bus/usb/devices/1-1.3", snap.SysPath)
}

func TestBinder_snapshotNilAndUnbound(t *testing.T) {
	var nilBinder *device.Binder

	var snap device.BindingSnapshot

	nilBinder.Snapshot(&snap)
	assert.Equal(t, device.NoCard, snap.ALSACard)
	assert.False(t, snap.Bound)

	b := newTestBinder(fstest.MapFS{}, newFakeProbe(), newFakeEnv())
	b.Snapshot(&snap)
	assert.False(t, snap.Bound)
	assert.Equal(t, device.NoCard, snap.ALSACard)
}
