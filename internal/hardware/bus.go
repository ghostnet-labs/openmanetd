package hardware

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"golang.org/x/sys/unix"
)

// Bus reaches a radio over its host bus: presence checks, device resets,
// driver unbind and PCI rescan. It never touches a GPIO.
type Bus interface {
	// Configured reports whether the radio's bus identity is known. An
	// unconfigured radio stays unmanaged.
	Configured(r Radio) bool
	// DevicePresent reports whether the radio's USB or PCI device exists.
	DevicePresent(r Radio) bool
	// NetdevPresent reports whether the radio's network interface exists.
	NetdevPresent(r Radio) bool
	// Netdev returns the configured interface name.
	Netdev(r Radio) string
	// ResetDevice is ladder step 1: USB device reset (HaLow) or PCI
	// function reset (Wi-Fi), with unbind/bind or remove/rescan fallback.
	ResetDevice(ctx context.Context, r Radio) error
	// Remove detaches the device before its rail goes off: driver unbind
	// (HaLow) or PCI remove (Wi-Fi). A missing device is not an error.
	Remove(ctx context.Context, r Radio) error
	// Rescan asks the bus to enumerate the device after its rail came up:
	// PCI rescan (Wi-Fi); a no-op for HaLow, whose hub enumerates it.
	Rescan(ctx context.Context, r Radio) error
}

// ErrBusNotConfigured means the radio's bus identity is not configured.
var ErrBusNotConfigured = errors.New("radio bus identity not configured")

// SysfsBus implements Bus over sysfs and usbdevfs.
type SysfsBus struct {
	USBReset       func(devNode string) error
	SysDir         string
	DevDir         string
	HaLowUSBDevice string
	HaLowNetdev    string
	WiFiPCIAddress string
	WiFiNetdev     string
}

func (b *SysfsBus) sysDir() string {
	if b.SysDir != "" {
		return b.SysDir
	}

	return "/sys"
}

func (b *SysfsBus) devDir() string {
	if b.DevDir != "" {
		return b.DevDir
	}

	return "/dev"
}

// Configured implements Bus.
func (b *SysfsBus) Configured(r Radio) bool {
	switch r {
	case RadioHaLow:
		return b.HaLowUSBDevice != "" && b.HaLowNetdev != ""
	case RadioWiFi:
		return b.WiFiPCIAddress != "" && b.WiFiNetdev != ""
	case RadioUnspecified:
		return false
	}

	return false
}

// Netdev implements Bus.
func (b *SysfsBus) Netdev(r Radio) string {
	switch r {
	case RadioHaLow:
		return b.HaLowNetdev
	case RadioWiFi:
		return b.WiFiNetdev
	case RadioUnspecified:
		return ""
	}

	return ""
}

func (b *SysfsBus) devicePath(r Radio) string {
	switch r {
	case RadioHaLow:
		if b.HaLowUSBDevice != "" {
			return filepath.Join(b.sysDir(), "bus", "usb", "devices", b.HaLowUSBDevice)
		}
	case RadioWiFi:
		if b.WiFiPCIAddress != "" {
			return filepath.Join(b.sysDir(), "bus", "pci", "devices", b.WiFiPCIAddress)
		}
	case RadioUnspecified:
	}

	return ""
}

// DevicePresent implements Bus.
func (b *SysfsBus) DevicePresent(r Radio) bool {
	p := b.devicePath(r)

	return p != "" && exists(p)
}

// NetdevPresent implements Bus.
func (b *SysfsBus) NetdevPresent(r Radio) bool {
	n := b.Netdev(r)

	return n != "" && exists(filepath.Join(b.sysDir(), "class", "net", n))
}

// ResetDevice implements Bus.
func (b *SysfsBus) ResetDevice(_ context.Context, r Radio) error {
	if !b.Configured(r) {
		return ErrBusNotConfigured
	}

	switch r {
	case RadioHaLow:
		return b.resetUSB()
	case RadioWiFi:
		return b.resetPCI()
	case RadioUnspecified:
	}

	return ErrUnknownRadio
}

// resetUSB issues USBDEVFS_RESET; if that fails it unbinds and rebinds the
// device from the generic usb driver, which re-probes every interface.
func (b *SysfsBus) resetUSB() error {
	node, err := b.usbDevNode()
	if err == nil {
		if err = b.usbReset(node); err == nil {
			return nil
		}
	}

	drv := filepath.Join(b.sysDir(), "bus", "usb", "drivers", "usb")
	if uerr := writeSysfs(filepath.Join(drv, "unbind"), b.HaLowUSBDevice); uerr != nil {
		return errors.Join(fmt.Errorf("usb reset: %w", err), uerr)
	}

	if berr := writeSysfs(filepath.Join(drv, "bind"), b.HaLowUSBDevice); berr != nil {
		return errors.Join(fmt.Errorf("usb reset: %w", err), berr)
	}

	return nil
}

func (b *SysfsBus) usbReset(node string) error {
	if b.USBReset != nil {
		return b.USBReset(node)
	}

	return ioctlUSBReset(node)
}

// usbDevNode derives /dev/bus/usb/BBB/DDD from the device's busnum and
// devnum attributes.
func (b *SysfsBus) usbDevNode() (string, error) {
	dir := b.devicePath(RadioHaLow)

	bus, err := readSysfsInt(filepath.Join(dir, "busnum"))
	if err != nil {
		return "", err
	}

	dev, err := readSysfsInt(filepath.Join(dir, "devnum"))
	if err != nil {
		return "", err
	}

	return filepath.Join(b.devDir(), "bus", "usb", fmt.Sprintf("%03d", bus), fmt.Sprintf("%03d", dev)), nil
}

// resetPCI writes the function-level reset attribute; if that fails it
// removes the function and rescans the bus.
func (b *SysfsBus) resetPCI() error {
	dir := b.devicePath(RadioWiFi)

	err := writeSysfs(filepath.Join(dir, "reset"), "1")
	if err == nil {
		return nil
	}

	if rerr := writeSysfs(filepath.Join(dir, "remove"), "1"); rerr != nil {
		return errors.Join(fmt.Errorf("pci reset: %w", err), rerr)
	}

	if serr := writeSysfs(filepath.Join(b.sysDir(), "bus", "pci", "rescan"), "1"); serr != nil {
		return errors.Join(fmt.Errorf("pci reset: %w", err), serr)
	}

	return nil
}

// Remove implements Bus.
func (b *SysfsBus) Remove(_ context.Context, r Radio) error {
	if !b.Configured(r) {
		return ErrBusNotConfigured
	}

	if !b.DevicePresent(r) {
		return nil
	}

	switch r {
	case RadioHaLow:
		return writeSysfs(filepath.Join(b.sysDir(), "bus", "usb", "drivers", "usb", "unbind"), b.HaLowUSBDevice)
	case RadioWiFi:
		return writeSysfs(filepath.Join(b.devicePath(r), "remove"), "1")
	case RadioUnspecified:
	}

	return ErrUnknownRadio
}

// Rescan implements Bus.
func (b *SysfsBus) Rescan(_ context.Context, r Radio) error {
	if !b.Configured(r) {
		return ErrBusNotConfigured
	}

	if r != RadioWiFi {
		return nil
	}

	return writeSysfs(filepath.Join(b.sysDir(), "bus", "pci", "rescan"), "1")
}

func exists(p string) bool {
	_, err := os.Stat(p)

	return err == nil
}

// writeSysfs writes value to an existing sysfs attribute. It never creates
// the file: a missing attribute is an error, not a new regular file.
func writeSysfs(path, value string) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_TRUNC, 0)
	if err != nil {
		return fmt.Errorf("open %s: %w", path, err)
	}

	if _, err = f.WriteString(value); err != nil {
		_ = f.Close() // the write error is the one to report

		return fmt.Errorf("write %s: %w", path, err)
	}

	if err = f.Close(); err != nil {
		return fmt.Errorf("close %s: %w", path, err)
	}

	return nil
}

func readSysfsInt(path string) (int, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, fmt.Errorf("read %s: %w", path, err)
	}

	v, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		return 0, fmt.Errorf("parse %s: %w", path, err)
	}

	return v, nil
}

// ioctlUSBReset opens a usbdevfs node and issues USBDEVFS_RESET.
func ioctlUSBReset(node string) error {
	fd, err := unix.Open(node, unix.O_WRONLY|unix.O_CLOEXEC, 0)
	if err != nil {
		return fmt.Errorf("open %s: %w", node, err)
	}

	defer func() { _ = unix.Close(fd) }() // nothing useful to do on close failure

	if err := unix.IoctlSetInt(fd, usbdevfsReset, 0); err != nil {
		return fmt.Errorf("USBDEVFS_RESET %s: %w", node, err)
	}

	return nil
}
