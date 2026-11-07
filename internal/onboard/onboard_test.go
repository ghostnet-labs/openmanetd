package onboard_test

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/openmanet/openmanetd/internal/config"
	"github.com/openmanet/openmanetd/internal/onboard"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// makeZip returns a zip holding the named entries.
func makeZip(t *testing.T, names ...string) []byte {
	t.Helper()

	var buf bytes.Buffer

	zw := zip.NewWriter(&buf)
	for _, n := range names {
		w, err := zw.Create(n)
		require.NoError(t, err)

		_, err = w.Write([]byte("x-" + n))
		require.NoError(t, err)
	}

	require.NoError(t, zw.Close())

	return buf.Bytes()
}

func makeAPK(t *testing.T) []byte {
	t.Helper()

	return makeZip(t, "AndroidManifest.xml", "classes.dex", "res/a.png")
}

func newStore(t *testing.T, maxBytes int64) (*onboard.Store, string) {
	t.Helper()

	dir := filepath.Join(t.TempDir(), "onboard")
	s, err := onboard.NewStore(dir, maxBytes)
	require.NoError(t, err)

	return s, dir
}

func TestStore_saveOpenReload(t *testing.T) {
	s, dir := newStore(t, 1<<20)

	_, ok := s.Info()
	assert.False(t, ok, "empty store has no APK")

	_, _, err := s.Open()
	require.ErrorIs(t, err, onboard.ErrNoAPK)

	apk := makeAPK(t)
	sum := sha256.Sum256(apk)

	info, err := s.Save(bytes.NewReader(apk), `C:\Downloads\../ATAK-CIV-5.6.apk`)
	require.NoError(t, err)
	assert.Equal(t, "ATAK-CIV-5.6.apk", info.Name, "only the base name is kept")
	assert.Equal(t, int64(len(apk)), info.Size)
	assert.Equal(t, hex.EncodeToString(sum[:]), info.SHA256)
	assert.False(t, info.Uploaded.IsZero())

	f, got, err := s.Open()
	require.NoError(t, err)
	t.Cleanup(func() { _ = f.Close() })

	data, err := io.ReadAll(f)
	require.NoError(t, err)
	assert.Equal(t, apk, data)
	assert.Equal(t, info, got)

	// A restart picks the APK up from disk.
	again, err := onboard.NewStore(dir, 1<<20)
	require.NoError(t, err)

	reloaded, ok := again.Info()
	require.True(t, ok)
	assert.Equal(t, info.SHA256, reloaded.SHA256)
	assert.True(t, info.Uploaded.Equal(reloaded.Uploaded))
}

func TestStore_rejects(t *testing.T) {
	tests := []struct {
		want error
		name string
		data []byte
		max  int64
	}{
		{name: "plain text", data: []byte("not a zip"), max: 1 << 20, want: onboard.ErrNotAPK},
		{name: "zip without dex", data: makeZip(t, "AndroidManifest.xml"), max: 1 << 20, want: onboard.ErrNotAPK},
		{name: "zip without manifest", data: makeZip(t, "classes.dex"), max: 1 << 20, want: onboard.ErrNotAPK},
		{name: "over the cap", data: makeAPK(t), max: 10, want: onboard.ErrTooLarge},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s, dir := newStore(t, tc.max)

			_, err := s.Save(bytes.NewReader(tc.data), "x.apk")
			require.ErrorIs(t, err, tc.want)

			_, ok := s.Info()
			assert.False(t, ok)

			entries, err := os.ReadDir(dir)
			require.NoError(t, err)
			assert.Empty(t, entries, "a rejected upload leaves no temp file")
		})
	}
}

func TestStore_rejectKeepsPrevious(t *testing.T) {
	s, _ := newStore(t, 1<<20)

	first, err := s.Save(bytes.NewReader(makeAPK(t)), "good.apk")
	require.NoError(t, err)

	_, err = s.Save(strings.NewReader("garbage"), "bad.apk")
	require.ErrorIs(t, err, onboard.ErrNotAPK)

	info, ok := s.Info()
	require.True(t, ok)
	assert.Equal(t, first, info)
}

func TestStore_remove(t *testing.T) {
	s, dir := newStore(t, 1<<20)

	_, err := s.Save(bytes.NewReader(makeAPK(t)), "a.apk")
	require.NoError(t, err)
	require.NoError(t, s.Remove())
	require.NoError(t, s.Remove(), "removing twice is fine")

	_, ok := s.Info()
	assert.False(t, ok)

	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	assert.Empty(t, entries)
}

func TestNewStore_ignoresMismatchedMeta(t *testing.T) {
	s, dir := newStore(t, 1<<20)

	_, err := s.Save(bytes.NewReader(makeAPK(t)), "a.apk")
	require.NoError(t, err)

	// Truncate the APK so its size no longer matches the metadata.
	require.NoError(t, os.WriteFile(filepath.Join(dir, "atak.apk"), []byte("x"), 0o600))

	again, err := onboard.NewStore(dir, 1<<20)
	require.NoError(t, err)

	_, ok := again.Info()
	assert.False(t, ok)
}

// readZip returns every entry of a zip by name.
func readZip(t *testing.T, data []byte) map[string]string {
	t.Helper()

	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	require.NoError(t, err)

	out := make(map[string]string, len(zr.File))

	for _, f := range zr.File {
		rc, err := f.Open()
		require.NoError(t, err)

		b, err := io.ReadAll(rc)
		require.NoError(t, err)
		require.NoError(t, rc.Close())

		out[f.Name] = string(b)
	}

	return out
}

func TestBuildDataPackage_full(t *testing.T) {
	var buf bytes.Buffer

	p := onboard.Profile{Callsign: "Bravo 6", Team: "Dark Blue", Role: "Team Lead"}
	err := onboard.BuildDataPackage(&buf, "http://10.41.3.7:8080/tiles/", p, config.DefaultOnboardMapSources())
	require.NoError(t, err)

	files := readZip(t, buf.Bytes())
	require.Len(t, files, 4)

	manifest := files["MANIFEST/manifest.xml"]
	assert.Contains(t, manifest, `<MissionPackageManifest version="2">`)
	assert.Contains(t, manifest, `<Parameter name="uid" value="`+onboard.PackageUID+`"/>`)
	assert.Contains(t, manifest, `zipEntry="prefs/openmanet.pref"`)
	assert.Contains(t, manifest, `zipEntry="mapsources/satellite.xml"`)
	assert.Contains(t, manifest, `zipEntry="mapsources/topo.xml"`)

	pref := files["prefs/openmanet.pref"]
	assert.Contains(t, pref, `<preference version="1" name="com.atakmap.app_preferences">`)
	assert.Contains(t, pref, `<entry key="locationCallsign" class="class java.lang.String">Bravo 6</entry>`)
	assert.Contains(t, pref, `<entry key="locationTeam" class="class java.lang.String">Dark Blue</entry>`)
	assert.Contains(t, pref, `<entry key="atakRoleType" class="class java.lang.String">Team Lead</entry>`)

	sat := files["mapsources/satellite.xml"]
	assert.Contains(t, sat, "<customMapSource>")
	assert.Contains(t, sat, "<name>Manet Satellite</name>")
	assert.Contains(t, sat, "<url>http://10.41.3.7:8080/tiles/satellite/{$z}/{$x}/{$y}</url>", "trailing slash on the base is trimmed")
	assert.Contains(t, sat, "<tileType>jpg</tileType>")
	assert.Contains(t, sat, "<maxZoom>17</maxZoom>")
}

func TestBuildDataPackage_emptyProfileAndNoMaps(t *testing.T) {
	var buf bytes.Buffer

	require.NoError(t, onboard.BuildDataPackage(&buf, "http://h/tiles", onboard.Profile{}, nil))

	files := readZip(t, buf.Bytes())
	require.Len(t, files, 1, "only the manifest")
	assert.NotContains(t, files["MANIFEST/manifest.xml"], "zipEntry=")
}

func TestBuildDataPackage_escapesMapSourceText(t *testing.T) {
	var buf bytes.Buffer

	src := []config.OnboardMapSource{{Name: "A & B <x>", Layer: "ab", TileType: "png", MaxZoom: 3}}
	require.NoError(t, onboard.BuildDataPackage(&buf, "http://h/tiles?k=1&v=2", onboard.Profile{}, src))

	xml := readZip(t, buf.Bytes())["mapsources/ab.xml"]
	assert.Contains(t, xml, "<name>A &amp; B &lt;x&gt;</name>")
	assert.Contains(t, xml, "k=1&amp;v=2")
}

func TestProfileValidate(t *testing.T) {
	tests := []struct {
		name    string
		p       onboard.Profile
		wantErr bool
	}{
		{name: "empty", p: onboard.Profile{}},
		{name: "full", p: onboard.Profile{Callsign: "Alpha-1.a_b", Team: "Cyan", Role: "Medic"}},
		{name: "forty chars", p: onboard.Profile{Callsign: strings.Repeat("a", 40)}},
		{name: "forty-one chars", p: onboard.Profile{Callsign: strings.Repeat("a", 41)}, wantErr: true},
		{name: "markup in callsign", p: onboard.Profile{Callsign: "<b>"}, wantErr: true},
		{name: "unknown team", p: onboard.Profile{Team: "Pink"}, wantErr: true},
		{name: "team wrong case", p: onboard.Profile{Team: "cyan"}, wantErr: true},
		{name: "unknown role", p: onboard.Profile{Role: "Pilot"}, wantErr: true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.p.Validate()
			if tc.wantErr {
				require.ErrorIs(t, err, onboard.ErrBadProfile)

				return
			}

			require.NoError(t, err)
		})
	}
}

func TestBuildDataPackage_rejectsBadProfile(t *testing.T) {
	var buf bytes.Buffer

	err := onboard.BuildDataPackage(&buf, "http://h/tiles", onboard.Profile{Team: "Pink"}, nil)
	require.ErrorIs(t, err, onboard.ErrBadProfile)
	assert.Zero(t, buf.Len(), "nothing is written for a bad profile")
}
