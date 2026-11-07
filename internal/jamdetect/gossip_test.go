package jamdetect_test

import (
	"errors"
	"net"
	"testing"
	"time"

	jamv1 "github.com/openmanet/openmanetd/internal/api/openmanet/jamdetect/v1"
	"github.com/openmanet/openmanetd/internal/jamdetect"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/timestamppb"
)

const selfMAC = "02:00:00:00:00:01"

func newGossipHarness(t *testing.T, g *fakeGossip) *harness {
	t.Helper()

	return newHarness(t, jamdetect.Options{
		Gossip:       g,
		GossipExpiry: time.Minute,
		Identity:     func() (string, string) { return "02:00:00:00:00:01", "mast-1" },
		Position: func() (jamdetect.Position, bool) {
			return jamdetect.Position{Latitude: 40.31, Longitude: -112.0, AltitudeM: 1485}, true
		},
	})
}

func remoteRecord(t *testing.T, mac, host string, at time.Time, jammed bool) jamdetect.GossipRecord {
	t.Helper()

	rep := &jamv1.JamReport{PrimaryMac: mac, Hostname: host, CollectedAt: timestamppb.New(at)}
	if jammed {
		rep.ActiveEvents = []*jamv1.JamEvent{{Id: "wlh0-1", State: jamv1.JamState_JAM_STATE_JAMMED}}
	}

	b, err := rep.MarshalVT()
	require.NoError(t, err)

	hw, err := net.ParseMAC(mac)
	require.NoError(t, err)

	return jamdetect.GossipRecord{Source: hw, Data: b}
}

func TestGossip_publishesLocalReport(t *testing.T) {
	g := &fakeGossip{}
	h := newGossipHarness(t, g)
	h.healthy(20)
	h.jammed(jamdetect.DefaultJamSamples)

	h.det.GossipOnce()

	rep := &jamv1.JamReport{}
	require.NoError(t, rep.UnmarshalVT(g.lastPublished()))
	assert.Equal(t, selfMAC, rep.GetPrimaryMac())
	assert.Equal(t, "mast-1", rep.GetHostname())
	assert.InDelta(t, 40.31, rep.GetPosition().GetLatitude(), 1e-9)
	require.Len(t, rep.GetRadios(), 1)
	assert.Equal(t, jamv1.JamState_JAM_STATE_JAMMED, rep.GetRadios()[0].GetState())
	assert.Equal(t, jamv1.RadioBand_RADIO_BAND_5G, rep.GetRadios()[0].GetBand())
	require.Len(t, rep.GetActiveEvents(), 1)
	assert.Equal(t, int32(-80), rep.GetActiveEvents()[0].GetRadio().GetNoiseDbm())
	assert.Contains(t, rep.GetActiveEvents()[0].GetRadio().GetReasons(), jamv1.JamReason_JAM_REASON_NOISE_RISE)
}

func TestGossip_ingestSkipsSelfAndBadRecords(t *testing.T) {
	g := &fakeGossip{}
	h := newGossipHarness(t, g)
	now := h.clk.Now()

	g.setRecords(
		remoteRecord(t, "02:00:00:00:00:03", "node-b", now, true),
		remoteRecord(t, "02:00:00:00:00:02", "node-a", now, false),
		remoteRecord(t, selfMAC, "mast-1", now, false),
		jamdetect.GossipRecord{Data: []byte{0xff, 0xff, 0xff}},
	)

	h.det.GossipOnce()

	remote := h.det.Remote()
	require.Len(t, remote, 2)
	assert.Equal(t, "node-a", remote[0].GetHostname())
	assert.Equal(t, "node-b", remote[1].GetHostname())

	snap := &jamdetect.Snapshotter{Detector: h.det}
	snap.Refresh()

	data, ok := snap.Data().(*jamdetect.Snapshot)
	require.True(t, ok)
	assert.Equal(t, uint32(2), data.RemoteNodes)
	assert.Equal(t, uint32(1), data.RemoteNodesJammed)
	assert.Equal(t, uint64(1), data.GossipDecodeErrorsTotal)

	// Active never includes gossiped episodes.
	assert.Empty(t, h.det.Active())
}

func TestGossip_expiresSilentNodes(t *testing.T) {
	g := &fakeGossip{}
	h := newGossipHarness(t, g)
	start := h.clk.Now()

	g.setRecords(
		remoteRecord(t, "02:00:00:00:00:02", "frozen", start, false),
		remoteRecord(t, "02:00:00:00:00:03", "alive", start, false),
	)
	h.det.GossipOnce()
	require.Len(t, h.det.Remote(), 2)

	// "alive" keeps publishing; "frozen" repeats its old record (alfred
	// keeps serving it until its own timeout).
	for i := 1; i <= 7; i++ {
		h.clk.Advance(10 * time.Second)
		g.setRecords(
			remoteRecord(t, "02:00:00:00:00:02", "frozen", start, false),
			remoteRecord(t, "02:00:00:00:00:03", "alive", start.Add(time.Duration(i)*10*time.Second), false),
		)
		h.det.GossipOnce()
	}

	remote := h.det.Remote()
	require.Len(t, remote, 1)
	assert.Equal(t, "alive", remote[0].GetHostname())
}

func TestGossip_errorsAreCounted(t *testing.T) {
	g := &fakeGossip{publishErr: errors.New("socket"), fetchErr: errors.New("socket")}
	h := newGossipHarness(t, g)

	h.det.GossipOnce()

	snap := &jamdetect.Snapshotter{Detector: h.det}
	snap.Refresh()

	data, ok := snap.Data().(*jamdetect.Snapshot)
	require.True(t, ok)
	assert.Equal(t, uint64(1), data.GossipPublishErrorsTotal)
	assert.Equal(t, uint64(1), data.GossipFetchErrorsTotal)
}

func TestSnapshotter_refreshDoesNotAllocate(t *testing.T) {
	h := newHarness(t, jamdetect.Options{})
	h.healthy(20)
	h.jammed(jamdetect.DefaultJamSamples)

	snap := &jamdetect.Snapshotter{Detector: h.det}

	allocs := testing.AllocsPerRun(100, snap.Refresh)
	assert.Zero(t, allocs)

	data, ok := snap.Data().(*jamdetect.Snapshot)
	require.True(t, ok)
	assert.Equal(t, uint32(1), data.RadiosMonitored)
	assert.Equal(t, uint32(1), data.RadiosJammed)
	assert.Equal(t, uint64(2), data.EventsTotal)
	assert.Equal(t, uint64(1), data.JammedEventsTotal)

	assert.NotPanics(t, (&jamdetect.Snapshotter{}).Refresh)
}
