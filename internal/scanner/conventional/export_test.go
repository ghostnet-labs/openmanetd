package conventional

import "github.com/openmanet/openmanetd/internal/scanner"

// WindowForTest is a planned window: its center and channel indexes.
type WindowForTest struct {
	Chans    []int
	CenterHz uint64
}

// PlanWindowsForTest exposes planWindows.
func PlanWindowsForTest(chs []scanner.ConventionalChannel, usableHz uint64, maxWindows, maxPer int) []WindowForTest {
	ws := planWindows(chs, usableHz, maxWindows, maxPer)
	out := make([]WindowForTest, 0, len(ws))

	for _, w := range ws {
		out = append(out, WindowForTest{Chans: w.chans, CenterHz: w.center})
	}

	return out
}

// ConsumerForTest builds a visitor on the first window of plan and
// returns its consume function, for allocation tests and benchmarks.
func (s *Scanner) ConsumerForTest(plan scanner.ConventionalPlan) (func([]complex64), error) {
	if err := s.Scan(plan); err != nil {
		return nil, err
	}

	s.mu.Lock()
	ws := s.windows
	s.mu.Unlock()

	v := newVisitor(s, plan, ws)
	v.candidates = append(v.candidates, 0)

	if err := v.begin(0, func() {}); err != nil {
		return nil, err
	}

	v.settle = 0

	return v.consume, nil
}
