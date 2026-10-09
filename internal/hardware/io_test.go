package hardware_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/openmanet/openmanetd/internal/hardware"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func writeFile(t *testing.T, path, content string) {
	t.Helper()

	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(t, os.WriteFile(path, []byte(content), 0o644))
}

func readFile(t *testing.T, path string) string {
	t.Helper()

	data, err := os.ReadFile(path)
	require.NoError(t, err)

	return string(data)
}

// newSysfsBus builds a fake sysfs tree with both radios present.
func newSysfsBus(t *testing.T) (*hardware.SysfsBus, string) {
	t.Helper()

	root := t.TempDir()
	usbDev := filepath.Join(root, "sys", "bus", "usb", "devices", "1-1.2")
	writeFile(t, filepath.Join(usbDev, "busnum"), "1\n")
	writeFile(t, filepath.Join(usbDev, "devnum"), "7\n")
	writeFile(t, filepath.Join(root, "sys", "bus", "usb", "drivers", "usb", "unbind"), "")
	writeFile(t, filepath.Join(root, "sys", "bus", "usb", "drivers", "usb", "bind"), "")
	pciDev := filepath.Join(root, "sys", "bus", "pci", "devices", "0001:01:00.0")
	writeFile(t, filepath.Join(pciDev, "reset"), "")
	writeFile(t, filepath.Join(pciDev, "remove"), "")
	writeFile(t, filepath.Join(root, "sys", "bus", "pci", "rescan"), "")
	require.NoError(t, os.MkdirAll(filepath.Join(root, "sys", "class", "net", "wlan0"), 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(root, "sys", "class", "net", "wlan1"), 0o755))

	return &hardware.SysfsBus{
		SysDir:         filepath.Join(root, "sys"),
		DevDir:         filepath.Join(root, "dev"),
		HaLowUSBDevice: "1-1.2",
		HaLowNetdev:    "wlan1",
		WiFiPCIAddress: "0001:01:00.0",
		WiFiNetdev:     "wlan0",
	}, root
}

func TestSysfsBus_Presence(t *testing.T) {
	b, root := newSysfsBus(t)

	for _, r := range []hardware.Radio{hardware.RadioHaLow, hardware.RadioWiFi} {
		assert.True(t, b.Configured(r))
		assert.True(t, b.DevicePresent(r))
		assert.True(t, b.NetdevPresent(r))
	}

	assert.Equal(t, "wlan1", b.Netdev(hardware.RadioHaLow))
	assert.False(t, b.Configured(hardware.RadioUnspecified))

	require.NoError(t, os.RemoveAll(filepath.Join(root, "sys", "class", "net", "wlan1")))
	assert.False(t, b.NetdevPresent(hardware.RadioHaLow))

	empty := &hardware.SysfsBus{SysDir: b.SysDir}
	assert.False(t, empty.Configured(hardware.RadioWiFi))
	assert.False(t, empty.DevicePresent(hardware.RadioWiFi))
	assert.False(t, empty.NetdevPresent(hardware.RadioWiFi))
	require.ErrorIs(t, empty.ResetDevice(context.Background(), hardware.RadioWiFi), hardware.ErrBusNotConfigured)
	require.ErrorIs(t, empty.Remove(context.Background(), hardware.RadioWiFi), hardware.ErrBusNotConfigured)
	require.ErrorIs(t, empty.Rescan(context.Background(), hardware.RadioWiFi), hardware.ErrBusNotConfigured)
}

func TestSysfsBus_ResetUSB(t *testing.T) {
	t.Run("ioctl succeeds", func(t *testing.T) {
		b, root := newSysfsBus(t)

		var node string

		b.USBReset = func(n string) error {
			node = n

			return nil
		}

		require.NoError(t, b.ResetDevice(context.Background(), hardware.RadioHaLow))
		assert.Equal(t, filepath.Join(root, "dev", "bus", "usb", "001", "007"), node)
		assert.Empty(t, readFile(t, filepath.Join(b.SysDir, "bus", "usb", "drivers", "usb", "unbind")))
	})

	t.Run("ioctl fails, unbind and bind", func(t *testing.T) {
		b, _ := newSysfsBus(t)
		b.USBReset = func(string) error { return errors.New("EIO") }

		require.NoError(t, b.ResetDevice(context.Background(), hardware.RadioHaLow))
		assert.Equal(t, "1-1.2", readFile(t, filepath.Join(b.SysDir, "bus", "usb", "drivers", "usb", "unbind")))
		assert.Equal(t, "1-1.2", readFile(t, filepath.Join(b.SysDir, "bus", "usb", "drivers", "usb", "bind")))
	})

	t.Run("everything fails", func(t *testing.T) {
		b, _ := newSysfsBus(t)
		b.USBReset = func(string) error { return errors.New("EIO") }
		require.NoError(t, os.Remove(filepath.Join(b.SysDir, "bus", "usb", "drivers", "usb", "unbind")))

		require.Error(t, b.ResetDevice(context.Background(), hardware.RadioHaLow))
	})
}

func TestSysfsBus_ResetPCI(t *testing.T) {
	t.Run("function reset", func(t *testing.T) {
		b, _ := newSysfsBus(t)
		require.NoError(t, b.ResetDevice(context.Background(), hardware.RadioWiFi))
		assert.Equal(t, "1", readFile(t, filepath.Join(b.SysDir, "bus", "pci", "devices", "0001:01:00.0", "reset")))
		assert.Empty(t, readFile(t, filepath.Join(b.SysDir, "bus", "pci", "rescan")))
	})

	t.Run("no reset attribute: remove and rescan", func(t *testing.T) {
		b, _ := newSysfsBus(t)
		require.NoError(t, os.Remove(filepath.Join(b.SysDir, "bus", "pci", "devices", "0001:01:00.0", "reset")))

		require.NoError(t, b.ResetDevice(context.Background(), hardware.RadioWiFi))
		assert.Equal(t, "1", readFile(t, filepath.Join(b.SysDir, "bus", "pci", "devices", "0001:01:00.0", "remove")))
		assert.Equal(t, "1", readFile(t, filepath.Join(b.SysDir, "bus", "pci", "rescan")))
		_, err := os.Stat(filepath.Join(b.SysDir, "bus", "pci", "devices", "0001:01:00.0", "reset"))
		assert.ErrorIs(t, err, os.ErrNotExist, "a missing sysfs attribute is never created")
	})
}

func TestSysfsBus_RemoveAndRescan(t *testing.T) {
	b, _ := newSysfsBus(t)
	ctx := context.Background()

	require.NoError(t, b.Remove(ctx, hardware.RadioHaLow))
	assert.Equal(t, "1-1.2", readFile(t, filepath.Join(b.SysDir, "bus", "usb", "drivers", "usb", "unbind")))

	require.NoError(t, b.Remove(ctx, hardware.RadioWiFi))
	assert.Equal(t, "1", readFile(t, filepath.Join(b.SysDir, "bus", "pci", "devices", "0001:01:00.0", "remove")))

	require.NoError(t, b.Rescan(ctx, hardware.RadioHaLow), "HaLow rescan is a no-op")
	assert.Empty(t, readFile(t, filepath.Join(b.SysDir, "bus", "pci", "rescan")))

	require.NoError(t, b.Rescan(ctx, hardware.RadioWiFi))
	assert.Equal(t, "1", readFile(t, filepath.Join(b.SysDir, "bus", "pci", "rescan")))

	require.NoError(t, os.RemoveAll(filepath.Join(b.SysDir, "bus", "pci", "devices", "0001:01:00.0")))
	require.NoError(t, b.Remove(ctx, hardware.RadioWiFi), "a missing device is not an error")
}

func TestFileStore(t *testing.T) {
	path := filepath.Join(t.TempDir(), "openmanetd", "hwrecovery.json")
	s := &hardware.FileStore{Path: path}

	rec, err := s.Load()
	require.NoError(t, err, "a missing file is an empty record")
	assert.Empty(t, rec.RecoveryResets)

	at := time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)
	require.NoError(t, s.Save(hardware.RecoveryRecord{
		RecoveryResets:    []time.Time{at},
		HostResetRequests: []time.Time{at},
	}))

	rec, err = s.Load()
	require.NoError(t, err)
	require.Len(t, rec.RecoveryResets, 1)
	assert.True(t, at.Equal(rec.RecoveryResets[0]))
	assert.Equal(t, 1, rec.Version)

	entries, err := os.ReadDir(filepath.Dir(path))
	require.NoError(t, err)
	assert.Len(t, entries, 1, "no temp file left behind")

	require.NoError(t, os.WriteFile(path, []byte("{not json"), 0o644))

	_, err = s.Load()
	require.Error(t, err)
}

func TestDevWatchdogOpener(t *testing.T) {
	root := t.TempDir()
	classDir := filepath.Join(root, "class")
	devDir := filepath.Join(root, "dev")
	writeFile(t, filepath.Join(classDir, "watchdog0", "identity"), "Broadcom BCM2835 Watchdog timer\n")
	writeFile(t, filepath.Join(classDir, "watchdog1", "identity"), "GPIO Watchdog\n")
	writeFile(t, filepath.Join(devDir, "watchdog0"), "")
	writeFile(t, filepath.Join(devDir, "watchdog1"), "")

	o := hardware.DevWatchdogOpener{ClassDir: classDir, DevDir: devDir}

	wd, dev, err := o.Open(hardware.DefaultWatchdogIdentity)
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(devDir, "watchdog1"), dev, "selected by identity, never the SoC watchdog")

	require.NoError(t, wd.Pet())
	require.NoError(t, wd.Pet())
	require.NoError(t, wd.Close(true))
	assert.Equal(t, "kkV", readFile(t, dev))
	assert.Empty(t, readFile(t, filepath.Join(devDir, "watchdog0")))

	wd, _, err = o.Open(hardware.DefaultWatchdogIdentity)
	require.NoError(t, err)
	require.NoError(t, wd.Close(false))
	assert.Equal(t, "kkV", readFile(t, dev), "unclean close writes no magic character")

	_, _, err = o.Open("nope")
	require.ErrorIs(t, err, hardware.ErrWatchdogNotFound)

	_, _, err = hardware.DevWatchdogOpener{ClassDir: filepath.Join(root, "missing")}.Open("x")
	require.Error(t, err)
}
