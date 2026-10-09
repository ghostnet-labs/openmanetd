package hardware

import (
	"errors"
	"fmt"
	"sync/atomic"
	"time"

	"github.com/warthog618/go-gpiocdev"
)

// GPIO line names. They must match the gpio-line-names the V1 overlay sets
// on the RP1 controller, which in turn match the signal column of the
// pinout record. Code never uses GPIO numbers.
const (
	LineHaLowPwrEn     = "HALOW_PWR_EN" //nolint:gosec // G101 false positive: GPIO line name, not a credential
	LineWiFiPwrEn      = "WIFI_PWR_EN"  //nolint:gosec // G101 false positive: GPIO line name, not a credential
	LineHaLowFaultN    = "HALOW_FAULT_N"
	LineWiFiFaultN     = "WIFI_FAULT_N"
	LineGNSSResetN     = "GNSS_RESET_N"
	LineSupervisorARM  = "SUPERVISOR_ARM"
	LinePowerGood      = "POWER_GOOD"
	LineEFuseFault     = "EFUSE_FAULT"
	LineHaLowResetN    = "HALOW_RESET_N"
	LineHaLowWakeN     = "HALOW_WAKE_N"
	LineINA228AlertN   = "INA228_ALERT_N"
	LineWiFiWDis1N     = "WIFI_WDIS1_N"
	LineUSBHubResetN   = "USB_HUB_RESET_N"
	LineHaLowUSBFaultN = "HALOW_USB_FAULT_N"
	LineVLMUSBFaultN   = "VLM_USB_FAULT_N"
	lineConsumer       = "openmanetd-hwmgr"
	lineInputDebounce  = 10 * time.Millisecond
)

// hwmgr never requests GNSS_PPS (owned by the pps-gpio overlay),
// SUPERVISOR_WDI (owned by the kernel gpio-wdt driver), the reserved GPIO17,
// the I2C pins or the GNSS UART pins. TestLineSpecs pins that list. The
// supervisor's WDO pin is not a CM5 input on V1 (D-044): it pulls the
// TPS386000 SENSE4L tap, so a watchdog trip cycles PMIC_Enable directly.

// LineSpec describes how one GPIO line is requested. Asserted always means
// the line's active level: for an ActiveLow line, asserted is electrically
// low. An OpenDrain output is only ever driven low or released (high-Z), so
// the software has no path that drives it high. An output normally keeps
// its present level when requested; StartDeasserted requests it deasserted
// instead, for a line whose safe state must not depend on what a previous
// owner left behind.
type LineSpec struct {
	Name            string
	ActiveLow       bool
	OpenDrain       bool
	Output          bool
	StartDeasserted bool
}

// supervisorArmSpec is SUPERVISOR_ARM (GPIO11), the active-high TPS386000
// MR watchdog arm (D-044). A 10 kOhm pull-down holds it low, so the
// external watchdog is disarmed while the CM5 is off, in reset, or before
// hwmgr runs. hwmgr requests it low, drives it high only after the WDI
// heartbeat has been written at least once, and drops it again before an
// orderly poweroff or daemon stop. It is requested only when the watchdog
// service is enabled, independent of the radio control mode, because the
// arm follows the heartbeat rather than radio actuation.
func supervisorArmSpec() LineSpec {
	return LineSpec{Name: LineSupervisorARM, Output: true, StartDeasserted: true}
}

// outputSpecs are the lines hwmgr drives in active control mode. GPIO8,
// GPIO18, GPIO19 and GPIO21 are open-drain: their pull-ups return to a
// switched radio rail, and a high GPIO would back-feed a card whose rail is
// off.
func outputSpecs() []LineSpec {
	return []LineSpec{
		{Name: LineHaLowPwrEn, Output: true},
		{Name: LineWiFiPwrEn, Output: true},
		{Name: LineGNSSResetN, Output: true, ActiveLow: true, OpenDrain: true},
		{Name: LineHaLowResetN, Output: true, ActiveLow: true, OpenDrain: true},
		{Name: LineHaLowWakeN, Output: true, ActiveLow: true, OpenDrain: true},
		{Name: LineWiFiWDis1N, Output: true, ActiveLow: true, OpenDrain: true},
		{Name: LineUSBHubResetN, Output: true, ActiveLow: true},
	}
}

// faultInput describes one hardware status input. faultWhenAsserted is
// false only for POWER_GOOD, whose asserted (active-high) level means
// healthy.
type faultInput struct {
	spec              LineSpec
	line              FaultLine
	faultWhenAsserted bool
}

// faultInputs lists every status input in FaultLine order.
func faultInputs() [faultLineCount]faultInput {
	return [faultLineCount]faultInput{
		{spec: LineSpec{Name: LineHaLowFaultN, ActiveLow: true}, line: FaultLineHaLowFault, faultWhenAsserted: true},
		{spec: LineSpec{Name: LineWiFiFaultN, ActiveLow: true}, line: FaultLineWiFiFault, faultWhenAsserted: true},
		{spec: LineSpec{Name: LinePowerGood}, line: FaultLinePowerGood, faultWhenAsserted: false},
		{spec: LineSpec{Name: LineEFuseFault, ActiveLow: true}, line: FaultLineEFuseFault, faultWhenAsserted: true},
		{spec: LineSpec{Name: LineHaLowUSBFaultN, ActiveLow: true}, line: FaultLineHaLowUSBFault, faultWhenAsserted: true},
		{spec: LineSpec{Name: LineVLMUSBFaultN, ActiveLow: true}, line: FaultLineVLMUSBFault, faultWhenAsserted: true},
		{spec: LineSpec{Name: LineINA228AlertN, ActiveLow: true}, line: FaultLineINA228Alert, faultWhenAsserted: true},
	}
}

// FaultInputNames returns the names accepted in hardware.faultInputs.
func FaultInputNames() []string {
	in := faultInputs()
	names := make([]string, 0, len(in))

	for _, f := range in {
		names = append(names, f.spec.Name)
	}

	return names
}

// LineOpener finds and requests GPIO lines by name.
type LineOpener interface {
	// Resolve reports whether a line with this name exists.
	Resolve(name string) bool
	// Open requests every spec. Outputs keep their current electrical
	// value, so a daemon restart does not glitch a radio.
	Open(specs []LineSpec) (Lines, error)
}

// Lines is a set of requested GPIO lines.
type Lines interface {
	// Set asserts or releases an output line.
	Set(name string, asserted bool) error
	// Get reads a line's logical (asserted) value.
	Get(name string) (bool, error)
	// Edges returns the number of edges seen on an input line.
	Edges(name string) uint64
	// Close releases every line.
	Close() error
}

// ErrLineNotRequested means a Lines method named a line that was not
// requested.
var ErrLineNotRequested = errors.New("gpio line not requested")

// CdevLineOpener requests lines through the GPIO character device using
// go-gpiocdev, finding each line by its device-tree name.
type CdevLineOpener struct{}

// Resolve implements LineOpener.
func (CdevLineOpener) Resolve(name string) bool {
	_, _, err := gpiocdev.FindLine(name)

	return err == nil
}

// Open implements LineOpener. On any failure every line already requested
// is released again.
func (CdevLineOpener) Open(specs []LineSpec) (Lines, error) {
	cl := &cdevLines{
		lines: make(map[string]*gpiocdev.Line, len(specs)),
		edges: make(map[string]*atomic.Uint64, len(specs)),
	}

	for _, s := range specs {
		if err := cl.request(s); err != nil {
			_ = cl.Close() // best effort; the request error is the one to report

			return nil, err
		}
	}

	return cl, nil
}

// cdevLines holds requested lines. Both maps are written only during Open
// and read-only afterwards, so concurrent Set/Get/Edges are safe; each
// gpiocdev.Line serializes its own ioctls.
type cdevLines struct {
	lines map[string]*gpiocdev.Line
	edges map[string]*atomic.Uint64
}

func (c *cdevLines) request(s LineSpec) error {
	chip, offset, err := gpiocdev.FindLine(s.Name)
	if err != nil {
		return fmt.Errorf("find gpio line %s: %w", s.Name, err)
	}

	if !s.Output {
		return c.requestInput(chip, offset, s)
	}

	if s.StartDeasserted {
		return c.requestDeasserted(chip, offset, s)
	}

	// Request without changing direction, read the present level, then
	// reconfigure as an output at that same level.
	l, err := gpiocdev.RequestLine(chip, offset, gpiocdev.AsIs, gpiocdev.WithConsumer(lineConsumer))
	if err != nil {
		return fmt.Errorf("request gpio line %s: %w", s.Name, err)
	}

	physical, err := l.Value()
	if err != nil {
		_ = l.Close() // best effort; the read error is the one to report

		return fmt.Errorf("read gpio line %s: %w", s.Name, err)
	}

	opts := make([]gpiocdev.LineConfigOption, 0, 3)
	logical := physical

	if s.ActiveLow {
		opts = append(opts, gpiocdev.AsActiveLow)
		logical = 1 - physical
	}

	if s.OpenDrain {
		opts = append(opts, gpiocdev.AsOpenDrain)
	}

	opts = append(opts, gpiocdev.AsOutput(logical))

	if err := l.Reconfigure(opts...); err != nil {
		_ = l.Close() // best effort; the reconfigure error is the one to report

		return fmt.Errorf("configure gpio line %s: %w", s.Name, err)
	}

	c.lines[s.Name] = l

	return nil
}

// requestDeasserted requests an output at its inactive level in one step,
// without first reading or adopting the present level.
func (c *cdevLines) requestDeasserted(chip string, offset int, s LineSpec) error {
	opts := make([]gpiocdev.LineReqOption, 0, 4)
	opts = append(opts, gpiocdev.WithConsumer(lineConsumer))

	if s.ActiveLow {
		opts = append(opts, gpiocdev.AsActiveLow)
	}

	if s.OpenDrain {
		opts = append(opts, gpiocdev.AsOpenDrain)
	}

	opts = append(opts, gpiocdev.AsOutput(0))

	l, err := gpiocdev.RequestLine(chip, offset, opts...)
	if err != nil {
		return fmt.Errorf("request gpio output %s: %w", s.Name, err)
	}

	c.lines[s.Name] = l

	return nil
}

func (c *cdevLines) requestInput(chip string, offset int, s LineSpec) error {
	counter := new(atomic.Uint64)

	opts := []gpiocdev.LineReqOption{
		gpiocdev.WithConsumer(lineConsumer),
		gpiocdev.AsInput,
		gpiocdev.WithBiasAsIs,
		gpiocdev.WithBothEdges,
		gpiocdev.WithDebounce(lineInputDebounce),
		gpiocdev.WithEventHandler(func(gpiocdev.LineEvent) { counter.Add(1) }),
	}
	if s.ActiveLow {
		opts = append(opts, gpiocdev.AsActiveLow)
	}

	l, err := gpiocdev.RequestLine(chip, offset, opts...)
	if err != nil {
		return fmt.Errorf("request gpio input %s: %w", s.Name, err)
	}

	c.lines[s.Name] = l
	c.edges[s.Name] = counter

	return nil
}

// Set implements Lines.
func (c *cdevLines) Set(name string, asserted bool) error {
	l, ok := c.lines[name]
	if !ok {
		return fmt.Errorf("%s: %w", name, ErrLineNotRequested)
	}

	v := 0
	if asserted {
		v = 1
	}

	if err := l.SetValue(v); err != nil {
		return fmt.Errorf("set gpio line %s: %w", name, err)
	}

	return nil
}

// Get implements Lines.
func (c *cdevLines) Get(name string) (bool, error) {
	l, ok := c.lines[name]
	if !ok {
		return false, fmt.Errorf("%s: %w", name, ErrLineNotRequested)
	}

	v, err := l.Value()
	if err != nil {
		return false, fmt.Errorf("read gpio line %s: %w", name, err)
	}

	return v == 1, nil
}

// Edges implements Lines.
func (c *cdevLines) Edges(name string) uint64 {
	if e, ok := c.edges[name]; ok {
		return e.Load()
	}

	return 0
}

// Close implements Lines.
func (c *cdevLines) Close() error {
	var errs []error

	for name, l := range c.lines {
		if err := l.Close(); err != nil {
			errs = append(errs, fmt.Errorf("close gpio line %s: %w", name, err))
		}
	}

	return errors.Join(errs...)
}
