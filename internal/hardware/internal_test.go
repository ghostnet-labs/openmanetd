package hardware

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestOutputSpecs_OpenDrainAndForbiddenLines(t *testing.T) {
	openDrain := map[string]bool{
		LineGNSSResetN: true, LineHaLowResetN: true, LineHaLowWakeN: true, LineWiFiWDis1N: true,
	}

	for _, s := range outputSpecs() {
		assert.True(t, s.Output, s.Name)
		assert.Equal(t, openDrain[s.Name], s.OpenDrain, "%s open-drain", s.Name)
	}

	forbidden := []string{
		"GNSS_PPS", "SUPERVISOR_WDI", "SUPERVISOR_WDO", "SYS_I2C_SDA", "SYS_I2C_SCL",
		"GNSS_UART_TX", "GNSS_UART_RX", "",
	}

	names := make([]string, 0, 16)
	for _, s := range outputSpecs() {
		names = append(names, s.Name)
	}

	names = append(names, supervisorArmSpec().Name)

	names = append(names, FaultInputNames()...)

	for _, f := range forbidden {
		assert.NotContains(t, names, f)
	}
}

func TestSupervisorArmSpec(t *testing.T) {
	s := supervisorArmSpec()

	assert.Equal(t, LineSupervisorARM, s.Name)
	assert.True(t, s.Output)
	assert.False(t, s.ActiveLow, "asserted (armed) is electrically high")
	assert.False(t, s.OpenDrain, "push-pull against the 10 kOhm pull-down")
	assert.True(t, s.StartDeasserted, "requested low, never adopted high")
	assert.NotContains(t, FaultInputNames(), s.Name)

	for _, o := range outputSpecs() {
		assert.NotEqual(t, s.Name, o.Name, "the arm follows the watchdog, not radio actuation")
	}
}

func TestFaultInputs_Polarity(t *testing.T) {
	for i, in := range faultInputs() {
		assert.Equal(t, FaultLine(i+1), in.line, "faultInputs is in FaultLine order")
		assert.False(t, in.spec.Output, in.spec.Name)

		if in.line == FaultLinePowerGood {
			assert.False(t, in.spec.ActiveLow)
			assert.False(t, in.faultWhenAsserted, "POWER_GOOD asserted means healthy")

			continue
		}

		assert.True(t, in.spec.ActiveLow, in.spec.Name)
		assert.True(t, in.faultWhenAsserted, in.spec.Name)
	}
}

func TestCooldownBackoff(t *testing.T) {
	want := []time.Duration{10 * time.Second, 30 * time.Second, 2 * time.Minute, 10 * time.Minute, 10 * time.Minute, 10 * time.Minute}
	for i, w := range want {
		assert.Equal(t, w, cooldownBackoff(uint32(i)))
	}
}

func TestSeverity(t *testing.T) {
	tests := []struct {
		mv, margin int64
		want       BatteryLevel
	}{
		{12600, 0, BatteryLevelNormal},
		{10500, 0, BatteryLevelNormal},
		{10499, 0, BatteryLevelWarning},
		{9900, 0, BatteryLevelWarning},
		{9899, 0, BatteryLevelCritical},
		{9599, 0, BatteryLevelShutdown},
		{10600, levelHysteresisMV, BatteryLevelWarning},
		{10700, levelHysteresisMV, BatteryLevelNormal},
	}

	for _, tc := range tests {
		assert.Equal(t, tc.want, severity(tc.mv, tc.margin), "%d mV margin %d", tc.mv, tc.margin)
	}
}

func TestEventRing_Wraps(t *testing.T) {
	var r eventRing

	base := time.Unix(0, 0)
	for i := range 100 {
		r.add(Event{At: base.Add(time.Duration(i) * time.Second), Radio: Radio(i%2 + 1)})
	}

	all := r.newest(0, RadioUnspecified)
	assert.Len(t, all, eventRingSize)
	assert.Equal(t, base.Add(99*time.Second), all[0].At)
	assert.Equal(t, base.Add(36*time.Second), all[len(all)-1].At)

	halow := r.newest(0, RadioHaLow)
	assert.Len(t, halow, eventRingSize/2)

	for _, e := range halow {
		assert.Equal(t, RadioHaLow, e.Radio)
	}

	assert.Len(t, r.newest(3, RadioWiFi), 3)
}

func TestPrune(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	ts := []time.Time{now.Add(-25 * time.Hour), now.Add(-time.Hour)}
	assert.Equal(t, []time.Time{now.Add(-time.Hour)}, prune(ts, now))

	many := make([]time.Time, 0, 40)
	for i := range 40 {
		many = append(many, now.Add(-time.Duration(40-i)*time.Minute))
	}

	got := prune(many, now)
	assert.Len(t, got, recordMaxEntries)
	assert.Equal(t, now.Add(-time.Minute), got[len(got)-1], "newest kept")
}
