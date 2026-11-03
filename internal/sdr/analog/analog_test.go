package analog_test

import (
	"math"
	"math/cmplx"
	"math/rand/v2"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/openmanet/openmanetd/internal/sdr/analog"
)

const rtlRate = 1_024_000

// fmTone synthesizes an FM carrier modulated by a tone, plus complex
// Gaussian noise for the given CNR in bandwidthHz (0 disables noise).
func fmTone(rate, seconds, toneHz, devHz, cnrDB, bandwidthHz float64, seed uint64) []complex64 {
	n := int(rate * seconds)
	out := make([]complex64, n)
	sigma := noiseSigma(0.25, cnrDB, rate, bandwidthHz)
	rng := rand.New(rand.NewPCG(seed, 1))
	phase := 0.0

	for i := range out {
		f := devHz * math.Sin(2*math.Pi*toneHz*float64(i)/rate)
		phase = math.Mod(phase+2*math.Pi*f/rate, 2*math.Pi)
		out[i] = complex64(cmplx.Rect(0.25, phase) + gauss(rng, sigma))
	}

	return out
}

// amTone synthesizes an AM carrier (amplitude amp) with modulation depth m.
func amTone(rate, seconds, toneHz, m, amp, cnrDB, bandwidthHz float64, seed uint64) []complex64 {
	n := int(rate * seconds)
	out := make([]complex64, n)
	sigma := noiseSigma(amp, cnrDB, rate, bandwidthHz)
	rng := rand.New(rand.NewPCG(seed, 2))

	for i := range out {
		a := amp * (1 + m*math.Sin(2*math.Pi*toneHz*float64(i)/rate))
		out[i] = complex64(complex(a, 0) + gauss(rng, sigma))
	}

	return out
}

func noiseSigma(amp, cnrDB, rate, bw float64) float64 {
	if cnrDB <= 0 {
		return 0
	}

	return math.Sqrt(amp * amp / math.Pow(10, cnrDB/10) * rate / bw / 2)
}

func gauss(rng *rand.Rand, sigma float64) complex128 {
	if sigma == 0 {
		return 0
	}

	return complex(rng.NormFloat64()*sigma, rng.NormFloat64()*sigma)
}

type capture struct {
	pcm    []int16
	opens  []bool
	levels []float64
}

func (c *capture) emit(b analog.Block) {
	c.pcm = append(c.pcm, b.PCM...)
	c.opens = append(c.opens, b.Open)
	c.levels = append(c.levels, b.Level)
}

func run(t *testing.T, cfg analog.Config, iqs []complex64) *capture {
	t.Helper()

	c := &capture{}
	d, err := analog.New(cfg, c.emit)
	require.NoError(t, err)

	for i := 0; i < len(iqs); i += 10_000 {
		d.Write(iqs[i:min(i+10_000, len(iqs))])
	}

	return c
}

// toneStats fits a sinusoid at the zero-crossing frequency estimate and
// returns that frequency and the SNR of the fit against the residual.
func toneStats(pcm []int16) (freq, snrDB float64) {
	x := make([]float64, len(pcm))
	for i, v := range pcm {
		x[i] = float64(v)
	}

	// Interpolated rising zero crossings.
	var first, last float64

	crossings := 0

	for i := 1; i < len(x); i++ {
		if x[i-1] < 0 && x[i] >= 0 {
			tc := float64(i-1) + x[i-1]/(x[i-1]-x[i])
			if crossings == 0 {
				first = tc
			}

			last = tc
			crossings++
		}
	}

	if crossings < 2 {
		return 0, math.Inf(-1)
	}

	freq = float64(crossings-1) / (last - first) * analog.AudioRate

	var c, s complex128

	for i, v := range x {
		w := 2 * math.Pi * freq * float64(i) / analog.AudioRate
		c += complex(v*math.Cos(w), 0)
		s += complex(v*math.Sin(w), 0)
	}

	a, b := 2*real(c)/float64(len(x)), 2*real(s)/float64(len(x))

	var sig, res float64

	for i, v := range x {
		w := 2 * math.Pi * freq * float64(i) / analog.AudioRate
		fit := a*math.Cos(w) + b*math.Sin(w)
		sig += fit * fit
		res += (v - fit) * (v - fit)
	}

	return freq, 10 * math.Log10(sig/res)
}

func TestNFM_recoversTone(t *testing.T) {
	cases := []struct {
		name    string
		cfg     analog.Config
		dev     float64
		cnr     float64
		minSNR  float64
		toneAmp float64 // expected PCM amplitude, fraction of full scale
	}{
		{"nb_clean", analog.Config{SampleRate: rtlRate, Mode: analog.ModeNFM}, 2500, 0, 30, 0.7},
		{"nb_cnr20", analog.Config{SampleRate: rtlRate, Mode: analog.ModeNFM}, 2500, 20, 20, 0.7},
		{"nb_deemph", analog.Config{SampleRate: rtlRate, Mode: analog.ModeNFM, Deemphasis: analog.NFMDeemphasis}, 2500, 20, 20, 0.7},
		{"wide_25k", analog.Config{SampleRate: 2_400_000, Mode: analog.ModeNFM, ChannelSpacingHz: 25000}, 5000, 25, 20, 0.7},
		{"airspy_2500k", analog.Config{SampleRate: 2_500_000, Mode: analog.ModeNFM}, 2500, 25, 20, 0.7},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			iqs := fmTone(float64(tc.cfg.SampleRate), 1, 1000, tc.dev, tc.cnr, 12500, 1)
			c := run(t, tc.cfg, iqs)

			require.InDelta(t, analog.AudioRate, len(c.pcm), 200)
			assert.True(t, c.opens[len(c.opens)-1], "squelch open")

			steady := c.pcm[1600:]
			freq, snr := toneStats(steady)
			assert.InDelta(t, 1000, freq, 2, "tone frequency")
			assert.Greater(t, snr, tc.minSNR, "tone SNR")

			var peak float64
			for _, v := range steady {
				peak = max(peak, math.Abs(float64(v)))
			}

			assert.InEpsilon(t, tc.toneAmp*math.MaxInt16, peak, 0.2, "level")
		})
	}
}

func TestNFM_noiseSquelch(t *testing.T) {
	rng := rand.New(rand.NewPCG(4, 4))
	noiseOnly := make([]complex64, rtlRate/2)

	for i := range noiseOnly {
		noiseOnly[i] = complex64(gauss(rng, 0.05))
	}

	// Noise, then a 15 dB CNR signal, then noise again.
	iqs := append([]complex64{}, noiseOnly...)
	iqs = append(iqs, fmTone(rtlRate, 0.5, 1000, 2500, 15, 12500, 2)...)
	iqs = append(iqs, noiseOnly...)

	c := run(t, analog.Config{SampleRate: rtlRate, Mode: analog.ModeNFM}, iqs)
	n := len(c.opens)
	require.Equal(t, 75, n) // 1.5 s of 20 ms blocks

	assert.NotContains(t, c.opens[:24], true, "closed on noise")
	assert.NotContains(t, c.opens[27:50], false, "open on signal")
	assert.NotContains(t, c.opens[52:], true, "closed again on noise")
	assert.Greater(t, c.levels[10], 1.0, "pure noise level")
	assert.Less(t, c.levels[38], analog.DefaultNoiseSquelch)

	for _, v := range c.pcm[:24*analog.DefaultBlockSamples] {
		require.Zero(t, v, "muted while closed")
	}
}

func TestAM_recoversTone(t *testing.T) {
	cases := []struct {
		name    string
		spacing int
		tone    float64
		minSNR  float64 // ~CNR - 10log10(2/m^2) for m = 0.5 in the audio band
	}{
		{"am_25k", 25000, 1000, 20},
		{"am_8k33", 8330, 800, 17},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			iqs := amTone(rtlRate, 1, tc.tone, 0.5, 0.1, 25, float64(tc.spacing), 3)
			c := run(t, analog.Config{SampleRate: rtlRate, Mode: analog.ModeAM, ChannelSpacingHz: tc.spacing}, iqs)

			assert.True(t, c.opens[len(c.opens)-1])
			assert.InDelta(t, -20, c.levels[len(c.levels)-1], 0.5, "carrier dBFS")

			freq, snr := toneStats(c.pcm[2400:])
			assert.InDelta(t, tc.tone, freq, 2)
			assert.Greater(t, snr, tc.minSNR)
		})
	}
}

func TestAM_carrierSquelch(t *testing.T) {
	weak := amTone(rtlRate, 0.5, 1000, 0.5, 0.001, 0, 25000, 5) // -60 dBFS
	c := run(t, analog.Config{SampleRate: rtlRate, Mode: analog.ModeAM}, weak)
	assert.NotContains(t, c.opens, true)

	c = run(t, analog.Config{SampleRate: rtlRate, Mode: analog.ModeAM, CarrierSquelchDBFS: -70}, weak)
	assert.True(t, c.opens[len(c.opens)-1], "lower threshold opens")
}

func TestNew_validation(t *testing.T) {
	emit := func(analog.Block) {}

	_, err := analog.New(analog.Config{SampleRate: 16000, Mode: analog.ModeNFM}, emit)
	require.Error(t, err)

	_, err = analog.New(analog.Config{SampleRate: rtlRate, Mode: analog.ModeAM, ChannelSpacingHz: 12500}, emit)
	require.Error(t, err)

	_, err = analog.New(analog.Config{SampleRate: rtlRate}, emit)
	require.Error(t, err)

	_, err = analog.New(analog.Config{SampleRate: rtlRate, Mode: analog.ModeNFM}, nil)
	require.Error(t, err)
}

func TestDemodulator_zeroAllocs(t *testing.T) {
	iqs := fmTone(rtlRate, 0.2, 1000, 2500, 20, 12500, 6)
	blocks := 0
	d, err := analog.New(analog.Config{SampleRate: rtlRate, Mode: analog.ModeNFM}, func(analog.Block) { blocks++ })
	require.NoError(t, err)
	d.Write(iqs)

	allocs := testing.AllocsPerRun(10, func() { d.Write(iqs[:16384]) })
	assert.Zero(t, allocs)
	assert.Positive(t, blocks)
}

func BenchmarkNFM_Write16k(b *testing.B) {
	iqs := fmTone(rtlRate, 0.1, 1000, 2500, 20, 12500, 7)
	d, err := analog.New(analog.Config{SampleRate: rtlRate, Mode: analog.ModeNFM}, func(analog.Block) {})
	require.NoError(b, err)

	b.ReportAllocs()

	for b.Loop() {
		d.Write(iqs[:16384])
	}
}
