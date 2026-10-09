package hardware

import (
	"math"
	"time"

	"github.com/openmanet/openmanetd/internal/system"
)

// Battery telemetry timing (design section 6).
const (
	batterySampleInterval = time.Second
	batteryFullInterval   = 10 * time.Second
	batteryStaleAfter     = 5 * time.Second
	batteryAbsentAfter    = 30 * time.Second
	// batteryIntegrateMaxGap stops coulomb counting across read gaps; a
	// longer gap is skipped rather than extrapolated.
	batteryIntegrateMaxGap = 5 * time.Second
)

// Low-battery policy for the V1 3S2P Molicel M35A pack (D-013), compared
// against the load-compensated voltage V_comp = V_bus + I x R_pack.
const (
	v1SeriesCells      = 3
	warningMV          = 10500
	criticalMV         = 9900
	shutdownMV         = 9600
	levelHysteresisMV  = 200
	levelEnterHold     = 30 * time.Second
	levelClearHold     = 60 * time.Second
	DefaultPackOhm     = 0.06
	microjoulePerMWh   = 3.6e6
	millisPerHour      = float64(time.Hour / time.Millisecond)
	milliCPerC         = 1000.0
	microwattPerMilliW = 1000
)

// MonitorReader reads the battery monitor. *system.HwmonBatteryProvider
// satisfies it.
type MonitorReader interface {
	ReadMonitor(full bool) (system.MonitorReading, error)
}

// batteryTracker owns battery sampling, stale-data handling, coulomb
// counting and the low-battery level. It is used only from the manager's
// step goroutine.
type batteryTracker struct {
	sampledAt     time.Time
	lastAttempt   time.Time
	lastFull      time.Time
	lastIntegrate time.Time
	enterSince    time.Time
	clearSince    time.Time
	reader        MonitorReader
	last          system.MonitorReading
	energyMWh     float64
	packOhm       float64
	chargeMAh     float64
	tempC         float64
	alarms        uint32
	seriesCells   uint32
	level         BatteryLevel
	noMonitor     bool
	hasEnergy     bool
	hasTemp       bool
	policyEnabled bool
	everAttempted bool
}

func newBatteryTracker(reader MonitorReader, seriesCells uint32, packOhm float64, policyEnabled bool) *batteryTracker {
	if packOhm <= 0 {
		packOhm = DefaultPackOhm
	}

	return &batteryTracker{
		reader:        reader,
		seriesCells:   seriesCells,
		packOhm:       packOhm,
		policyEnabled: policyEnabled && seriesCells == v1SeriesCells,
		level:         BatteryLevelUnknown,
	}
}

// due reports whether a sample is due at now.
func (b *batteryTracker) due(now time.Time) bool {
	return !b.everAttempted || now.Sub(b.lastAttempt) >= batterySampleInterval
}

// sample reads the monitor when due and updates the level.
func (b *batteryTracker) sample(now time.Time) {
	if !b.due(now) {
		return
	}

	b.everAttempted = true
	b.lastAttempt = now

	if b.reader == nil {
		b.noMonitor = true

		return
	}

	full := b.lastFull.IsZero() || now.Sub(b.lastFull) >= batteryFullInterval

	r, err := b.reader.ReadMonitor(full)
	if err != nil {
		b.enterSince, b.clearSince = time.Time{}, time.Time{}

		return
	}

	if !r.Present {
		b.noMonitor = true
		b.sampledAt = time.Time{}
		b.enterSince, b.clearSince = time.Time{}, time.Time{}

		return
	}

	b.noMonitor = false
	b.integrate(now, r)
	b.last = r
	b.sampledAt = now

	if full {
		b.lastFull = now
		b.hasEnergy, b.hasTemp, b.alarms = r.HasEnergy, r.HasTemp, r.Alarms

		if r.HasEnergy {
			b.energyMWh = float64(r.EnergyMicrojoul) / microjoulePerMWh
		}

		if r.HasTemp {
			b.tempC = float64(r.TempMilliC) / milliCPerC
		}
	}

	b.updateLevel(now)
}

// integrate adds the charge drawn since the previous reading.
func (b *batteryTracker) integrate(now time.Time, r system.MonitorReading) {
	if !r.HasCurrent {
		b.lastIntegrate = time.Time{}

		return
	}

	if !b.lastIntegrate.IsZero() {
		if gap := now.Sub(b.lastIntegrate); gap > 0 && gap <= batteryIntegrateMaxGap {
			b.chargeMAh += float64(r.CurrentMilliamp) * float64(gap/time.Millisecond) / millisPerHour
		}
	}

	b.lastIntegrate = now
}

// compensatedMV is V_bus + I x R_pack in millivolts.
func (b *batteryTracker) compensatedMV() int64 {
	mv := b.last.BusMillivolts
	if b.last.HasCurrent {
		mv += int64(math.Round(float64(b.last.CurrentMilliamp) * b.packOhm))
	}

	return mv
}

// severity maps a compensated voltage to a level, with marginMV added to
// every threshold (0 to enter a level, the hysteresis to leave it).
func severity(mv, marginMV int64) BatteryLevel {
	switch {
	case mv < shutdownMV+marginMV:
		return BatteryLevelShutdown
	case mv < criticalMV+marginMV:
		return BatteryLevelCritical
	case mv < warningMV+marginMV:
		return BatteryLevelWarning
	default:
		return BatteryLevelNormal
	}
}

// updateLevel applies the 30 s entry hold and the 0.2 V / 60 s clear
// hysteresis. Called only after a fresh reading.
func (b *batteryTracker) updateLevel(now time.Time) {
	if !b.policyEnabled {
		return
	}

	mv := b.compensatedMV()

	if b.level == BatteryLevelUnknown {
		// First fresh reading: start from normal and let the entry hold
		// apply, so one sag at boot does not trip a level.
		b.level = BatteryLevelNormal
	}

	enter := severity(mv, 0)
	if enter > b.level {
		if b.enterSince.IsZero() {
			b.enterSince = now
		}

		if now.Sub(b.enterSince) >= levelEnterHold {
			b.level = enter
			b.enterSince = time.Time{}
		}
	} else {
		b.enterSince = time.Time{}
	}

	clear := severity(mv, levelHysteresisMV)
	if clear < b.level {
		if b.clearSince.IsZero() {
			b.clearSince = now
		}

		if now.Sub(b.clearSince) >= levelClearHold {
			b.level = clear
			b.clearSince = time.Time{}
		}
	} else {
		b.clearSince = time.Time{}
	}
}

// fresh reports whether the newest reading is no older than the stale
// limit.
func (b *batteryTracker) fresh(now time.Time) bool {
	return !b.sampledAt.IsZero() && now.Sub(b.sampledAt) <= batteryStaleAfter
}

// policyLevel is the level the battery actions act on. It stays at the last
// computed level while readings are missing, so losing the monitor never
// re-enables a load that low battery switched off.
func (b *batteryTracker) policyLevel() BatteryLevel {
	if !b.policyEnabled {
		return BatteryLevelUnknown
	}

	return b.level
}

// status assembles the published battery block.
func (b *batteryTracker) status(now time.Time) BatteryStatus {
	st := BatteryStatus{
		SeriesCells:  b.seriesCells,
		ChargeMAh:    b.chargeMAh,
		Level:        BatteryLevelUnknown,
		AbsentReason: AbsentReasonNone,
	}

	switch {
	case b.noMonitor:
		st.AbsentReason = AbsentReasonNoMonitor

		return st
	case b.sampledAt.IsZero():
		st.AbsentReason = AbsentReasonMonitorUnreachable

		return st
	}

	age := now.Sub(b.sampledAt)

	st.SampledAt = b.sampledAt
	st.BusMV = clampInt32(b.last.BusMillivolts)
	st.CurrentMA = clampInt32(b.last.CurrentMilliamp)
	st.PowerMW = clampInt32(b.last.PowerMicrowatts / microwattPerMilliW)
	st.CompensatedMV = clampInt32(b.compensatedMV())
	st.EnergyMWh, st.HasEnergy = b.energyMWh, b.hasEnergy
	st.TempC, st.HasTemp = b.tempC, b.hasTemp
	st.Alarms = b.alarms
	st.Stale = age > batteryStaleAfter
	st.Present = age <= batteryAbsentAfter

	if !st.Present {
		st.AbsentReason = AbsentReasonMonitorUnreachable

		return st
	}

	if b.policyEnabled {
		st.Level = b.level
	}

	return st
}

func clampInt32(v int64) int32 {
	switch {
	case v > math.MaxInt32:
		return math.MaxInt32
	case v < math.MinInt32:
		return math.MinInt32
	}

	return int32(v)
}
