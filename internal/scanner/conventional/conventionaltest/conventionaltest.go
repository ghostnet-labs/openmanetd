// Package conventionaltest synthesizes analog IQ captures and provides a
// capture Runner that replays them by tuned frequency, for tests that
// drive the conventional scanner end to end without SDR hardware.
package conventionaltest

import (
	"context"
	"errors"
	"io"
	"math"
	"math/rand/v2"
	"slices"
	"strconv"
	"sync"

	"github.com/openmanet/openmanetd/internal/sdr/source"
)

// ErrKilled is the read error a replay process returns once its context
// is canceled, like a killed capture tool's closed pipe.
var ErrKilled = errors.New("conventionaltest: process killed")

// Carrier is one signal in a synthesized capture.
type Carrier struct {
	// OffsetHz is the carrier offset from the capture center.
	OffsetHz float64
	// ToneHz is the modulating tone.
	ToneHz float64
	// Amplitude is the carrier amplitude (full scale 1).
	Amplitude float64
	// DeviationHz is the FM peak deviation; zero selects AM.
	DeviationHz float64
	// Depth is the AM modulation depth (0-1).
	Depth float64
}

// Synthesize returns rate*seconds complex samples holding the carriers
// plus complex Gaussian noise at noiseDBFS total power (seeded, so the
// output is reproducible on one architecture).
func Synthesize(rate, seconds float64, carriers []Carrier, noiseDBFS float64, seed uint64) []complex64 {
	n := int(rate * seconds)
	out := make([]complex64, n)
	rng := rand.New(rand.NewPCG(seed, 3)) //nolint:gosec // deterministic test noise, not security sensitive
	sigma := math.Pow(10, noiseDBFS/20) / math.Sqrt2

	phases := make([]float64, len(carriers))

	for i := range out {
		t := float64(i) / rate
		x := complex(rng.NormFloat64()*sigma, rng.NormFloat64()*sigma)

		for k, c := range carriers {
			tone := math.Sin(2 * math.Pi * c.ToneHz * t)

			if c.DeviationHz != 0 {
				phases[k] += 2 * math.Pi * (c.OffsetHz + c.DeviationHz*tone) / rate
				x += complex(c.Amplitude*math.Cos(phases[k]), c.Amplitude*math.Sin(phases[k]))

				continue
			}

			a := c.Amplitude * (1 + c.Depth*tone)
			p := 2 * math.Pi * c.OffsetHz * t
			x += complex(a*math.Cos(p), a*math.Sin(p))
		}

		out[i] = complex64(x)
	}

	return out
}

// CU8 converts samples to rtl_sdr's unsigned 8-bit interleaved format.
func CU8(samples []complex64) []byte {
	out := make([]byte, 0, 2*len(samples))

	q := func(v float32) byte {
		return byte(min(max(math.Round(float64(v)*127.5+127.5), 0), 255))
	}

	for _, s := range samples {
		out = append(out, q(real(s)), q(imag(s)))
	}

	return out
}

// Capture is replayed when the tuner is set inside [LowHz, HighHz].
type Capture struct {
	Data   []byte
	LowHz  uint64
	HighHz uint64
}

// Start is one recorded Runner.Start call.
type Start struct {
	Name     string
	Args     []string
	CenterHz uint64
}

// Runner is a source.Runner for rtl_sdr argument lists: each process
// replays the capture whose range holds the -f frequency, looping until
// its context is canceled. A tuning with no capture exits at once. It
// records every start.
type Runner struct { //nolint:govet // fieldalignment: mu stays above the field it guards per .claude/rules/concurrency.md
	StartErr error
	Captures []Capture

	mu     sync.Mutex // protects starts
	starts []Start
}

var _ source.Runner = (*Runner)(nil)

// Start implements source.Runner.
func (r *Runner) Start(ctx context.Context, name string, args []string) (source.Process, error) {
	center := uint64(0)

	for i := 0; i+1 < len(args); i++ {
		if args[i] == "-f" {
			center, _ = strconv.ParseUint(args[i+1], 10, 64)
		}
	}

	r.mu.Lock()
	r.starts = append(r.starts, Start{Name: name, Args: slices.Clone(args), CenterHz: center})
	r.mu.Unlock()

	if r.StartErr != nil {
		return nil, r.StartErr
	}

	p := &process{ctx: ctx}

	for _, c := range r.Captures {
		if center >= c.LowHz && center <= c.HighHz {
			p.data = c.Data
		}
	}

	return p, nil
}

// Starts returns the recorded starts.
func (r *Runner) Starts() []Start {
	r.mu.Lock()
	defer r.mu.Unlock()

	return slices.Clone(r.starts)
}

// process replays data; it is read only by source.Run's goroutine.
type process struct {
	ctx  context.Context //nolint:containedctx // the process lives exactly as long as its start context, like exec.CommandContext
	data []byte
	pos  int
}

func (p *process) Stdout() io.Reader { return p }

func (p *process) Wait() error { return nil }

// Read implements io.Reader over the looping data.
func (p *process) Read(b []byte) (int, error) {
	if p.ctx.Err() != nil {
		return 0, ErrKilled
	}

	if len(p.data) == 0 {
		return 0, io.EOF
	}

	n := copy(b, p.data[p.pos:])
	p.pos = (p.pos + n) % len(p.data)

	return n, nil
}
