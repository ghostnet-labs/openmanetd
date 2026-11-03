package iq

import (
	"math"
	"math/cmplx"
)

// Shifter is a numerically controlled oscillator that mixes a channel at
// offsetHz down to baseband (multiplies by exp(-j*2*pi*offset*t)). The
// phasor is advanced by complex multiplication rather than per-sample trig
// and renormalized once per block, so the hot loop is one complex multiply
// per sample. Not safe for concurrent use.
type Shifter struct {
	phasor     complex128
	step       complex128
	step4      complex128 // step^4
	sampleRate float64
	offsetHz   float64
}

// NewShifter returns a Shifter that moves a signal at offsetHz to 0 Hz.
func NewShifter(offsetHz, sampleRate float64) *Shifter {
	s := &Shifter{phasor: 1, sampleRate: sampleRate}
	s.SetOffset(offsetHz)

	return s
}

// SetOffset changes the shift frequency while keeping phase continuity.
func (s *Shifter) SetOffset(offsetHz float64) {
	s.offsetHz = offsetHz
	s.step = cmplx.Rect(1, -2*math.Pi*offsetHz/s.sampleRate)
	s.step4 = cmplx.Rect(1, -8*math.Pi*offsetHz/s.sampleRate)
}

// Offset returns the current shift frequency in Hz.
func (s *Shifter) Offset() float64 { return s.offsetHz }

// Mix writes the shifted src into dst (which may alias src) and returns
// dst[:len(src)]. It allocates only when cap(dst) < len(src).
func (s *Shifter) Mix(dst, src []complex64) []complex64 {
	if cap(dst) < len(src) {
		dst = make([]complex64, len(src))
	}

	dst = dst[:len(src)]

	// Four interleaved phasors (p, p*w, p*w^2, p*w^3, each advanced by
	// w^4) break the multiply-to-multiply dependency so the loop is
	// throughput- rather than latency-bound. The arithmetic is spelled out
	// in reals: Go's complex multiply adds NaN/Inf checks that cost as
	// much as the multiply.
	var pr, pi [4]float64

	pr[0], pi[0] = real(s.phasor), imag(s.phasor)
	for k := 1; k < 4; k++ {
		pr[k], pi[k] = pr[k-1]*real(s.step)-pi[k-1]*imag(s.step), pr[k-1]*imag(s.step)+pi[k-1]*real(s.step)
	}

	sr, si := real(s.step4), imag(s.step4)
	n4 := len(src) &^ 3

	for i := 0; i < n4; i += 4 {
		x := src[i : i+4 : i+4]
		o := dst[i : i+4 : i+4]

		for k := range 4 {
			xr, xi := float64(real(x[k])), float64(imag(x[k]))
			o[k] = complex(float32(xr*pr[k]-xi*pi[k]), float32(xr*pi[k]+xi*pr[k]))
			pr[k], pi[k] = pr[k]*sr-pi[k]*si, pr[k]*si+pi[k]*sr
		}
	}

	for k, x := range src[n4:] {
		xr, xi := float64(real(x)), float64(imag(x))
		dst[n4+k] = complex(float32(xr*pr[k]-xi*pi[k]), float32(xr*pi[k]+xi*pr[k]))
	}

	// The next sample's phasor is lane len%4; renormalize it to stop
	// amplitude drift from accumulated rounding.
	p := complex(pr[len(src)-n4], pi[len(src)-n4])
	s.phasor = p / complex(cmplx.Abs(p), 0)

	return dst
}
