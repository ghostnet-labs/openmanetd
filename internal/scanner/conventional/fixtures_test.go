package conventional_test

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/openmanet/openmanetd/internal/scanner/conventional/conventionaltest"
)

// Fixture geometry (see testfixtures/sdr/conventional/README.md).
const (
	fixtureRate      = 256_000
	aviationCenterHz = 120_000_000
	aviationLiveHz   = 120_050_000 // AM tone
	aviationQuietHz  = 119_950_000
	nfmCenterHz      = 155_425_000
	nfmLiveHz        = 155_450_000 // NFM tone
	nfmQuietHz       = 155_400_000
)

type fixture struct {
	name     string
	carriers []conventionaltest.Carrier
	seed     uint64
}

func fixtures() []fixture {
	return []fixture{
		{name: "am_256k_aviation.cu8", seed: 11, carriers: []conventionaltest.Carrier{
			{OffsetHz: aviationLiveHz - aviationCenterHz, ToneHz: 1000, Amplitude: 0.3, Depth: 0.6},
		}},
		{name: "nfm_256k_vhf.cu8", seed: 12, carriers: []conventionaltest.Carrier{
			{OffsetHz: nfmLiveHz - nfmCenterHz, ToneHz: 1000, Amplitude: 0.3, DeviationHz: 2500},
		}},
	}
}

func fixturePath(t testing.TB, name string) string {
	t.Helper()

	_, file, _, ok := runtime.Caller(0)
	require.True(t, ok)

	dir := filepath.Dir(file)
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return filepath.Join(dir, "testfixtures", "sdr", "conventional", name)
		}

		parent := filepath.Dir(dir)
		require.NotEqual(t, parent, dir, "module root not found")
		dir = parent
	}
}

func loadFixture(t testing.TB, name string) []byte {
	t.Helper()

	b, err := os.ReadFile(fixturePath(t, name))
	require.NoError(t, err)

	return b
}

// TestGenerateFixtures rewrites the committed IQ fixtures when
// OPENMANET_UPDATE_FIXTURES=1; otherwise it checks they exist and have the
// expected length.
func TestGenerateFixtures(t *testing.T) {
	for _, f := range fixtures() {
		if os.Getenv("OPENMANET_UPDATE_FIXTURES") == "1" {
			iq := conventionaltest.Synthesize(fixtureRate, 0.25, f.carriers, -42, f.seed)
			//nolint:gosec // test fixture, world-readable like the rest of the tree
			require.NoError(t, os.WriteFile(fixturePath(t, f.name), conventionaltest.CU8(iq), 0o644))
		}

		require.Len(t, loadFixture(t, f.name), 2*fixtureRate/4, f.name)
	}
}
