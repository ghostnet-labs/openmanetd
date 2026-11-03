package source_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/openmanet/openmanetd/internal/sdr/iq"
	"github.com/openmanet/openmanetd/internal/sdr/p25"
	"github.com/openmanet/openmanetd/internal/sdr/source"
)

func rtlConfig() source.Config {
	return source.Config{Driver: source.DriverRTLSDR, Serial: "1090", FrequencyHz: 851_012_500, SampleRate: 1_024_000}
}

func TestConfig_Command(t *testing.T) {
	cases := []struct {
		name     string
		cfg      source.Config
		freq     uint64
		wantName string
		wantArgs []string
		format   iq.Format
	}{
		{
			"rtl_auto_gain", rtlConfig(), 851_012_500, "rtl_sdr",
			[]string{"-d", "1090", "-f", "851012500", "-s", "1024000", "-"}, iq.FormatCU8,
		},
		{
			"rtl_gain_ppm_binary", source.Config{
				Driver: source.DriverRTLSDR, Serial: "978", SampleRate: 2_400_000, GainDB: 38.6, PPM: -3, Binary: "/usr/bin/rtl_sdr",
			},
			978_000_000, "/usr/bin/rtl_sdr",
			[]string{"-d", "978", "-f", "978000000", "-s", "2400000", "-g", "38.6", "-p", "-3", "-"}, iq.FormatCU8,
		},
		{
			"airspy", source.Config{Driver: source.DriverAirspy, Serial: "0x35AC63DC2D8C7A4F", SampleRate: 2_500_000, GainDB: 16},
			851_012_500, "airspy_rx",
			[]string{"-s", "0x35AC63DC2D8C7A4F", "-f", "851.0125", "-a", "2500000", "-t", "2", "-g", "16", "-r", "-"}, iq.FormatCS16,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			name, args := tc.cfg.Command(tc.freq)
			assert.Equal(t, tc.wantName, name)
			assert.Equal(t, tc.wantArgs, args)
			assert.Equal(t, tc.format, tc.cfg.Format())
		})
	}
}

func TestConfig_Validate(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*source.Config)
		ok     bool
	}{
		{"valid", func(*source.Config) {}, true},
		{"empty_serial", func(c *source.Config) { c.Serial = "" }, false},
		{"default_rtl_serial_is_index", func(c *source.Config) { c.Serial = "00000001" }, false},
		{"small_numeric_serial", func(c *source.Config) { c.Serial = "3" }, false},
		{"leading_zero_non_octal", func(c *source.Config) { c.Serial = "0978" }, true},
		{"alpha_serial", func(c *source.Config) { c.Serial = "scan1" }, true},
		{"flag_injection", func(c *source.Config) { c.Serial = "-f" }, false},
		{"no_driver", func(c *source.Config) { c.Driver = 0 }, false},
		{"no_rate", func(c *source.Config) { c.SampleRate = 0 }, false},
		{"airspy_small_numeric_ok", func(c *source.Config) { c.Driver = source.DriverAirspy; c.Serial = "1" }, true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := rtlConfig()
			tc.mutate(&cfg)

			err := cfg.Validate()
			if tc.ok {
				assert.NoError(t, err)
			} else {
				assert.Error(t, err)
			}
		})
	}
}

func TestSource_streamsAndReportsExit(t *testing.T) {
	r := &fakeRunner{chunk: func(int) []byte { return []byte{255, 0, 128, 127, 0} }} // odd tail byte
	s, err := source.New(rtlConfig(), r)
	require.NoError(t, err)

	var got []complex64

	err = s.Run(t.Context(), func(x []complex64) { got = append(got, x...) })
	require.ErrorIs(t, err, source.ErrProcessExited)

	require.Len(t, got, 2)
	assert.InDelta(t, 1, real(got[0]), 1e-6)
	assert.InDelta(t, -1, imag(got[0]), 1e-6)

	calls := r.calls()
	require.Len(t, calls, 1)
	assert.Equal(t, "rtl_sdr", calls[0].name)
	assert.True(t, r.processes()[0].wasWaited())
}

func TestSource_startError(t *testing.T) {
	r := &fakeRunner{startErr: errors.New("no such file"), chunk: func(int) []byte { return nil }}
	s, err := source.New(rtlConfig(), r)
	require.NoError(t, err)

	err = s.Run(t.Context(), func([]complex64) {})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no such file")
}

func TestSource_retuneRestartsProcess(t *testing.T) {
	// Process i streams bytes of value i*100 so samples identify it.
	r := &fakeRunner{loop: true, chunk: func(i int) []byte { return []byte{byte(i * 100), byte(i * 100)} }}
	s, err := source.New(rtlConfig(), r)
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	first := (float32(0) - 127.5) / 127.5
	second := (float32(100) - 127.5) / 127.5
	retuned := false

	err = s.Run(ctx, func(x []complex64) {
		switch real(x[0]) {
		case first:
			if !retuned {
				retuned = true

				s.Retune(852_000_000)
			}
		case second:
			cancel()
		}
	})
	require.NoError(t, err, "cancellation is a clean shutdown")

	calls := r.calls()
	require.Len(t, calls, 2)
	assert.Equal(t, "851012500", calls[0].args[slices.Index(calls[0].args, "-f")+1])
	assert.Equal(t, "852000000", calls[1].args[slices.Index(calls[1].args, "-f")+1])
	assert.Equal(t, uint64(852_000_000), s.Frequency())

	for _, p := range r.processes() {
		assert.True(t, p.wasWaited(), "every process is reaped")
	}
}

func TestSource_retuneToSameFrequencyIsIgnored(t *testing.T) {
	r := &fakeRunner{loop: true, chunk: func(int) []byte { return []byte{1, 2} }}
	s, err := source.New(rtlConfig(), r)
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	n := 0
	err = s.Run(ctx, func([]complex64) {
		n++
		if n == 1 {
			s.Retune(rtlConfig().FrequencyHz)
		}

		if n == 200 {
			cancel()
		}
	})
	require.NoError(t, err)
	assert.Len(t, r.calls(), 1)
}

func TestSource_cancelNoGoroutineLeak(t *testing.T) {
	baseline := runtime.NumGoroutine()

	for range 5 {
		r := &fakeRunner{loop: true, chunk: func(int) []byte { return make([]byte, 4096) }}
		s, err := source.New(rtlConfig(), r)
		require.NoError(t, err)

		ctx, cancel := context.WithCancel(t.Context())
		done := make(chan error, 1)

		go func() {
			done <- s.Run(ctx, func([]complex64) { cancel() })
		}()

		require.NoError(t, <-done)
		cancel()

		for _, p := range r.processes() {
			assert.True(t, p.wasWaited())
		}
	}

	// Poll on this goroutine (testify's Eventually runs its condition in
	// an extra goroutine, which would skew the count).
	tick := time.NewTicker(5 * time.Millisecond)
	defer tick.Stop()

	deadline := time.Now().Add(2 * time.Second)
	for runtime.NumGoroutine() > baseline && time.Now().Before(deadline) {
		<-tick.C
	}

	assert.LessOrEqual(t, runtime.NumGoroutine(), baseline, "goroutines leaked")
}

// TestExecRunner_cancelTerminatesProcess runs this test binary as a fake
// capture tool that streams forever, and checks cancellation reaps it.
func TestExecRunner_cancelTerminatesProcess(t *testing.T) {
	t.Setenv("OPENMANET_SDR_HELPER", "1")

	cfg := rtlConfig()
	cfg.Binary = os.Args[0]

	// The helper ignores the rtl_sdr arguments; -test.run selects it.
	r := &argPrefixRunner{inner: &source.ExecRunner{}, prefix: []string{"-test.run=^TestHelperProcess$", "--"}}
	s, err := source.New(cfg, r)
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	var got int

	err = s.Run(ctx, func(x []complex64) {
		got += len(x)
		if got >= 10_000 {
			cancel()
		}
	})
	require.NoError(t, err)
	assert.GreaterOrEqual(t, got, 10_000)
}

type argPrefixRunner struct {
	inner  source.Runner
	prefix []string
}

func (a *argPrefixRunner) Start(ctx context.Context, name string, args []string) (source.Process, error) {
	return a.inner.Start(ctx, name, append(append([]string(nil), a.prefix...), args...))
}

// TestHelperProcess is the fake capture tool used above; it is a no-op in
// a normal test run.
func TestHelperProcess(t *testing.T) {
	if os.Getenv("OPENMANET_SDR_HELPER") != "1" {
		t.Skip("helper process only")
	}

	buf := make([]byte, 8192)
	for {
		if _, err := os.Stdout.Write(buf); err != nil {
			os.Exit(0)
		}
	}
}

func fixturePath(t *testing.T, name string) string {
	t.Helper()

	_, file, _, ok := runtime.Caller(0)
	require.True(t, ok)

	dir := filepath.Dir(file)
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return filepath.Join(dir, "testfixtures", "sdr", "p25", name)
		}

		parent := filepath.Dir(dir)
		require.NotEqual(t, dir, parent)
		dir = parent
	}
}

// TestFileSource_replaysFixtureIntoP25 is the CI replay path: fixture file
// -> FileSource -> p25.Decoder.
func TestFileSource_replaysFixtureIntoP25(t *testing.T) {
	var grants []p25.GroupVoiceGrant

	d := p25.NewDecoder(1_024_000, func(e p25.Event) {
		if g, ok := e.(*p25.GroupVoiceGrant); ok {
			grants = append(grants, *g)
		}
	})
	d.SetFrequencyOffset(100_000)

	fs := &source.FileSource{Path: fixturePath(t, "c4fm_1024k_site.cu8"), Format: iq.FormatCU8, SampleRate: 1_024_000}
	require.NoError(t, fs.Run(t.Context(), d.Write))

	require.Len(t, grants, 1)
	assert.Equal(t, uint16(8001), grants[0].Talkgroup)
	assert.Equal(t, uint64(855_275_000), grants[0].FrequencyHz)
}

func TestFileSource_loopUntilCancel(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tiny.cu8")
	require.NoError(t, os.WriteFile(path, []byte{1, 2, 3, 4, 5}, 0o600))

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	calls := 0
	fs := &source.FileSource{Path: path, Format: iq.FormatCU8, Loop: true, BlockSamples: 4}
	err := fs.Run(ctx, func(x []complex64) {
		calls++

		assert.Len(t, x, 2, "odd trailing byte dropped at each rewind")

		if calls == 10 {
			cancel()
		}
	})
	require.NoError(t, err)
	assert.Equal(t, 10, calls)
}

func TestFileSource_missingFile(t *testing.T) {
	fs := &source.FileSource{Path: filepath.Join(t.TempDir(), "missing.cu8"), Format: iq.FormatCU8}
	require.Error(t, fs.Run(t.Context(), func([]complex64) {}))
}
