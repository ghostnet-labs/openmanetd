package p25_test

import (
	"io"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/openmanet/openmanetd/internal/sdr/iq"
	"github.com/openmanet/openmanetd/internal/sdr/p25"
	"github.com/openmanet/openmanetd/internal/sdr/p25/p25test"
)

// updateFixturesEnv regenerates the committed IQ fixtures when set to 1:
//
//	OPENMANET_UPDATE_FIXTURES=1 go test ./internal/sdr/p25 -run TestGenerateFixtures
const updateFixturesEnv = "OPENMANET_UPDATE_FIXTURES"

const fixtureRate = 1_024_000

type fixture struct {
	name   string
	mod    p25test.Modulator
	frames [][]uint8
	tsbks  int
	grants []p25.GroupVoiceGrant
	check  func(t *testing.T, r *recorder)
}

func fixtures() []fixture {
	f := siteFrames()
	all := expectedGrants()

	unknown := func(g p25.GroupVoiceGrant) p25.GroupVoiceGrant {
		g.FrequencyHz = 0 // identifier not announced in this capture

		return g
	}

	return []fixture{
		{
			name: "c4fm_1024k_site.cu8",
			mod: p25test.Modulator{
				SampleRate: fixtureRate, OffsetHz: 100_000, FreqErrorHz: 400, Amplitude: 0.25, CNRdB: 22,
				TimingOffset: 0.37, Seed: 1,
			},
			frames: f[0:2],
			tsbks:  4,
			grants: all[0:1],
			check: func(t *testing.T, r *recorder) {
				t.Helper()
				require.Len(t, r.sites, 2)
				assert.Equal(t, uint32(testWACN), r.sites[1].WACN)
				assert.Equal(t, uint8(7), r.sites[1].Site)
				assert.Equal(t, uint64(base800+0x123*6250), r.sites[1].ControlChannelHz)
			},
		},
		{
			name: "c4fm_1024k_inverted_encrypted.cu8",
			mod: p25test.Modulator{
				SampleRate: fixtureRate, OffsetHz: -150_000, FreqErrorHz: -700, Amplitude: 0.25, CNRdB: 16,
				ClockPPM: 40, Invert: true, Seed: 2,
			},
			frames: [][]uint8{f[2], p25test.TSDU(testNAC, p25test.AdjacentStatus(1, 0x3, testSys, 1, 8, p25test.Channel(iden800, 0x200), 0x70))},
			tsbks:  4,
			grants: []p25.GroupVoiceGrant{all[1], unknown(all[2]), unknown(all[3])},
			check: func(t *testing.T, r *recorder) {
				t.Helper()
				require.NotEmpty(t, r.syncs)
				assert.True(t, r.syncs[0].Inverted)
				require.Len(t, r.adjacent, 1)
				assert.Equal(t, uint8(8), r.adjacent[0].Site)
				assert.Zero(t, r.adjacent[0].ControlChannelHz)
			},
		},
		{
			name: "c4fm_1024k_tdma_lowsnr.cu8",
			mod: p25test.Modulator{
				SampleRate: fixtureRate, OffsetHz: 250_000, FreqErrorHz: 1000, Amplitude: 0.25, CNRdB: 14,
				TimingOffset: 0.6, Seed: 3,
			},
			frames: f[3:5],
			tsbks:  4,
			grants: []p25.GroupVoiceGrant{unknown(all[4]), all[5]},
			check: func(t *testing.T, r *recorder) {
				t.Helper()
				require.Len(t, r.idens, 1)
				assert.True(t, r.idens[0].TDMA)
			},
		},
	}
}

// fixtureDir locates testfixtures/sdr/p25 relative to the module root.
func fixtureDir(t *testing.T) string {
	t.Helper()

	_, file, _, ok := runtime.Caller(0)
	require.True(t, ok)

	dir := filepath.Dir(file)
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return filepath.Join(dir, "testfixtures", "sdr", "p25")
		}

		parent := filepath.Dir(dir)
		require.NotEqual(t, dir, parent, "module root not found")
		dir = parent
	}
}

func TestGenerateFixtures(t *testing.T) {
	if os.Getenv(updateFixturesEnv) != "1" {
		t.Skip("set " + updateFixturesEnv + "=1 to regenerate fixtures")
	}

	dir := fixtureDir(t)
	require.NoError(t, os.MkdirAll(dir, 0o755))

	for _, fx := range fixtures() {
		raw := iq.AppendCU8(nil, signal(fx.mod, fx.frames, 96))
		require.Less(t, len(raw), 300*1024, fx.name)
		require.NoError(t, os.WriteFile(filepath.Join(dir, fx.name), raw, 0o644))
	}
}

func TestFixtures_decode(t *testing.T) {
	dir := fixtureDir(t)

	for _, fx := range fixtures() {
		t.Run(fx.name, func(t *testing.T) {
			f, err := os.Open(filepath.Join(dir, fx.name))
			require.NoError(t, err)
			t.Cleanup(func() { _ = f.Close() })

			conv, err := iq.NewConverter(iq.FormatCU8)
			require.NoError(t, err)

			r := &recorder{}
			d := p25.NewDecoder(fixtureRate, r.emit)
			d.SetFrequencyOffset(fx.mod.OffsetHz)

			buf := make([]byte, 32768+1) // odd size exercises the converter carry

			var samples []complex64

			for {
				n, err := f.Read(buf)
				samples = conv.Convert(samples, buf[:n])
				d.Write(samples)

				if err == io.EOF {
					break
				}

				require.NoError(t, err)
			}

			assert.Equal(t, uint64(fx.tsbks), d.Stats().TSBKs, "%+v", d.Stats())
			assert.Equal(t, fx.grants, r.grants)
			fx.check(t, r)
		})
	}
}
