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
	p := s.phasor

	for i, x := range src {
		dst[i] = x * complex64(p)
		p *= s.step
	}

	// Renormalize to stop amplitude drift from accumulated rounding.
	s.phasor = p / complex(cmplx.Abs(p), 0)

	return dst
}
