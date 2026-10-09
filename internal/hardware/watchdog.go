package hardware

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// DefaultWatchdogIdentity is the identity string the Linux gpio-wdt driver
// reports in /sys/class/watchdog/watchdogN/identity.
const DefaultWatchdogIdentity = "GPIO Watchdog"

// Watchdog is an open watchdog device.
type Watchdog interface {
	// Pet writes one heartbeat.
	Pet() error
	// Close releases the device. With clean set it first writes the magic
	// close character: with gpio-wdt "always-running" the kernel then keeps
	// toggling WDI, so a daemon stop or restart does not reset the node.
	// Without it (a host reset was requested) the watchdog times out.
	Close(clean bool) error
}

// WatchdogOpener finds and opens a watchdog device by identity.
type WatchdogOpener interface {
	// Open returns the device and its path.
	Open(identity string) (Watchdog, string, error)
}

// ErrWatchdogNotFound means no watchdog reports the wanted identity.
var ErrWatchdogNotFound = errors.New("watchdog device not found")

// DevWatchdogOpener opens /dev/watchdogN after matching its sysfs identity,
// so the daemon never grabs the SoC watchdog that procd pets.
type DevWatchdogOpener struct {
	// ClassDir defaults to "/sys/class/watchdog".
	ClassDir string
	// DevDir defaults to "/dev".
	DevDir string
}

// Open implements WatchdogOpener.
func (o DevWatchdogOpener) Open(identity string) (Watchdog, string, error) {
	classDir := o.ClassDir
	if classDir == "" {
		classDir = "/sys/class/watchdog"
	}

	devDir := o.DevDir
	if devDir == "" {
		devDir = "/dev"
	}

	entries, err := os.ReadDir(classDir)
	if err != nil {
		return nil, "", fmt.Errorf("list watchdogs: %w", err)
	}

	for _, e := range entries {
		id, err := os.ReadFile(filepath.Join(classDir, e.Name(), "identity"))
		if err != nil || strings.TrimSpace(string(id)) != identity {
			continue
		}

		path := filepath.Join(devDir, e.Name())

		f, err := os.OpenFile(path, os.O_WRONLY, 0)
		if err != nil {
			return nil, "", fmt.Errorf("open %s: %w", path, err)
		}

		return &devWatchdog{f: f}, path, nil
	}

	return nil, "", fmt.Errorf("%q: %w", identity, ErrWatchdogNotFound)
}

type devWatchdog struct {
	f *os.File
}

// heartbeat is any byte other than the magic close character.
var heartbeat = []byte{'k'} //nolint:gochecknoglobals // constant byte slice reused per pet

var magicClose = []byte{'V'} //nolint:gochecknoglobals // constant byte slice

func (w *devWatchdog) Pet() error {
	if _, err := w.f.Write(heartbeat); err != nil {
		return fmt.Errorf("pet watchdog: %w", err)
	}

	return nil
}

func (w *devWatchdog) Close(clean bool) error {
	var werr error
	if clean {
		_, werr = w.f.Write(magicClose)
	}

	cerr := w.f.Close()

	if werr != nil {
		return fmt.Errorf("watchdog magic close: %w", werr)
	}

	if cerr != nil {
		return fmt.Errorf("close watchdog: %w", cerr)
	}

	return nil
}
