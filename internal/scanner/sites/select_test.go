package sites_test

import (
	"testing"

	"github.com/openmanet/openmanetd/internal/scanner/sites"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// lineSites are three synthetic sites on the 40 N parallel, 0.5 degrees
// (~42.6 km) apart.
func lineSites() []sites.Site {
	mk := func(state, name string, lon float64) sites.Site {
		return sites.Site{Key: state + "-" + name, State: state, Name: name, Lat: 40, Lon: lon, ControlHz: []uint64{770_000_000}}
	}

	return []sites.Site{mk("UT", "west", -112), mk("UT", "mid", -111.5), mk("CO", "east", -111)}
}

func TestSelector(t *testing.T) {
	all := lineSites()
	fresh := func(lon float64) sites.Fix { return sites.Fix{Lat: 40, Lon: lon, Valid: true, Fresh: true} }

	type step struct {
		fix     sites.Fix
		filter  sites.StateFilter
		want    string
		reason  sites.Reason
		changed bool
	}

	tests := []struct {
		name  string
		steps []step
	}{
		{
			name: "nearest then hysteresis then switch",
			steps: []step{
				{fix: fresh(-112), want: "west", reason: sites.ReasonNearest, changed: true},
				// Just past the midpoint: mid is nearer by ~4 km < 10 km.
				{fix: fresh(-111.725), want: "west", reason: sites.ReasonHysteresis},
				// Nearer by ~21 km > 10 km.
				{fix: fresh(-111.625), want: "mid", reason: sites.ReasonNearest, changed: true},
				{fix: fresh(-111.5), want: "mid", reason: sites.ReasonNearest},
			},
		},
		{
			name: "stale fix holds the selection",
			steps: []step{
				{fix: fresh(-112), want: "west", reason: sites.ReasonNearest, changed: true},
				{fix: sites.Fix{Lat: 40, Lon: -111, Valid: true}, want: "west", reason: sites.ReasonLastKnown},
				{fix: sites.Fix{}, want: "west", reason: sites.ReasonLastKnown},
			},
		},
		{
			name: "stale fix picks a first site",
			steps: []step{
				{fix: sites.Fix{}, want: "", reason: sites.ReasonNone},
				{fix: sites.Fix{Lat: 40, Lon: -111, Valid: true}, want: "east", reason: sites.ReasonStaleFix, changed: true},
			},
		},
		{
			name: "filter drops the current site",
			steps: []step{
				{fix: fresh(-111.1), want: "east", reason: sites.ReasonNearest, changed: true},
				{fix: fresh(-111.1), filter: sites.StateFilter{"UT"}, want: "mid", reason: sites.ReasonNearest, changed: true},
				{fix: sites.Fix{}, filter: sites.StateFilter{"CO"}, want: "", reason: sites.ReasonNone, changed: true},
				{fix: fresh(-111.1), filter: sites.StateFilter{"NM"}, want: "", reason: sites.ReasonNone},
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			sel := sites.Selector{HysteresisKm: 10}

			for i, st := range tc.steps {
				changed := sel.Update(all, st.fix, st.filter)
				cur, reason := sel.Current()

				name := ""
				if cur != nil {
					name = cur.Name
				}

				assert.Equal(t, st.want, name, "step %d", i)
				assert.Equal(t, st.reason, reason, "step %d", i)
				assert.Equal(t, st.changed, changed, "step %d", i)
			}
		})
	}
}

func TestSelector_manual(t *testing.T) {
	all := lineSites()
	sel := sites.Selector{HysteresisKm: 10}

	sel.Pin(&all[2])
	cur, reason := sel.Current()
	assert.Equal(t, "east", cur.Name)
	assert.Equal(t, sites.ReasonManual, reason)

	// Back to auto: the manual pin is dropped and the nearest site wins at
	// once, without hysteresis.
	assert.True(t, sel.Update(all, sites.Fix{Lat: 40, Lon: -112, Valid: true, Fresh: true}, nil))
	cur, _ = sel.Current()
	assert.Equal(t, "west", cur.Name)

	sel.Reset()
	cur, reason = sel.Current()
	assert.Nil(t, cur)
	assert.Equal(t, sites.ReasonNone, reason)
}

func TestPlanTuner(t *testing.T) {
	const mhz = 1_000_000

	tests := []struct {
		name      string
		site      sites.Site
		usable    uint64
		center    uint64
		control   uint64
		covered   int
		uncovered []uint64
		fits      bool
	}{
		{
			name:    "fits",
			site:    sites.Site{ControlHz: []uint64{770 * mhz}, VoiceHz: []uint64{772 * mhz, 774 * mhz}},
			usable:  8 * mhz,
			center:  772 * mhz,
			control: 770 * mhz,
			covered: 3,
			fits:    true,
		},
		{
			name:      "keeps the control channel over a denser voice cluster",
			site:      sites.Site{ControlHz: []uint64{851 * mhz}, VoiceHz: []uint64{860 * mhz, 861 * mhz, 862 * mhz, 852 * mhz}},
			usable:    2 * mhz,
			center:    851*mhz + mhz/2,
			control:   851 * mhz,
			covered:   2,
			uncovered: []uint64{860 * mhz, 861 * mhz, 862 * mhz},
		},
		{
			name: "prefers the window holding more control channels, first listed control preferred",
			site: sites.Site{
				ControlHz: []uint64{775 * mhz, 860 * mhz, 861 * mhz},
				VoiceHz:   []uint64{774 * mhz, 862 * mhz},
			},
			usable:    8 * mhz,
			center:    861 * mhz,
			control:   860 * mhz,
			covered:   3,
			uncovered: []uint64{774 * mhz, 775 * mhz},
		},
		{
			name:    "exact span fits",
			site:    sites.Site{ControlHz: []uint64{770 * mhz}, VoiceHz: []uint64{778 * mhz}},
			usable:  8 * mhz,
			center:  774 * mhz,
			control: 770 * mhz,
			covered: 2,
			fits:    true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			p := sites.PlanTuner(&tc.site, tc.usable)
			assert.Equal(t, tc.center, p.CenterHz)
			assert.Equal(t, tc.control, p.Control)
			assert.Len(t, p.Covered, tc.covered)
			assert.Equal(t, tc.fits, p.Fits)
			assert.Equal(t, tc.usable, p.UsableHz)

			if tc.uncovered != nil {
				assert.Equal(t, tc.uncovered, p.Uncovered)
			}

			assert.Contains(t, p.CoveredControl(&tc.site), tc.control)
		})
	}

	empty := sites.PlanTuner(&sites.Site{}, 8*mhz)
	assert.Zero(t, empty.CenterHz)
	assert.False(t, empty.Fits)
}

// TestPlanTuner_matchesTable cross-checks the planner against the
// site-tuner-fit.csv columns the table was built from: a site needing one
// tuner must fit in one window of that tuner's usable width.
func TestPlanTuner_matchesTable(t *testing.T) {
	all := table(t)

	for i := range all {
		s := &all[i]

		wide := sites.PlanTuner(s, sites.DefaultWideUsableHz)
		require.NotZero(t, wide.Control, s.Key)
		assert.Equal(t, s.WideNeeded == 1, wide.Fits, "%s wide (span %d)", s.Name, s.SpanHz())

		rtl := sites.PlanTuner(s, sites.DefaultRTLUsableHz)
		assert.Equal(t, s.RTLNeeded == 1, rtl.Fits, "%s rtl (span %d)", s.Name, s.SpanHz())

		if wide.Fits {
			assert.LessOrEqual(t, s.HighHz-wide.CenterHz, sites.DefaultWideUsableHz/2)
			assert.LessOrEqual(t, wide.CenterHz-s.LowHz, sites.DefaultWideUsableHz/2)
		}
	}
}
