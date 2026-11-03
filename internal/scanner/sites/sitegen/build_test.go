package main

import (
	"bytes"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/openmanet/openmanetd/internal/scanner/sites"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func fixturePath(t *testing.T, name string) string {
	t.Helper()

	_, file, _, ok := runtime.Caller(0)
	require.True(t, ok)

	dir := filepath.Dir(file)
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return filepath.Join(dir, "testfixtures", "scanner", "sites", name)
		}

		parent := filepath.Dir(dir)
		require.NotEqual(t, parent, dir, "module root not found")
		dir = parent
	}
}

func open(t *testing.T, name string) *os.File {
	t.Helper()

	f, err := os.Open(fixturePath(t, name))
	require.NoError(t, err)
	t.Cleanup(func() { _ = f.Close() })

	return f
}

// TestBuild_matchesEmbedded fails when the CSVs and the embedded
// sites.dat.gz drift apart: run go generate ./internal/scanner/sites.
func TestBuild_matchesEmbedded(t *testing.T) {
	var raw bytes.Buffer
	require.NoError(t, build(open(t, "site-tuner-fit.csv"), open(t, "scanner-frequencies.csv"), &raw))

	built, err := sites.Parse(&raw)
	require.NoError(t, err)

	embedded, err := sites.Table()
	require.NoError(t, err)

	assert.Equal(t, embedded, built, "embedded table is stale; run go generate ./internal/scanner/sites")
}

func TestRun_writesGzip(t *testing.T) {
	out := filepath.Join(t.TempDir(), "sites.dat.gz")
	require.NoError(t, run(fixturePath(t, "site-tuner-fit.csv"), fixturePath(t, "scanner-frequencies.csv"), out))

	b, err := os.ReadFile(out)
	require.NoError(t, err)
	assert.Equal(t, []byte{0x1f, 0x8b}, b[:2])
	assert.Less(t, len(b), 64*1024, "compact encoding")

	require.Error(t, run("missing.csv", fixturePath(t, "scanner-frequencies.csv"), out))
}

func TestMHzToHz(t *testing.T) {
	tests := []struct {
		in      string
		want    uint64
		wantErr bool
	}{
		{in: "773.06875", want: 773_068_750},
		{in: "851.0125", want: 851_012_500},
		{in: "155.7", want: 155_700_000},
		{in: "770", want: 770_000_000},
		{in: " 769.40625 ", want: 769_406_250},
		{in: "1.0000001", wantErr: true},
		{in: "abc", wantErr: true},
		{in: "770.x", wantErr: true},
	}

	for _, tc := range tests {
		t.Run(tc.in, func(t *testing.T) {
			got, err := mhzToHz(tc.in)
			if tc.wantErr {
				require.Error(t, err)

				return
			}

			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
}

func TestParsers(t *testing.T) {
	for in, want := range map[string]sites.Phase{
		"P25 Phase I": sites.Phase1, "P25 Phase II": sites.Phase2,
		"P25 Phase I (RR); Phase II TDMA on some talkgroups": sites.Phase1,
		"P25 Phase II (TDMA voice, FDMA CC)":                 sites.Phase2,
		"P25 Phase II (RR db; wiki says Phase I)":            sites.Phase2,
	} {
		got, err := parsePhase(in)
		require.NoError(t, err, in)
		assert.Equal(t, want, got, in)
	}

	_, err := parsePhase("DMR")
	require.Error(t, err)

	_, err = parseBand("UHF")
	require.Error(t, err)

	_, err = parseEncryption("some")
	require.Error(t, err)

	enc, err := parseEncryption("none")
	require.NoError(t, err)
	assert.Equal(t, sites.EncryptionNone, enc)
}

func TestBuild_errors(t *testing.T) {
	const (
		fitHeader  = "state,system,site,county,lat,lon,n,low,high,span,rtl_sdr_needed,rspduo_dual_fits,wide_tuners_needed,band,phase,encryption\n"
		freqHeader = "state,category,system,site,freq_mhz,mode,use,encrypted,lat,lon,source\n"
		fitRow     = "UT,Sys,Peak,Salt Lake,40.7,-111.9,2,770.0,771.0,1.0,1,True,1,700,P25 Phase II,partial\n"
		freqRows   = "UT,trunked,Sys,Peak,770.0,P25,control,partial,40.7,-111.9,src\n" +
			"UT,trunked,Sys,Peak,771.0,P25,voice/traffic,partial,40.7,-111.9,src\n" +
			"UT,aviation,,Tower,118.3,AM,tower,none,40.7,-111.9,src\n"
	)

	tests := []struct {
		name, fit, freq, want string
	}{
		{name: "ok", fit: fitHeader + fitRow, freq: freqHeader + freqRows},
		{name: "missing column", fit: "state,system\nUT,x\n", freq: freqHeader + freqRows, want: "missing column"},
		{name: "count mismatch", fit: fitHeader + strings.Replace(fitRow, ",2,", ",3,", 1), freq: freqHeader + freqRows, want: "n=3"},
		{name: "no control rows", fit: fitHeader + fitRow, freq: freqHeader + strings.ReplaceAll(freqRows, ",control,", ",voice/traffic,"), want: "no trunked control"},
		{name: "bad band", fit: fitHeader + strings.Replace(fitRow, ",700,", ",UHF,", 1), freq: freqHeader + freqRows, want: "band"},
		{name: "bad lat", fit: fitHeader + strings.Replace(fitRow, "40.7", "N", 1), freq: freqHeader + freqRows, want: "lat"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var out bytes.Buffer

			err := build(strings.NewReader(tc.fit), strings.NewReader(tc.freq), &out)
			if tc.want == "" {
				require.NoError(t, err)

				got, perr := sites.Parse(&out)
				require.NoError(t, perr)
				require.Len(t, got, 1)
				assert.Equal(t, []uint64{770_000_000}, got[0].ControlHz)
				assert.Equal(t, []uint64{771_000_000}, got[0].VoiceHz)

				return
			}

			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.want)
		})
	}
}
