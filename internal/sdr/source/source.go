// Package source streams complex IQ from SDR receivers. A Source runs an
// external capture tool (rtl_sdr or airspy_rx) and converts its stdout into
// complex64 blocks; Retune restarts the tool on a new frequency. A
// FileSource replays a recorded or synthesized capture for CI and bench
// work. Receivers are always claimed by USB serial, never by index.
package source

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"sync"

	"github.com/openmanet/openmanetd/internal/sdr/iq"
)

// Driver selects the capture tool.
type Driver int

const (
	// DriverRTLSDR runs rtl_sdr (cu8 output).
	DriverRTLSDR Driver = iota + 1
	// DriverAirspy runs airspy_rx with -t 2 (cs16 output).
	DriverAirspy
)

// readSize is the pipe read size: 64 KiB is ~32 ms of cu8 at 1.024 MS/s,
// large enough to keep syscall overhead negligible on small CPUs.
const readSize = 64 * 1024

// ErrProcessExited is returned by Run when the capture tool exits on its
// own (device unplugged, USB error, bad arguments).
var ErrProcessExited = errors.New("source: capture process exited")

// Consumer receives converted samples. The slice is reused and is only
// valid for the duration of the call.
type Consumer func(samples []complex64)

// Config describes one receiver.
type Config struct {
	// Binary overrides the tool path (default "rtl_sdr" / "airspy_rx").
	Binary string
	// Serial is the USB serial used to claim the device. For airspy_rx it
	// is the 64-bit serial as printed by airspy_info (e.g.
	// "0x35AC63DC2D8C7A4F").
	Serial      string
	FrequencyHz uint64
	SampleRate  int
	// GainDB is the rtl_sdr tuner gain in dB, or the airspy_rx
	// linearity gain index (0-21). Zero leaves the tool's automatic gain.
	GainDB float64
	Driver Driver
	// PPM is the rtl_sdr crystal correction (-p). The P25 decoder
	// tolerates about +/-1.5 kHz of carrier error, i.e. under 2 ppm at
	// 800 MHz, so uncorrected non-TCXO dongles need this. Ignored for
	// airspy_rx (TCXO, no correction flag).
	PPM int
}

// Format returns the sample format the configured tool writes.
func (c Config) Format() iq.Format {
	if c.Driver == DriverAirspy {
		return iq.FormatCS16
	}

	return iq.FormatCU8
}

// Validate checks the configuration without starting anything.
func (c Config) Validate() error {
	if c.Driver != DriverRTLSDR && c.Driver != DriverAirspy {
		return fmt.Errorf("source: unknown driver %d", int(c.Driver))
	}

	if c.Serial == "" {
		return errors.New("source: serial is required (devices are claimed by serial, never by index)")
	}

	if strings.HasPrefix(c.Serial, "-") || strings.ContainsAny(c.Serial, " \t\n") {
		return fmt.Errorf("source: invalid serial %q", c.Serial)
	}

	if c.Driver == DriverRTLSDR {
		// rtl_sdr -d first tries the argument as a device index
		// (strtol with base 0), so a serial such as "00000001" or "1"
		// would silently open device #1. Large numeric serials like
		// "1090" are safe because they exceed the device count.
		if v, err := strconv.ParseInt(c.Serial, 0, 64); err == nil && v < 256 {
			return fmt.Errorf("source: rtl-sdr serial %q would be read as a device index; reprogram it with rtl_eeprom -s", c.Serial)
		}
	}

	if c.Driver == DriverAirspy {
		// airspy_rx parses -s with strtoull (decimal, or hex after 0x)
		// and treats -a values <= 1000000 as an index into the device's
		// rate table rather than a rate in Hz.
		if _, err := strconv.ParseUint(strings.TrimPrefix(strings.ToLower(c.Serial), "0x"), serialBase(c.Serial), 64); err != nil {
			return fmt.Errorf("source: airspy serial %q must be decimal or 0x-prefixed hex", c.Serial)
		}

		if c.SampleRate <= 1_000_000 {
			return fmt.Errorf("source: airspy sample rate %d must be above 1 MS/s (lower values are rate indexes)", c.SampleRate)
		}
	}

	if c.FrequencyHz == 0 || c.SampleRate <= 0 {
		return errors.New("source: frequency and sample rate are required")
	}

	return nil
}

func serialBase(s string) int {
	if len(s) > 2 && (strings.HasPrefix(s, "0x") || strings.HasPrefix(s, "0X")) {
		return 16
	}

	return 10
}

// Command returns the tool and arguments that capture at freqHz to stdout.
func (c Config) Command(freqHz uint64) (name string, args []string) {
	rate := strconv.Itoa(c.SampleRate)

	switch c.Driver {
	case DriverAirspy:
		name = "airspy_rx"
		mhz := strconv.FormatFloat(float64(freqHz)/1e6, 'f', -1, 64)
		args = []string{"-s", c.Serial, "-f", mhz, "-a", rate, "-t", "2"}

		if c.GainDB > 0 {
			args = append(args, "-g", strconv.Itoa(int(c.GainDB)))
		}

		args = append(args, "-r", "-")
	default:
		name = "rtl_sdr"
		args = []string{"-d", c.Serial, "-f", strconv.FormatUint(freqHz, 10), "-s", rate}

		if c.GainDB != 0 {
			args = append(args, "-g", strconv.FormatFloat(c.GainDB, 'f', -1, 64))
		}

		if c.PPM != 0 {
			args = append(args, "-p", strconv.Itoa(c.PPM))
		}

		args = append(args, "-")
	}

	if c.Binary != "" {
		name = c.Binary
	}

	return name, args
}

// Process is a started capture tool.
type Process interface {
	// Stdout is the tool's sample stream.
	Stdout() io.Reader
	// Wait blocks until the process has exited and released its pipes.
	Wait() error
}

// Runner starts capture processes. The process must terminate when ctx is
// canceled.
type Runner interface {
	Start(ctx context.Context, name string, args []string) (Process, error)
}

// Source streams IQ from an external capture tool. Run is called once;
// Retune and Frequency are safe to call concurrently with it.
type Source struct {
	runner Runner
	retune chan struct{} // capacity 1: coalesces bursts of retunes
	cfg    Config

	mu   sync.Mutex // protects freq
	freq uint64
}

// New returns a Source for cfg using runner (nil selects ExecRunner).
func New(cfg Config, runner Runner) (*Source, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}

	if runner == nil {
		runner = &ExecRunner{}
	}

	return &Source{runner: runner, retune: make(chan struct{}, 1), cfg: cfg, freq: cfg.FrequencyHz}, nil
}

// Frequency returns the current (or pending) center frequency.
func (s *Source) Frequency() uint64 {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.freq
}

// SampleRate returns the configured sample rate.
func (s *Source) SampleRate() int { return s.cfg.SampleRate }

// Retune moves the receiver to freqHz by restarting the capture tool. It
// does not block; the restart happens inside Run.
func (s *Source) Retune(freqHz uint64) {
	s.mu.Lock()
	s.freq = freqHz
	s.mu.Unlock()

	select {
	case s.retune <- struct{}{}:
	default: // a restart is already pending and will pick up freqHz
	}
}

// Run starts the capture tool and streams converted samples into consume
// until ctx is canceled (returns nil) or the tool exits on its own
// (returns an error wrapping ErrProcessExited). consume runs on the
// calling goroutine. Run waits for the process and its helper goroutine
// before returning.
func (s *Source) Run(ctx context.Context, consume Consumer) error {
	conv, err := iq.NewConverter(s.cfg.Format())
	if err != nil {
		return err
	}

	buf := make([]byte, readSize)
	samples := make([]complex64, 0, readSize/s.cfg.Format().BytesPerSample()+1)

	for {
		retuned, err := s.runOnce(ctx, conv, buf, &samples, consume)

		switch {
		case ctx.Err() != nil:
			return nil
		case retuned:
			conv.Reset()

			continue
		default:
			return err
		}
	}
}

func (s *Source) runOnce(ctx context.Context, conv *iq.Converter, buf []byte, samples *[]complex64,
	consume Consumer,
) (retuned bool, err error) {
	freq := s.Frequency()
	name, args := s.cfg.Command(freq)

	pctx, cancel := context.WithCancel(ctx)
	defer cancel()

	proc, err := s.runner.Start(pctx, name, args)
	if err != nil {
		return false, fmt.Errorf("source: start %s: %w", name, err)
	}

	done := make(chan struct{})

	var (
		wg      sync.WaitGroup
		mu      sync.Mutex // protects restart
		restart bool
	)

	wg.Go(func() {
		for {
			select {
			case <-s.retune:
				if s.Frequency() == freq {
					continue // stale signal, already on this frequency
				}

				mu.Lock()
				restart = true
				mu.Unlock()
				cancel()

				return
			case <-done:
				return
			case <-pctx.Done():
				return
			}
		}
	})

	readErr := pump(proc.Stdout(), conv, buf, samples, consume)

	close(done)
	cancel()

	waitErr := proc.Wait()

	wg.Wait()

	mu.Lock()
	defer mu.Unlock()

	if restart {
		return true, nil
	}

	return false, fmt.Errorf("%w: %s: %w", ErrProcessExited, name, errors.Join(readErr, waitErr))
}

func pump(r io.Reader, conv *iq.Converter, buf []byte, samples *[]complex64, consume Consumer) error {
	for {
		n, err := r.Read(buf)
		if n > 0 {
			*samples = conv.Convert(*samples, buf[:n])
			if len(*samples) > 0 {
				consume(*samples)
			}
		}

		if errors.Is(err, io.EOF) {
			return nil
		}

		if err != nil {
			return fmt.Errorf("source: read: %w", err)
		}
	}
}
