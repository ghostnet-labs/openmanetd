package iq

import (
	"fmt"
	"slices"
)

// maxStageFactor caps the decimation done by one FIR stage. Composite
// factors are split into stages no larger than this so early stages run
// short, cheap filters at the high input rate.
const maxStageFactor = 16

// Chain is a cascade of FIRDecimator stages that pulls a narrow channel
// (|f| <= passHz around DC) out of a wideband capture. Each stage's filter
// only has to reject energy that would alias into the final channel, so the
// expensive sharp filter runs at the lowest rate. Not safe for concurrent
// use.
type Chain struct {
	stages  []*FIRDecimator
	scratch [][]complex64
	outRate float64
	factor  int
	block   int
}

// NewChain designs a decimation cascade from inRate by factor (factor 1 runs
// a single channel filter without decimation). passHz/stopHz describe the
// final channel filter edges at the output rate and attenDB the stop-band
// attenuation of every stage. block bounds per-stage scratch memory.
func NewChain(inRate float64, factor int, passHz, stopHz, attenDB float64, block int) (*Chain, error) {
	if factor < 1 || block < 1 {
		return nil, fmt.Errorf("iq: invalid chain factor=%d block=%d", factor, block)
	}

	outRate := inRate / float64(factor)
	if stopHz > outRate/2 {
		return nil, fmt.Errorf("iq: stop edge %g Hz above output Nyquist %g Hz", stopHz, outRate/2)
	}

	factors := splitFactor(factor)
	c := &Chain{outRate: outRate, factor: factor, block: block}
	rate := inRate
	stageBlock := block

	for i, f := range factors {
		stageOut := rate / float64(f)
		stop := stageOut - stopHz // first frequency that folds into the channel

		if i == len(factors)-1 {
			stop = stopHz
		}

		taps, err := KaiserLowPass(rate, passHz, min(stop, rate/2), attenDB)
		if err != nil {
			return nil, fmt.Errorf("iq: chain stage %d: %w", i, err)
		}

		d, err := NewFIRDecimator(taps, f, stageBlock)
		if err != nil {
			return nil, err
		}

		c.stages = append(c.stages, d)
		stageBlock = stageBlock/f + 1
		c.scratch = append(c.scratch, make([]complex64, 0, stageBlock))
		rate = stageOut
	}

	return c, nil
}

// splitFactor factors n into stage factors <= maxStageFactor where
// possible, largest first, with the smallest factor last so the sharpest
// filter runs at the lowest input rate.
func splitFactor(n int) []int {
	if n == 1 {
		return []int{1}
	}

	var primes []int

	for p := 2; p*p <= n; p++ {
		for n%p == 0 {
			primes = append(primes, p)
			n /= p
		}
	}

	if n > 1 {
		primes = append(primes, n)
	}

	slices.SortFunc(primes, func(a, b int) int { return b - a })

	var out []int

	cur := 1
	for _, p := range primes {
		if cur > 1 && cur*p > maxStageFactor {
			out = append(out, cur)
			cur = 1
		}

		cur *= p
	}

	out = append(out, cur)
	slices.SortFunc(out, func(a, b int) int { return b - a })

	return out
}

// OutRate returns the output sample rate.
func (c *Chain) OutRate() float64 { return c.outRate }

// Factor returns the overall decimation factor.
func (c *Chain) Factor() int { return c.factor }

// Stages returns the per-stage decimation factors (for diagnostics).
func (c *Chain) Stages() []int {
	out := make([]int, len(c.stages))
	for i, s := range c.stages {
		out[i] = s.Factor()
	}

	return out
}

// Reset clears every stage's history.
func (c *Chain) Reset() {
	for _, s := range c.stages {
		s.Reset()
	}
}

// Process runs src through the cascade and appends the output to dst[:0].
// It does not allocate when cap(dst) >= len(src)/Factor()+1.
func (c *Chain) Process(dst, src []complex64) []complex64 {
	dst = dst[:0]

	for len(src) > 0 {
		n := min(len(src), c.block)
		cur := src[:n]
		src = src[n:]

		last := len(c.stages) - 1
		for i, s := range c.stages {
			if i == last {
				dst = s.Append(dst, cur)

				break
			}

			c.scratch[i] = s.Process(c.scratch[i], cur)
			cur = c.scratch[i]
		}
	}

	return dst
}
