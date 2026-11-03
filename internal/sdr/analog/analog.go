// Package analog demodulates narrowband analog voice channels from complex
// baseband IQ: narrowband FM (land mobile, 12.5/25 kHz) with a CTCSS-free
// noise squelch, and AM envelope detection for aviation (8.33/25 kHz
// channels) with a carrier-level squelch. Output is 8 kHz signed 16-bit
// PCM delivered in fixed-size blocks with the squelch state.
package analog

import (
	"errors"
	"fmt"
	"math"
	"time"

	"github.com/openmanet/openmanetd/internal/sdr/iq"
)

// Mode selects the demodulator.
type Mode int

const (
	// ModeNFM is narrowband FM with noise squelch.
	ModeNFM Mode = iota + 1
	// ModeAM is AM envelope detection with carrier squelch.
	ModeAM
)

const (
	// AudioRate is the PCM output rate.
	AudioRate = 8000
	// DefaultBlockSamples is 20 ms of audio.
	DefaultBlockSamples = 160
	// NFMDeemphasis is the land mobile de-emphasis time constant.
	NFMDeemphasis = 750 * time.Microsecond

	// DefaultNoiseSquelch is the NFM squelch threshold: the RMS of the
	// discriminator output above the voice band, relative to the peak
	// deviation. Pure noise measures well above 1; a 12 dB CNR signal
	// around 0.3.
	DefaultNoiseSquelch = 0.5
	// DefaultCarrierSquelchDBFS is the AM squelch threshold on the
	// in-channel carrier level.
	DefaultCarrierSquelchDBFS = -45.0

	// demodRate is the minimum intermediate rate; the decimation factor is
	// floor(SampleRate/demodRate).
	demodRate = 32000
	// processBlock bounds scratch memory per Write chunk.
	processBlock = 4096
	// squelchTau is the time constant of the squelch level detectors.
	squelchTau = 0.012
	// carrierTau is the AM carrier (AGC) averaging time constant.
	carrierTau = 0.15
	// hysteresis separates the open and close thresholds.
	hysteresis = 1.25
	// outputGain maps full deviation / 100% modulation to this fraction
	// of int16 full scale.
	outputGain = 0.7 * math.MaxInt16
	// voiceHighPassHz removes CTCSS/DCS tones and DC from the audio.
	voiceHighPassHz = 250
)

// Config parameterizes a Demodulator.
type Config struct {
	// SampleRate of the input IQ (>= 32 kS/s; any rate, not necessarily
	// a multiple of 8 kHz).
	SampleRate int
	Mode       Mode
	// ChannelSpacingHz selects the channel filter: NFM 12500 (default) or
	// 25000; AM 8330 or 25000 (default).
	ChannelSpacingHz int
	// Deemphasis time constant for NFM (0 disables; NFMDeemphasis is the
	// usual land mobile value). Ignored for AM.
	Deemphasis time.Duration
	// NoiseSquelch is the NFM squelch threshold (0 selects the default;
	// negative keeps the squelch always open).
	NoiseSquelch float64
	// CarrierSquelchDBFS is the AM squelch threshold (0 selects the
	// default; use a very low value such as -200 to keep it open).
	CarrierSquelchDBFS float64
	// BlockSamples is the PCM block length (0 selects 160 = 20 ms).
	BlockSamples int
}

// Block is one chunk of 8 kHz PCM. PCM is reused by the demodulator and is
// only valid during the emit callback; it is silence while the squelch is
// closed. Level is the squelch metric at the end of the block (NFM: noise
// RMS relative to deviation; AM: carrier level in dBFS).
type Block struct {
	PCM   []int16
	Level float64
	Open  bool
}

// Demodulator is a streaming analog voice demodulator. Not safe for
// concurrent use.
type Demodulator struct {
	emit      func(Block)
	chain     *iq.Chain
	audioLP   *iq.RealFIR
	noiseHP   *iq.RealFIR
	resampler *iq.Resampler
	decBuf    []complex64
	resBuf    []float32
	pcm       []int16
	voiceHP   biquad
	cfg       Config
	prev      complex64
	fmScale   float64 // radians per sample -> fraction of peak deviation
	sqAlpha   float64
	carAlpha  float64
	deAlpha   float64
	deGain    float64 // restores unity gain at 1 kHz after de-emphasis
	deState   float64
	level     float64 // squelch detector state (NFM: noise power; AM: carrier)
	threshold float64
	open      bool
}

// New returns a Demodulator delivering PCM blocks to emit.
func New(cfg Config, emit func(Block)) (*Demodulator, error) {
	if emit == nil {
		return nil, errors.New("analog: emit callback is required")
	}

	if cfg.SampleRate < demodRate {
		return nil, fmt.Errorf("analog: sample rate %d below %d", cfg.SampleRate, demodRate)
	}

	pass, stop, peakDev, err := channelEdges(&cfg)
	if err != nil {
		return nil, err
	}

	if cfg.BlockSamples <= 0 {
		cfg.BlockSamples = DefaultBlockSamples
	}

	factor := cfg.SampleRate / demodRate

	chain, err := iq.NewChain(float64(cfg.SampleRate), factor, pass, stop, 60, processBlock)
	if err != nil {
		return nil, fmt.Errorf("analog: channel filter: %w", err)
	}

	rate := chain.OutRate()

	audioTaps, err := iq.KaiserLowPass(rate, 3000, 3800, 50)
	if err != nil {
		return nil, fmt.Errorf("analog: audio filter: %w", err)
	}

	res, err := iq.NewResampler(rate, AudioRate)
	if err != nil {
		return nil, fmt.Errorf("analog: resampler: %w", err)
	}

	d := &Demodulator{
		emit:      emit,
		chain:     chain,
		audioLP:   iq.NewRealFIR(audioTaps),
		resampler: res,
		decBuf:    make([]complex64, 0, processBlock/factor+1),
		resBuf:    make([]float32, 0, 8),
		pcm:       make([]int16, 0, cfg.BlockSamples),
		voiceHP:   newHighPass(voiceHighPassHz, rate),
		cfg:       cfg,
		fmScale:   rate / (2 * math.Pi) / peakDev,
		sqAlpha:   1 - math.Exp(-1/(squelchTau*rate)),
		carAlpha:  1 - math.Exp(-1/(carrierTau*rate)),
	}

	if cfg.Deemphasis > 0 && cfg.Mode == ModeNFM {
		tau := cfg.Deemphasis.Seconds()
		d.deAlpha = 1 - math.Exp(-1/(tau*rate))
		d.deGain = math.Hypot(1, 2*math.Pi*1000*tau)
	}

	if cfg.Mode == ModeNFM {
		if err := d.initNoiseSquelch(rate); err != nil {
			return nil, err
		}

		return d, nil
	}

	d.threshold = cfg.CarrierSquelchDBFS
	if d.threshold == 0 {
		d.threshold = DefaultCarrierSquelchDBFS
	}

	return d, nil
}

func (d *Demodulator) initNoiseSquelch(rate float64) error {
	// Noise band: above the voice band, below the channel filter edge
	// (beyond it there is no noise left to measure).
	lp, err := iq.KaiserLowPass(rate, 4000, 5500, 40)
	if err != nil {
		return fmt.Errorf("analog: squelch filter: %w", err)
	}

	d.noiseHP = iq.NewRealFIR(iq.HighPassFromLowPass(lp))
	d.level = 4 // start closed: noise power well above any threshold

	d.threshold = d.cfg.NoiseSquelch
	if d.threshold == 0 {
		d.threshold = DefaultNoiseSquelch
	}

	return nil
}

func channelEdges(cfg *Config) (pass, stop, peakDev float64, err error) {
	switch cfg.Mode {
	case ModeNFM:
		switch cfg.ChannelSpacingHz {
		case 0, 12500:
			cfg.ChannelSpacingHz = 12500

			return 5500, 7500, 2500, nil
		case 25000:
			return 8000, 11000, 5000, nil
		}
	case ModeAM:
		switch cfg.ChannelSpacingHz {
		case 8330:
			return 3200, 4500, 1, nil
		case 0, 25000:
			cfg.ChannelSpacingHz = 25000

			return 6000, 9000, 1, nil
		}
	default:
		return 0, 0, 0, fmt.Errorf("analog: unknown mode %d", int(cfg.Mode))
	}

	return 0, 0, 0, fmt.Errorf("analog: unsupported channel spacing %d Hz", cfg.ChannelSpacingHz)
}

// Open reports the current squelch state.
func (d *Demodulator) Open() bool { return d.open }

// Write consumes complex baseband samples (channel centered at 0 Hz). PCM
// blocks are emitted synchronously. It does not allocate in steady state.
func (d *Demodulator) Write(samples []complex64) {
	for len(samples) > 0 {
		n := min(len(samples), processBlock)
		d.decBuf = d.chain.Process(d.decBuf, samples[:n])
		samples = samples[n:]

		for _, s := range d.decBuf {
			var a float64

			if d.cfg.Mode == ModeNFM {
				a = d.nfmSample(s)
			} else {
				a = d.amSample(s)
			}

			a = float64(d.audioLP.Step(float32(a)))
			a = d.voiceHP.step(a)

			d.resBuf = d.resampler.Push(d.resBuf[:0], float32(a))
			for _, v := range d.resBuf {
				d.pushPCM(v)
			}
		}
	}
}

func (d *Demodulator) nfmSample(s complex64) float64 {
	p := s * complex(real(d.prev), -imag(d.prev))
	d.prev = s
	f := math.Atan2(float64(imag(p)), float64(real(p))) * d.fmScale

	hp := float64(d.noiseHP.Step(float32(f)))
	d.level += (hp*hp - d.level) * d.sqAlpha

	rms := math.Sqrt(d.level)

	switch {
	case d.threshold < 0:
		d.open = true
	case d.open && rms > d.threshold*hysteresis:
		d.open = false
	case !d.open && rms < d.threshold:
		d.open = true
	}

	if d.deAlpha > 0 {
		d.deState += (f - d.deState) * d.deAlpha
		f = d.deState * d.deGain
	}

	return f
}

func (d *Demodulator) amSample(s complex64) float64 {
	env := math.Hypot(float64(real(s)), float64(imag(s)))
	d.level += (env - d.level) * d.carAlpha

	dbfs := 20 * math.Log10(d.level+1e-12)

	switch {
	case d.open && dbfs < d.threshold-3:
		d.open = false
	case !d.open && dbfs > d.threshold:
		d.open = true
	}

	if d.level <= 0 {
		return 0
	}

	// Modulation relative to the carrier: AGC'd, carrier removed.
	return env/d.level - 1
}

func (d *Demodulator) pushPCM(v float32) {
	x := 0.0
	if d.open {
		x = math.Round(float64(v) * outputGain)
	}

	d.pcm = append(d.pcm, int16(min(max(x, math.MinInt16), math.MaxInt16)))
	if len(d.pcm) < d.cfg.BlockSamples {
		return
	}

	level := 20 * math.Log10(d.level+1e-12)
	if d.cfg.Mode == ModeNFM {
		level = math.Sqrt(d.level)
	}

	d.emit(Block{PCM: d.pcm, Open: d.open, Level: level})
	d.pcm = d.pcm[:0]
}

// biquad is a direct-form-I second-order IIR section.
type biquad struct {
	b0, b1, b2, a1, a2 float64
	x1, x2, y1, y2     float64
}

// newHighPass returns a Butterworth (Q = 1/sqrt 2) high-pass section.
func newHighPass(f0, rate float64) biquad {
	w0 := 2 * math.Pi * f0 / rate
	cw, alpha := math.Cos(w0), math.Sin(w0)/math.Sqrt2
	a0 := 1 + alpha

	return biquad{
		b0: (1 + cw) / 2 / a0,
		b1: -(1 + cw) / a0,
		b2: (1 + cw) / 2 / a0,
		a1: -2 * cw / a0,
		a2: (1 - alpha) / a0,
	}
}

func (q *biquad) step(x float64) float64 {
	y := q.b0*x + q.b1*q.x1 + q.b2*q.x2 - q.a1*q.y1 - q.a2*q.y2
	q.x2, q.x1 = q.x1, x
	q.y2, q.y1 = q.y1, y

	return y
}
