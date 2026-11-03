package sites

import "slices"

// Default usable bandwidths. They are planning assumptions (the same ones
// used for the wide_tuners_needed and rtl_sdr_needed columns of
// site-tuner-fit.csv) until bench-measured.
const (
	DefaultWideUsableHz uint64 = 8_000_000
	DefaultRTLUsableHz  uint64 = 2_000_000
)

// TunerPlan is a center frequency for one tuner and the site frequencies
// its usable bandwidth covers.
type TunerPlan struct {
	// Covered and Uncovered partition the site's frequencies, ascending.
	Covered   []uint64
	Uncovered []uint64
	// Control is the preferred control channel inside the window (the
	// first control frequency in source order that is covered); zero when
	// the window holds none.
	Control  uint64
	CenterHz uint64
	UsableHz uint64
	// Fits is true when every site frequency is covered.
	Fits bool
}

// PlanTuner centers a tuner of usableHz on site. The window always holds
// a control channel when one exists and, among such windows, holds the
// most frequencies (ties: more control channels, then the narrowest
// spread). The center is the midpoint of the covered extremes.
func PlanTuner(site *Site, usableHz uint64) TunerPlan {
	all := site.Frequencies()
	slices.Sort(all)
	all = slices.Compact(all)

	plan := TunerPlan{UsableHz: usableHz}
	if len(all) == 0 {
		return plan
	}

	isControl := func(f uint64) bool { return slices.Contains(site.ControlHz, f) }

	bestLo, bestHi := -1, -1
	bestN, bestCtl := 0, 0

	var bestSpread uint64

	// Two-pointer sweep: for each left edge, extend right while the span
	// stays within usableHz. n <= ~100 per site, so O(n^2) control counting
	// is irrelevant next to clarity.
	hi := 0
	for lo := range all {
		if hi < lo {
			hi = lo
		}

		for hi+1 < len(all) && all[hi+1]-all[lo] <= usableHz {
			hi++
		}

		ctl := 0

		for _, f := range all[lo : hi+1] {
			if isControl(f) {
				ctl++
			}
		}

		if ctl == 0 && len(site.ControlHz) > 0 {
			continue
		}

		n, spread := hi-lo+1, all[hi]-all[lo]
		if n > bestN || (n == bestN && ctl > bestCtl) || (n == bestN && ctl == bestCtl && spread < bestSpread) {
			bestLo, bestHi, bestN, bestCtl, bestSpread = lo, hi, n, ctl, spread
		}
	}

	if bestLo < 0 {
		return plan
	}

	plan.CenterHz = all[bestLo] + (all[bestHi]-all[bestLo])/2
	plan.Covered = slices.Clone(all[bestLo : bestHi+1])
	plan.Uncovered = make([]uint64, 0, len(all)-len(plan.Covered))
	plan.Uncovered = append(plan.Uncovered, all[:bestLo]...)
	plan.Uncovered = append(plan.Uncovered, all[bestHi+1:]...)
	plan.Fits = len(plan.Uncovered) == 0

	for _, c := range site.ControlHz {
		if _, ok := slices.BinarySearch(plan.Covered, c); ok {
			plan.Control = c

			break
		}
	}

	return plan
}

// CoveredControl returns the control channels inside the plan's window, in
// the site's source order.
func (p *TunerPlan) CoveredControl(site *Site) []uint64 {
	out := make([]uint64, 0, len(site.ControlHz))

	for _, c := range site.ControlHz {
		if _, ok := slices.BinarySearch(p.Covered, c); ok {
			out = append(out, c)
		}
	}

	return out
}
