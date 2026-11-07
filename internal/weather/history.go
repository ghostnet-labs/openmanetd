package weather

import (
	"math"
	"time"
)

// History bounds. One history point is kept per minute, enough for the
// 3-hour pressure change with some slack.
const (
	historyStep       = time.Minute
	pressureSpan      = 3 * time.Hour
	pressureTolerance = 10 * time.Minute
	rainSpan          = time.Hour
	// rainMinSpan is how much history rain_last_hour needs; a little
	// under an hour so a point landing just late still counts.
	rainMinSpan    = 55 * time.Minute
	historyEntries = int((pressureSpan+pressureTolerance)/historyStep) + 2
	maxWindEntries = 3600
)

type windPoint struct {
	t     time.Time
	speed float64
}

type historyPoint struct {
	t        time.Time
	pressure float64
	rain     float64
	has      Field
}

// gust is the highest wind speed in w since cutoff.
func gust(w *ring[windPoint], cutoff time.Time) (float64, bool) {
	best, ok := 0.0, false

	for i := range w.len() {
		p := w.at(i)
		if p.t.Before(cutoff) {
			continue
		}

		if !ok || p.speed > best {
			best, ok = p.speed, true
		}
	}

	return best, ok
}

// pressureChange is current minus the pressure about pressureSpan before
// now, from the history point closest to that time within the tolerance.
func pressureChange(h *ring[historyPoint], now time.Time, current float64) (float64, bool) {
	target := now.Add(-pressureSpan)
	bestDiff := pressureTolerance + 1
	past, ok := 0.0, false

	for i := range h.len() {
		p := h.at(i)
		if p.has&FieldPressure == 0 {
			continue
		}

		d := p.t.Sub(target)
		if d < 0 {
			d = -d
		}

		if d <= pressureTolerance && d < bestDiff {
			bestDiff, past, ok = d, p.pressure, true
		}
	}

	if !ok {
		return 0, false
	}

	return round1(current - past), true
}

// rainLastHour sums the rain counter's increases over the last hour. A
// drop means the counter was zeroed, so the new value counts as rain
// since the reset. It needs a history point at least rainMinSpan old.
func rainLastHour(h *ring[historyPoint], now time.Time, current float64) (float64, bool) {
	cutoff := now.Add(-rainSpan)
	prev, havePrev, covered := 0.0, false, false

	total := 0.0
	add := func(v float64) {
		if havePrev {
			if v >= prev {
				total += v - prev
			} else {
				total += v
			}
		}

		prev, havePrev = v, true
	}

	for i := range h.len() {
		p := h.at(i)
		if p.has&FieldRainTotal == 0 {
			continue
		}

		if now.Sub(p.t) >= rainMinSpan {
			covered = true
		}

		if p.t.Before(cutoff) {
			// The newest point before the window is the baseline.
			prev, havePrev = p.rain, true

			continue
		}

		add(p.rain)
	}

	if !covered {
		return 0, false
	}

	add(current)

	return math.Round(total*10) / 10, true
}
