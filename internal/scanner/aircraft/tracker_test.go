package aircraft_test

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"testing/fstest"
	"time"

	"github.com/openmanet/openmanetd/internal/scanner/aircraft"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	path1090 = "var/run/dump1090/aircraft.json"
	path978  = "var/run/skyaware978/aircraft.json"
)

// fileNow is the "now" of dump1090-aircraft.json.
var fileNow = time.Unix(1762150000, 0) //nolint:gochecknoglobals // test constant

func fixture(t *testing.T, name string) []byte {
	t.Helper()

	_, file, _, ok := runtime.Caller(0)
	require.True(t, ok)

	dir := filepath.Dir(file)
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			break
		}

		parent := filepath.Dir(dir)
		require.NotEqual(t, parent, dir, "module root not found")
		dir = parent
	}

	b, err := os.ReadFile(filepath.Join(dir, "testfixtures", "scanner", name))
	require.NoError(t, err)

	return b
}

func fixtureFS(t *testing.T) fstest.MapFS {
	t.Helper()

	return fstest.MapFS{
		path1090: {Data: fixture(t, "dump1090-aircraft.json")},
		path978:  {Data: fixture(t, "skyaware978-aircraft.json")},
	}
}

func newTracker(maxAircraft int) *aircraft.Tracker {
	return aircraft.NewTracker([]aircraft.Feed{
		{Path: path1090, Source: aircraft.Source1090},
		{Path: path978, Source: aircraft.Source978},
	}, maxAircraft, time.Minute)
}

func byICAO(list []aircraft.Aircraft) map[string]aircraft.Aircraft {
	out := make(map[string]aircraft.Aircraft, len(list))
	for _, a := range list {
		out[a.ICAO] = a
	}

	return out
}

func TestTracker_mergesBothFeeds(t *testing.T) {
	tr := newTracker(0)
	tr.Poll(fixtureFS(t), fileNow)

	list := tr.Snapshot(nil, fileNow, false)
	got := byICAO(list)

	// c0ffee was last seen 250 s ago: evicted (> 2 x 60 s).
	require.Len(t, list, 5)
	assert.NotContains(t, got, "c0ffee")

	icaos := make([]string, 0, len(list))
	for _, a := range list {
		icaos = append(icaos, a.ICAO)
	}

	assert.Equal(t, []string{"4b1805", "a00001", "a1b2c3", "abc123", "~2a0b0c"}, icaos, "sorted by ICAO")

	ual := got["a1b2c3"]
	assert.Equal(t, "UAL123", ual.Callsign)
	assert.Equal(t, "1200", ual.Squawk)
	assert.Equal(t, aircraft.Source1090, ual.Sources)
	assert.True(t, ual.HasPos)
	assert.InDelta(t, 40.7608, ual.Lat, 1e-9)
	assert.Equal(t, int32(35000), ual.AltFt)
	assert.Equal(t, int32(-64), ual.VRate)
	assert.InDelta(t, 450.2, ual.GS, 1e-9)
	assert.InDelta(t, 270.5, ual.Track, 1e-9)
	assert.InDelta(t, -12.3, ual.RSSI, 1e-9)
	assert.Equal(t, fileNow.Add(-500*time.Millisecond), ual.LastSeen)
	assert.False(t, ual.Stale)

	ground := got["abc123"]
	assert.True(t, ground.OnGround)
	assert.False(t, ground.HasAlt)

	// Heard on both: 978 is newer, carries the position, callsign and
	// vertical rate; 1090 contributes the squawk; RSSI is the strongest.
	both := got["4b1805"]
	assert.Equal(t, aircraft.SourceBoth, both.Sources)
	assert.Equal(t, "N456CD", both.Callsign)
	assert.Equal(t, "7000", both.Squawk)
	assert.True(t, both.HasPos)
	assert.InDelta(t, 40.70, both.Lat, 1e-9)
	assert.Equal(t, int32(4500), both.AltFt)
	assert.Equal(t, int32(500), both.VRate)
	assert.InDelta(t, -20.0, both.RSSI, 1e-9)

	ga := got["a00001"]
	assert.Equal(t, aircraft.Source978, ga.Sources)
	assert.Equal(t, int32(8500), ga.AltFt, "geometric altitude when barometric is missing")
	assert.Equal(t, int32(-300), ga.VRate)
	assert.False(t, ga.HasPos)

	tisb := got["~2a0b0c"]
	assert.False(t, tisb.HasRSSI)
	assert.False(t, tisb.HasGS)
}

func TestTracker_staleAndEviction(t *testing.T) {
	tr := newTracker(0)
	tr.Poll(fixtureFS(t), fileNow)

	later := fileNow.Add(90 * time.Second)
	assert.Empty(t, tr.Snapshot(nil, later, false), "all stale after 90 s")

	stale := tr.Snapshot(nil, later, true)
	require.Len(t, stale, 5)

	for _, a := range stale {
		assert.True(t, a.Stale, a.ICAO)
		assert.NotZero(t, a.Sources, "stale aircraft keep the receivers that last heard them")
	}

	// Polling an unchanged file 3 minutes later evicts everything: its
	// "now" did not advance, so nothing is refreshed.
	tr.Poll(fixtureFS(t), fileNow.Add(3*time.Minute))
	assert.Equal(t, 0, tr.Len())
}

func TestTracker_boundedByMaxAircraft(t *testing.T) {
	tr := newTracker(2)
	tr.Poll(fixtureFS(t), fileNow)

	assert.Equal(t, 2, tr.Len())
	// The newest two (a00001 seen 0.2 s before 978 now, plus the most
	// recent other) survive; eviction removes the oldest when full.
	got := byICAO(tr.Snapshot(nil, fileNow, true))
	assert.Contains(t, got, "a00001")
}

func TestTracker_feeds(t *testing.T) {
	fsys := fixtureFS(t)
	delete(fsys, path978)

	tr := newTracker(0)
	tr.Poll(fsys, fileNow)

	feeds := tr.Feeds(fileNow)
	require.Len(t, feeds, 2)

	assert.Equal(t, aircraft.Source1090, feeds[0].Source)
	assert.NoError(t, feeds[0].Err)
	assert.False(t, feeds[0].Stale)
	assert.Equal(t, 4, feeds[0].Aircraft)
	assert.Equal(t, fileNow, feeds[0].Updated)

	assert.Equal(t, aircraft.Source978, feeds[1].Source)
	require.Error(t, feeds[1].Err)
	assert.True(t, errors.Is(feeds[1].Err, fs.ErrNotExist))
	assert.True(t, feeds[1].Stale)

	assert.True(t, tr.Feeds(fileNow.Add(2 * time.Minute))[0].Stale, "producer clock older than stale timeout")
}

func TestTracker_malformedFile(t *testing.T) {
	tests := []struct {
		name string
		data []byte
	}{
		{name: "truncated", data: fixture(t, "malformed-aircraft.json")},
		{name: "no now", data: []byte(`{"aircraft":[]}`)},
		{name: "bad altitude", data: []byte(`{"now":1,"aircraft":[{"hex":"a","alt_baro":true}]}`)},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			tr := newTracker(0)
			tr.Poll(fstest.MapFS{path1090: {Data: tc.data}}, fileNow)

			f := tr.Feeds(fileNow)[0]
			require.Error(t, f.Err)
			assert.False(t, errors.Is(f.Err, fs.ErrNotExist))
			assert.True(t, f.Stale)
			assert.Equal(t, 0, tr.Len())
		})
	}
}

func TestTracker_reusesSnapshotBuffer(t *testing.T) {
	tr := newTracker(0)
	tr.Poll(fixtureFS(t), fileNow)

	buf := make([]aircraft.Aircraft, 0, 16)
	out := tr.Snapshot(buf, fileNow, false)
	require.Len(t, out, 5)
	assert.Equal(t, &buf[:1][0], &out[0], "snapshot appends into the caller's buffer")
}

func TestTracker_updatesOnNewerFile(t *testing.T) {
	fsys := fixtureFS(t)
	tr := newTracker(0)
	tr.Poll(fsys, fileNow)

	fsys[path1090] = &fstest.MapFile{Data: []byte(`{"now":1762150010.0,"aircraft":[
		{"hex":"a1b2c3","flight":"UAL123","alt_baro":34000,"lat":40.9,"lon":-111.5,"seen":0,"seen_pos":0}]}`)}

	tr.SetStaleAfter(30 * time.Second)
	tr.Poll(fsys, fileNow.Add(10*time.Second))

	a := byICAO(tr.Snapshot(nil, fileNow.Add(10*time.Second), false))["a1b2c3"]
	assert.Equal(t, int32(34000), a.AltFt)
	assert.InDelta(t, 40.9, a.Lat, 1e-9)
	assert.Equal(t, fileNow.Add(10*time.Second), a.LastSeen)
}
