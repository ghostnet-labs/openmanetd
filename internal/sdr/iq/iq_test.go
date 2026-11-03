package iq_test

import (
	"math"
	"math/cmplx"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/openmanet/openmanetd/internal/sdr/iq"
)

func tone(n int, freq, rate float64) []complex64 {
	out := make([]complex64, n)
	for i := range out {
		out[i] = complex64(cmplx.Rect(0.5, 2*math.Pi*freq*float64(i)/rate))
	}

	return out
}

// toneAmplitude correlates x against a complex exponential at freq and
// returns the recovered amplitude (skipping the first skip samples).
func toneAmplitude(x []complex64, freq, rate float64, skip int) float64 {
	var acc complex128

	x = x[skip:]
	for i, v := range x {
		acc += complex128(v) * cmplx.Rect(1, -2*math.Pi*freq*float64(i)/rate)
	}

	return cmplx.Abs(acc) / float64(len(x))
}

func TestConverter_CU8(t *testing.T) {
	c, err := iq.NewConverter(iq.FormatCU8)
	require.NoError(t, err)

	out := c.Convert(nil, []byte{0, 255, 128, 127})
	require.Len(t, out, 2)
	assert.InDelta(t, -1, real(out[0]), 1e-6)
	assert.InDelta(t, 1, imag(out[0]), 1e-6)
	assert.InDelta(t, 0.5/127.5, real(out[1]), 1e-6)
	assert.InDelta(t, -0.5/127.5, imag(out[1]), 1e-6)
}

func TestConverter_carryAcrossReads(t *testing.T) {
	src := make([]complex64, 101)
	for i := range src {
		src[i] = complex(float32(i%7)/8-0.4, -float32(i%5)/6+0.3)
	}

	for _, f := range []iq.Format{iq.FormatCU8, iq.FormatCS16, iq.FormatCF32} {
		t.Run(f.String(), func(t *testing.T) {
			var raw []byte

			switch f {
			case iq.FormatCU8:
				raw = iq.AppendCU8(nil, src)
			case iq.FormatCS16:
				raw = iq.AppendCS16(nil, src)
			case iq.FormatCF32:
				for _, s := range src {
					raw = appendF32(raw, real(s))
					raw = appendF32(raw, imag(s))
				}
			}

			whole, err := iq.NewConverter(f)
			require.NoError(t, err)

			want := whole.Convert(nil, raw)
			require.Len(t, want, len(src))

			// Feed in awkward odd-sized pieces; the result must match.
			split, err := iq.NewConverter(f)
			require.NoError(t, err)

			var got []complex64

			for i, step := 0, 1; i < len(raw); step = step%7 + 2 {
				end := min(i+step, len(raw))
				got = append(got, split.Convert(nil, raw[i:end])...)
				i = end
			}

			assert.Equal(t, want, got)

			tol := 1.0 / 127.0
			if f != iq.FormatCU8 {
				tol = 1e-4
			}

			for i := range src {
				assert.InDelta(t, real(src[i]), real(got[i]), tol)
				assert.InDelta(t, imag(src[i]), imag(got[i]), tol)
			}
		})
	}
}

func appendF32(b []byte, v float32) []byte {
	u := math.Float32bits(v)

	return append(b, byte(u), byte(u>>8), byte(u>>16), byte(u>>24))
}

func TestParseFormat(t *testing.T) {
	for _, name := range []string{"cu8", "cs16", "cf32"} {
		f, err := iq.ParseFormat(name)
		require.NoError(t, err)
		assert.Equal(t, name, f.String())
	}

	_, err := iq.ParseFormat("wav")
	require.Error(t, err)

	_, err = iq.NewConverter(iq.Format(99))
	require.Error(t, err)
}

func TestKaiserLowPass_response(t *testing.T) {
	const fs = 48000.0

	taps, err := iq.KaiserLowPass(fs, 6000, 8500, 60)
	require.NoError(t, err)
	require.Equal(t, 1, len(taps)%2, "odd length")

	resp := func(f float64) float64 {
		var acc complex128
		for i, h := range taps {
			acc += complex(float64(h), 0) * cmplx.Rect(1, -2*math.Pi*f*float64(i)/fs)
		}

		return cmplx.Abs(acc)
	}

	assert.InDelta(t, 1, resp(0), 1e-3)
	assert.InDelta(t, 1, resp(5000), 0.01)

	for _, f := range []float64{8500, 10000, 15000, 23000} {
		assert.Less(t, 20*math.Log10(resp(f)), -58.0, "stop band at %g Hz", f)
	}

	_, err = iq.KaiserLowPass(fs, 8000, 6000, 60)
	require.Error(t, err)
}

func TestFIRDecimator_matchesDirectConvolution(t *testing.T) {
	taps := []float32{0.1, 0.2, 0.4, 0.2, 0.1}
	src := make([]complex64, 200)

	for i := range src {
		src[i] = complex(float32(i%13)-6, float32(i%9)-4)
	}

	const factor = 3

	var want []complex64

	for n := factor - 1; n < len(src); n += factor {
		var acc complex64

		for k, h := range taps {
			if n-k >= 0 {
				acc += complex(h, 0) * src[n-k]
			}
		}

		want = append(want, acc)
	}

	for _, block := range []int{1, 4, 7, 64, 1000} {
		d, err := iq.NewFIRDecimator(taps, factor, block)
		require.NoError(t, err)

		var got []complex64

		for i, step := 0, 1; i < len(src); step = step%11 + 1 {
			end := min(i+step, len(src))
			got = append(got, d.Process(nil, src[i:end])...)
			i = end
		}

		require.Len(t, got, len(want))

		for i := range want {
			assert.InDelta(t, real(want[i]), real(got[i]), 1e-4, "block %d i %d", block, i)
			assert.InDelta(t, imag(want[i]), imag(got[i]), 1e-4, "block %d i %d", block, i)
		}
	}
}

func TestChain_channelSelectivity(t *testing.T) {
	cases := []struct {
		name   string
		rate   float64
		factor int
	}{
		{"rtl_1024k", 1_024_000, 21},
		{"rtl_2400k", 2_400_000, 50},
		{"airspy_2500k", 2_500_000, 52},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c, err := iq.NewChain(tc.rate, tc.factor, 6000, 8500, 60, 4096)
			require.NoError(t, err)

			pass := c.Process(nil, tone(200_000, 3000, tc.rate))
			assert.InDelta(t, 0.5, toneAmplitude(pass, 3000, c.OutRate(), 500), 0.01)

			// An adjacent 12.5 kHz channel and a far-away signal that
			// would alias into the channel without filtering.
			for _, f := range []float64{12500, c.OutRate() + 2000, 150_000} {
				out := c.Process(nil, tone(200_000, f, tc.rate))
				alias := math.Mod(f+c.OutRate()/2, c.OutRate()) - c.OutRate()/2
				amp := toneAmplitude(out, alias, c.OutRate(), 500)
				assert.Less(t, 20*math.Log10(amp/0.5), -55.0, "tone %g Hz", f)
			}
		})
	}
}

func TestChain_stages(t *testing.T) {
	c, err := iq.NewChain(2_400_000, 50, 6000, 8500, 60, 1024)
	require.NoError(t, err)
	assert.Equal(t, []int{10, 5}, c.Stages())
	assert.InDelta(t, 48000, c.OutRate(), 1e-9)

	c, err = iq.NewChain(48000, 1, 6000, 8500, 60, 1024)
	require.NoError(t, err)
	assert.Equal(t, []int{1}, c.Stages())

	_, err = iq.NewChain(48000, 4, 6000, 8500, 60, 1024)
	require.Error(t, err, "stop edge above output nyquist")
}

func TestChain_zeroAllocs(t *testing.T) {
	c, err := iq.NewChain(1_024_000, 21, 6000, 8500, 60, 4096)
	require.NoError(t, err)

	src := tone(16384, 1000, 1_024_000)
	dst := make([]complex64, 0, len(src)/21+1)

	allocs := testing.AllocsPerRun(20, func() { dst = c.Process(dst, src) })
	assert.Zero(t, allocs)
}

func TestShifter_movesToneToBaseband(t *testing.T) {
	const rate = 1_024_000.0

	s := iq.NewShifter(100_000, rate)
	src := tone(50_000, 100_250, rate)

	var out []complex64

	for i := 0; i < len(src); i += 777 {
		out = append(out, s.Mix(nil, src[i:min(i+777, len(src))])...)
	}

	assert.InDelta(t, 0.5, toneAmplitude(out, 250, rate, 0), 1e-3)
	assert.InDelta(t, 100_000, s.Offset(), 0)

	// In-place mixing is allowed and does not allocate.
	allocs := testing.AllocsPerRun(10, func() { s.Mix(src, src) })
	assert.Zero(t, allocs)
}

func TestResampler_toneFrequency(t *testing.T) {
	const in, out = 44_100.0, 8000.0

	r, err := iq.NewResampler(in, out)
	require.NoError(t, err)

	var y []float32

	for i := range 44_100 {
		y = r.Push(y, float32(math.Sin(2*math.Pi*1000*float64(i)/in)))
	}

	assert.InDelta(t, 8000, len(y), 2)

	// Correlate against the expected 1 kHz output tone.
	var acc complex128
	for i, v := range y[100:] {
		acc += complex(float64(v), 0) * cmplx.Rect(1, -2*math.Pi*1000*float64(i)/out)
	}

	assert.InDelta(t, 0.5, cmplx.Abs(acc)/float64(len(y)-100), 0.01)

	_, err = iq.NewResampler(0, 8000)
	require.Error(t, err)
}

func TestRealFIR_highPass(t *testing.T) {
	lp, err := iq.KaiserLowPass(32000, 3000, 5000, 50)
	require.NoError(t, err)

	hp := iq.NewRealFIR(iq.HighPassFromLowPass(lp))

	var lowE, highE float64

	for i := range 8000 {
		v := hp.Step(float32(math.Sin(2*math.Pi*500*float64(i)/32000) + math.Sin(2*math.Pi*9000*float64(i)/32000)))
		if i < 200 {
			continue
		}

		lowE += float64(v) * float64(v)
	}

	hp2 := iq.NewRealFIR(iq.HighPassFromLowPass(lp))
	for i := range 8000 {
		v := hp2.Step(float32(math.Sin(2 * math.Pi * 9000 * float64(i) / 32000)))
		if i < 200 {
			continue
		}

		highE += float64(v) * float64(v)
	}

	// The 500 Hz component is rejected: total energy ~= the 9 kHz part.
	assert.InEpsilon(t, highE, lowE, 0.01)
}

func BenchmarkChain_1024k(b *testing.B) {
	c, err := iq.NewChain(1_024_000, 21, 6000, 8500, 60, 4096)
	require.NoError(b, err)

	src := tone(16384, 1000, 1_024_000)
	dst := make([]complex64, 0, len(src)/21+1)

	b.ReportAllocs()
	b.SetBytes(int64(len(src) * 8))

	for b.Loop() {
		dst = c.Process(dst, src)
	}
}

func BenchmarkConverter_CU8(b *testing.B) {
	c, err := iq.NewConverter(iq.FormatCU8)
	require.NoError(b, err)

	raw := make([]byte, 65536)
	dst := make([]complex64, 0, len(raw)/2)

	b.ReportAllocs()
	b.SetBytes(int64(len(raw)))

	for b.Loop() {
		dst = c.Convert(dst, raw)
	}
}
