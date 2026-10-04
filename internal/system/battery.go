package system

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/openmanet/openmanetd/internal/util/board"
)

// BatteryStatus holds one reading from the node's battery monitor.
type BatteryStatus struct {
	// Present is false when no supported battery monitor was found.
	Present       bool
	VoltageVolts  float32
	CurrentAmps   float32
	PowerWatts    float32
	ChargePercent float32 // negative when it cannot be estimated
	CellCount     uint32  // zero when unknown
}

// BatteryProvider abstracts battery monitor reads for testability.
type BatteryProvider interface {
	GetBatteryStatus() (*BatteryStatus, error)
}

// HwmonBatteryProvider reads an INA2xx battery/UPS monitor through the
// kernel hwmon subsystem (the ina2xx driver).
type HwmonBatteryProvider struct {
	// HwmonDir is the hwmon class directory (default "/sys/class/hwmon").
	HwmonDir      string
	seriesCells   uint32
	chargeUnknown bool
}

// NewHwmonBatteryProviderForBoard applies only the documented V1 battery
// measurement profile. Its INA228 measures the charger/system bus, not battery
// current; that voltage cannot establish SOC while charging or bridging a swap.
// V1 has a known 3S pack, so a depleted pack must never be inferred as full 2S.
// Other identified boards retain generic voltage estimation. An empty board ID
// keeps the measurements but leaves cell count and SOC unknown.
func NewHwmonBatteryProviderForBoard(modelID string) *HwmonBatteryProvider {
	if modelID == board.GhostnetV1 {
		return &HwmonBatteryProvider{seriesCells: 3, chargeUnknown: true}
	}

	if modelID == "" {
		return &HwmonBatteryProvider{chargeUnknown: true}
	}

	return &HwmonBatteryProvider{}
}

func (h *HwmonBatteryProvider) hwmonDir() string {
	if h.HwmonDir != "" {
		return h.HwmonDir
	}

	return "/sys/class/hwmon"
}

// liionCurve maps per-cell Li-ion voltage to an approximate state of charge.
// Points are ascending by voltage; values between points are interpolated.
var liionCurve = [...]struct { //nolint:gochecknoglobals // constant lookup table
	volts   float32
	percent float32
}{
	{3.00, 0},
	{3.30, 5},
	{3.50, 10},
	{3.60, 20},
	{3.70, 40},
	{3.75, 50},
	{3.80, 60},
	{3.90, 70},
	{4.00, 80},
	{4.10, 90},
	{4.20, 100},
}

// Per-cell voltage bounds used to infer the series cell count. The lower
// bound allows for sag under load at empty.
const (
	minCellVolts   = 2.9
	maxCellVolts   = 4.3
	maxSeriesCells = 6
)

// GetBatteryStatus returns the first INA2xx monitor's readings. It returns
// a status with Present=false (and no error) when the device has none.
func (h *HwmonBatteryProvider) GetBatteryStatus() (*BatteryStatus, error) {
	dir, err := h.findMonitor()
	if err != nil {
		return nil, err
	}

	if dir == "" {
		return &BatteryStatus{ChargePercent: -1}, nil
	}

	millivolts, err := readHwmonInt(filepath.Join(dir, "in1_input"))
	if err != nil {
		return nil, fmt.Errorf("read battery voltage: %w", err)
	}

	status := &BatteryStatus{
		Present:      true,
		VoltageVolts: float32(millivolts) / 1000,
	}

	// Current and power depend on the shunt configuration; a monitor
	// without them still reports voltage.
	if milliamps, err := readHwmonInt(filepath.Join(dir, "curr1_input")); err == nil {
		status.CurrentAmps = float32(milliamps) / 1000
	}

	if microwatts, err := readHwmonInt(filepath.Join(dir, "power1_input")); err == nil {
		status.PowerWatts = float32(microwatts) / 1_000_000
	}

	if h.chargeUnknown {
		status.CellCount = h.seriesCells
		status.ChargePercent = -1

		return status, nil
	}

	status.CellCount, status.ChargePercent = EstimateLiionCharge(status.VoltageVolts)

	return status, nil
}

// findMonitor returns the hwmon directory of the first INA2xx device, or ""
// when there is none.
func (h *HwmonBatteryProvider) findMonitor() (string, error) {
	entries, err := os.ReadDir(h.hwmonDir())
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}

		return "", fmt.Errorf("list hwmon: %w", err)
	}

	for _, e := range entries {
		dir := filepath.Join(h.hwmonDir(), e.Name())

		name, err := os.ReadFile(filepath.Join(dir, "name"))
		if err != nil {
			continue
		}

		if strings.HasPrefix(strings.TrimSpace(string(name)), "ina2") {
			return dir, nil
		}
	}

	return "", nil
}

func readHwmonInt(path string) (int64, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, fmt.Errorf("read %s: %w", path, err)
	}

	v, err := strconv.ParseInt(strings.TrimSpace(string(data)), 10, 64)
	if err != nil {
		return 0, fmt.Errorf("parse %s: %w", path, err)
	}

	return v, nil
}

// EstimateLiionCharge infers the series cell count from a pack voltage and
// estimates state of charge from the Li-ion voltage curve. The smallest
// cell count whose per-cell voltage is plausible wins, so a full 3S pack
// (12.6 V) is not mistaken for an empty 4S one. It returns (0, -1) when the
// voltage doesn't fit a 1–6 cell Li-ion pack.
func EstimateLiionCharge(volts float32) (uint32, float32) {
	cells := 0

	for n := 1; n <= maxSeriesCells; n++ {
		perCell := volts / float32(n)
		if perCell >= minCellVolts && perCell <= maxCellVolts {
			cells = n

			break
		}
	}

	if cells == 0 {
		return 0, -1
	}

	perCell := volts / float32(cells)

	first, last := liionCurve[0], liionCurve[len(liionCurve)-1]

	switch {
	case perCell <= first.volts:
		return uint32(cells), first.percent
	case perCell >= last.volts:
		return uint32(cells), last.percent
	}

	for i := 1; i < len(liionCurve); i++ {
		hi := liionCurve[i]
		if perCell > hi.volts {
			continue
		}

		lo := liionCurve[i-1]
		frac := (perCell - lo.volts) / (hi.volts - lo.volts)

		return uint32(cells), lo.percent + frac*(hi.percent-lo.percent)
	}

	return uint32(cells), last.percent
}
