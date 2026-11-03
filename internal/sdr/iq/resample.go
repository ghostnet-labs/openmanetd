package iq

import "fmt"

// Resampler converts a real-valued stream between arbitrary rates using
// 4-point cubic (Catmull-Rom) interpolation. It assumes the input is
// already band-limited well below the output Nyquist frequency (audio after
// a voice-band low-pass), which keeps interpolation images negligible.
// Not safe for concurrent use.
type Resampler struct {
	hist [4]float32
	step float64 // input samples per output sample
	pos  float64 // position of the next output relative to hist[1]
}

// NewResampler returns a resampler from inRate to outRate.
func NewResampler(inRate, outRate float64) (*Resampler, error) {
	if inRate <= 0 || outRate <= 0 {
		return nil, fmt.Errorf("iq: invalid resampler rates %g -> %g", inRate, outRate)
	}

	return &Resampler{step: inRate / outRate}, nil
}

// Push consumes one input sample and appends zero or more outputs to dst.
func (r *Resampler) Push(dst []float32, x float32) []float32 {
	r.hist[0], r.hist[1], r.hist[2], r.hist[3] = r.hist[1], r.hist[2], r.hist[3], x
	r.pos--

	// Interpolate between hist[1] and hist[2] for 0 <= pos < 1.
	for r.pos < 0 {
		t := float32(r.pos + 1)
		r.pos += r.step
		dst = append(dst, catmullRom(r.hist, t))
	}

	return dst
}

func catmullRom(p [4]float32, t float32) float32 {
	a := -0.5*p[0] + 1.5*p[1] - 1.5*p[2] + 0.5*p[3]
	b := p[0] - 2.5*p[1] + 2*p[2] - 0.5*p[3]
	c := -0.5*p[0] + 0.5*p[2]

	return ((a*t+b)*t+c)*t + p[1]
}
