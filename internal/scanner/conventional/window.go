package conventional

import (
	"cmp"
	"slices"

	"github.com/openmanet/openmanetd/internal/scanner"
)

// Window planning constants.
const (
	// rtlMinHz and rtlMaxHz are the R820T/R828D tuning range.
	rtlMinHz = 24_000_000
	rtlMaxHz = 1_766_000_000
	// edgeGuardHz keeps a whole 25 kHz channel inside the usable band.
	edgeGuardHz = 12_500
	// dcGuardHz keeps every channel clear of the tuner's DC spike.
	dcGuardHz = 20_000
	// centerStepHz is the granularity of the DC-avoiding center search.
	centerStepHz = 5_000
	// alwaysOnWeight makes nationwide channels (121.5 guard, national
	// interop calling) win window slots over local ones.
	alwaysOnWeight = 3
)

// window is one capture: a tuner center and the plan channels it
// demodulates in parallel.
type window struct {
	chans  []int // indexes into the plan's channels, ascending frequency
	center uint64
}

// planWindows packs channels into at most maxWindows capture windows of
// usableHz, at most maxPer channels each, by greedy weighted cover: each
// round takes the frequency span covering the most remaining weight
// (nationwide channels weigh alwaysOnWeight). Channels outside the RTL
// tuning range or left over are not scanned. Windows are returned in
// frequency order.
func planWindows(chs []scanner.ConventionalChannel, usableHz uint64, maxWindows, maxPer int) []window {
	if usableHz <= 2*edgeGuardHz || maxWindows <= 0 || maxPer <= 0 {
		return nil
	}

	span := usableHz - 2*edgeGuardHz

	left := make([]int, 0, len(chs))
	for i := range chs {
		f := chs[i].Channel.FrequencyHz
		if f >= rtlMinHz && f <= rtlMaxHz {
			left = append(left, i)
		}
	}

	slices.SortStableFunc(left, func(a, b int) int { return cmp.Compare(chs[a].Channel.FrequencyHz, chs[b].Channel.FrequencyHz) })

	out := make([]window, 0, maxWindows)
	members := make([]int, 0, maxPer)

	for len(left) > 0 && len(out) < maxWindows {
		bestStart, bestEnd, bestScore := 0, 0, -1

		for i := range left {
			lo := chs[left[i]].Channel.FrequencyHz
			score, j := 0, i

			for ; j < len(left) && j-i < maxPer && chs[left[j]].Channel.FrequencyHz-lo <= span; j++ {
				score += weight(&chs[left[j]])
			}

			if score > bestScore {
				bestStart, bestEnd, bestScore = i, j, score
			}
		}

		members = append(members[:0], left[bestStart:bestEnd]...)
		w := window{chans: slices.Clone(members)}
		w.center = centerFor(chs, w.chans, usableHz)
		out = append(out, w)
		left = slices.Delete(left, bestStart, bestEnd)
	}

	slices.SortFunc(out, func(a, b window) int { return cmp.Compare(a.center, b.center) })

	return out
}

func weight(c *scanner.ConventionalChannel) int {
	if c.AlwaysOn {
		return alwaysOnWeight
	}

	return 1
}

// centerFor centers the window on its channels, then moves the center in
// centerStepHz steps (nearest first) until no channel sits within
// dcGuardHz of it, keeping every channel edgeGuardHz inside the usable
// band. A single channel is placed off center by a quarter of the band.
func centerFor(chs []scanner.ConventionalChannel, idx []int, usableHz uint64) uint64 {
	lo, hi := chs[idx[0]].Channel.FrequencyHz, chs[idx[len(idx)-1]].Channel.FrequencyHz
	half := int64(usableHz/2) - edgeGuardHz

	if len(idx) == 1 {
		return lo - usableHz/4
	}

	mid := int64(lo+hi) / 2

	fits := func(c int64) bool {
		if int64(hi)-c > half || c-int64(lo) > half {
			return false
		}

		for _, i := range idx {
			d := int64(chs[i].Channel.FrequencyHz) - c
			if d < dcGuardHz && d > -dcGuardHz {
				return false
			}
		}

		return true
	}

	for k := int64(0); k*centerStepHz <= half; k++ {
		for _, c := range []int64{mid + k*centerStepHz, mid - k*centerStepHz} {
			if fits(c) {
				return uint64(c)
			}
		}
	}

	return uint64(mid)
}
