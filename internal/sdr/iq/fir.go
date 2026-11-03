package iq

import (
	"fmt"
	"math"
)

// maxTaps bounds filter designs so a pathological request (tiny transition
// band at a huge sample rate) cannot exhaust memory on a small device.
const maxTaps = 4095

// KaiserLowPass designs a linear-phase low-pass FIR with a Kaiser window.
// passHz and stopHz bound the transition band; attenDB is the desired
// stop-band attenuation. The taps are normalized to unity DC gain and the
// length is always odd (integer group delay).
func KaiserLowPass(sampleRate, passHz, stopHz, attenDB float64) ([]float32, error) {
	if sampleRate <= 0 || passHz <= 0 || stopHz <= passHz || stopHz > sampleRate/2 {
		return nil, fmt.Errorf("iq: invalid low-pass design fs=%g pass=%g stop=%g", sampleRate, passHz, stopHz)
	}

	attenDB = max(attenDB, 21)
	dw := 2 * math.Pi * (stopHz - passHz) / sampleRate
	n := int(math.Ceil((attenDB-7.95)/(2.285*dw))) + 1

	n |= 1
	if n > maxTaps {
		return nil, fmt.Errorf("iq: low-pass needs %d taps (max %d)", n, maxTaps)
	}

	var beta float64

	switch {
	case attenDB > 50:
		beta = 0.1102 * (attenDB - 8.7)
	case attenDB >= 21:
		beta = 0.5842*math.Pow(attenDB-21, 0.4) + 0.07886*(attenDB-21)
	}

	cutoff := (passHz + stopHz) / 2 / sampleRate

	return windowedSinc(n, cutoff, beta), nil
}

// windowedSinc returns n Kaiser-windowed sinc taps with normalized cutoff
// (cycles/sample), scaled to unity DC gain.
func windowedSinc(n int, cutoff, beta float64) []float32 {
	taps := make([]float32, n)
	mid := float64(n-1) / 2
	i0b := besselI0(beta)

	var sum float64

	vals := make([]float64, n)
	for i := range n {
		x := float64(i) - mid

		s := 2 * cutoff
		if x != 0 {
			s = math.Sin(2*math.Pi*cutoff*x) / (math.Pi * x)
		}

		r := 0.0
		if mid > 0 {
			r = x / mid
		}

		w := besselI0(beta*math.Sqrt(max(0, 1-r*r))) / i0b
		vals[i] = s * w
		sum += vals[i]
	}

	for i, v := range vals {
		taps[i] = float32(v / sum)
	}

	return taps
}

// besselI0 is the zeroth-order modified Bessel function of the first kind,
// evaluated by its power series (converges quickly for Kaiser betas < 20).
func besselI0(x float64) float64 {
	sum, term := 1.0, 1.0
	q := x * x / 4

	for k := 1; k < 64; k++ {
		term *= q / float64(k*k)
		sum += term

		if term < sum*1e-12 {
			break
		}
	}

	return sum
}

// FIRDecimator is a streaming complex FIR filter with real taps that keeps
// one output in every Factor inputs. Only the kept outputs are computed, so
// the cost per input sample is len(taps)/Factor multiply-accumulates (the
// same work as a polyphase decomposition). Not safe for concurrent use.
type FIRDecimator struct {
	taps   []float32 // reversed so taps[0] multiplies the oldest sample
	buf    []complex64
	factor int
	phase  int // inputs still to consume before the next output
	block  int
}

// NewFIRDecimator returns a decimator using taps and keeping every
// factor-th output. block bounds the internal scratch buffer; inputs of any
// length are accepted and processed in block-sized pieces.
func NewFIRDecimator(taps []float32, factor, block int) (*FIRDecimator, error) {
	if len(taps) == 0 || factor < 1 || block < 1 {
		return nil, fmt.Errorf("iq: invalid decimator taps=%d factor=%d block=%d", len(taps), factor, block)
	}

	rev := make([]float32, len(taps))
	for i, t := range taps {
		rev[len(taps)-1-i] = t
	}

	return &FIRDecimator{
		taps:   rev,
		buf:    make([]complex64, len(taps)-1, len(taps)-1+block),
		factor: factor,
		phase:  factor,
		block:  block,
	}, nil
}

// Factor returns the decimation factor.
func (f *FIRDecimator) Factor() int { return f.factor }

// Reset clears the filter history.
func (f *FIRDecimator) Reset() {
	clear(f.buf)
	f.buf = f.buf[:len(f.taps)-1]
	f.phase = f.factor
}

// Process filters src and appends the decimated output to dst[:0]. It does
// not allocate when cap(dst) >= len(src)/Factor()+1.
func (f *FIRDecimator) Process(dst, src []complex64) []complex64 {
	return f.Append(dst[:0], src)
}

// Append filters src and appends the decimated output to dst.
func (f *FIRDecimator) Append(dst, src []complex64) []complex64 {
	hist := len(f.taps) - 1

	for len(src) > 0 {
		n := min(len(src), f.block)
		f.buf = append(f.buf[:hist], src[:n]...)
		src = src[n:]

		// Output k uses buf[p-hist : p+1] where p is the index of the
		// newest contributing input.
		p := hist + f.phase - 1
		for ; p < len(f.buf); p += f.factor {
			dst = append(dst, f.dot(f.buf[p-hist:p+1]))
		}

		f.phase = p - len(f.buf) + 1
		copy(f.buf, f.buf[len(f.buf)-hist:])
	}

	return dst
}

func (f *FIRDecimator) dot(x []complex64) complex64 {
	var re, im float32

	x = x[:len(f.taps)]
	for i, t := range f.taps {
		re += t * real(x[i])
		im += t * imag(x[i])
	}

	return complex(re, im)
}

// RealFIR is a streaming real-valued FIR filter (no decimation) used on
// demodulated audio and discriminator output.
type RealFIR struct {
	taps []float32 // reversed
	hist []float32 // doubled ring: hist[i] == hist[i+n]
	pos  int
}

// NewRealFIR returns a streaming filter with the given taps.
func NewRealFIR(taps []float32) *RealFIR {
	n := len(taps)
	rev := make([]float32, n)

	for i, t := range taps {
		rev[n-1-i] = t
	}

	return &RealFIR{taps: rev, hist: make([]float32, 2*n)}
}

// Reset clears the filter history.
func (r *RealFIR) Reset() {
	clear(r.hist)
	r.pos = 0
}

// Step pushes one sample and returns one filtered output.
func (r *RealFIR) Step(x float32) float32 {
	n := len(r.taps)
	r.hist[r.pos] = x
	r.hist[r.pos+n] = x

	r.pos++
	if r.pos == n {
		r.pos = 0
	}

	// Oldest sample is at hist[pos], newest at hist[pos+n-1].
	w := r.hist[r.pos : r.pos+n]

	var acc float32
	for i, t := range r.taps {
		acc += t * w[i]
	}

	return acc
}

// HighPassFromLowPass converts linear-phase odd-length low-pass taps into
// the complementary high-pass (spectral inversion).
func HighPassFromLowPass(lp []float32) []float32 {
	hp := make([]float32, len(lp))
	for i, t := range lp {
		hp[i] = -t
	}

	hp[len(hp)/2]++

	return hp
}
