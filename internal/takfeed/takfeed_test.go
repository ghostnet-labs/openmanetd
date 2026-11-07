package takfeed_test

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	meshtopov1 "github.com/openmanet/openmanetd/internal/api/openmanet/mesh_topology/v1"
	"github.com/openmanet/openmanetd/internal/database/models"
	"github.com/openmanet/openmanetd/internal/scanner/aircraft"
	"github.com/openmanet/openmanetd/internal/system"
	"github.com/openmanet/openmanetd/internal/takfeed"
)

const (
	selfLat = 40.40
	selfLon = -112.00
)

type harness struct {
	sender   *fakeSender
	cfg      *fakeConfig
	pos      *fakePosition
	topo     *fakeTopology
	nodes    *fakeNodes
	aircraft *fakeAircraft
	jams     *fakeJams
	opts     takfeed.Options
}

func newHarness(t *testing.T, yaml string) *harness {
	t.Helper()

	h := &harness{
		sender:   &fakeSender{},
		cfg:      newFakeConfig(t, yaml),
		pos:      &fakePosition{pos: takfeed.Position{Time: testNow, Lat: selfLat, Lon: selfLon}, ok: true},
		topo:     &fakeTopology{topo: threeNodeTopology()},
		nodes:    &fakeNodes{nodes: threeNodeRows()},
		aircraft: &fakeAircraft{},
		jams:     &fakeJams{},
	}

	h.opts = takfeed.Options{
		Log:      zerolog.Nop(),
		Config:   h.cfg,
		Sender:   h.sender,
		Position: h.pos,
		Topology: h.topo,
		Nodes:    h.nodes,
		Battery:  &fakeBattery{st: &system.BatteryStatus{Present: true, ChargePercent: 50, VoltageVolts: 11.5}},
		Aircraft: h.aircraft,
		Jams:     h.jams,
		Callsign: "manet01-MANET",
		Hostname: "manet01",
		Now:      func() time.Time { return testNow },
	}

	return h
}

// threeNodeTopology: self (aa) links directly to bb and cc; bb-cc is a
// link between two other nodes and must not be published by self.
func threeNodeTopology() *meshtopov1.MeshTopology {
	return &meshtopov1.MeshTopology{
		SelfMac:   "aa",
		Algorithm: "BATMAN_IV",
		Nodes: []*meshtopov1.MeshNode{
			{Mac: "aa", Hostname: "manet01", IsSelf: true, IsGateway: true},
			{Mac: "bb", Hostname: "manet02"},
			{Mac: "cc", Hostname: "manet03"},
		},
		Edges: []*meshtopov1.MeshEdge{
			{FromMac: "aa", ToMac: "bb", Metric: 1.1},
			{FromMac: "aa", ToMac: "cc", Metric: 2.4, Blos: true},
			{FromMac: "bb", ToMac: "cc", Metric: 1.0},
		},
	}
}

func row(host, ip string, lat, lon float64, hasPos bool) models.MeshNode {
	return models.MeshNode{
		Hostname:  host,
		IpAddr:    ip,
		Latitude:  sql.NullFloat64{Float64: lat, Valid: hasPos},
		Longitude: sql.NullFloat64{Float64: lon, Valid: hasPos},
	}
}

func threeNodeRows() []models.MeshNode {
	return []models.MeshNode{
		row("manet01", "10.41.1.1", selfLat, selfLon, true),
		row("manet02", "10.41.2.1", 40.41, -112.01, true),
		row("manet03", "10.41.3.1", 40.42, -112.02, true),
	}
}

func TestPublishOnce_nodeMarkerWithMeshHealth(t *testing.T) {
	h := newHarness(t, "")
	p := takfeed.New(h.opts)

	p.PublishOnce(context.Background())

	nodes := byType(h.sender.events(t), takfeed.TypeRadio)
	require.Len(t, nodes, 1)
	assert.Equal(t, "manet01-MANET", nodes[0].GetUid())

	xml := nodes[0].GetDetail().GetXmlDetail()
	assert.Contains(t, xml, "Battery 50% 11.5 V")
	assert.Contains(t, xml, "2 direct mesh links, 3 nodes on the mesh")
	assert.Contains(t, xml, "Internet gateway")
	assert.Contains(t, xml, "http://10.41.1.1")
}

func TestPublishOnce_mastRole(t *testing.T) {
	h := newHarness(t, "tak:\n  role: mast\n")

	takfeed.New(h.opts).PublishOnce(context.Background())

	assert.Len(t, byType(h.sender.events(t), takfeed.TypeMast), 1)
	assert.Empty(t, byType(h.sender.events(t), takfeed.TypeRadio))
}

func TestPublishOnce_noNodeMarkerWithoutFreshFix(t *testing.T) {
	tests := []struct {
		name string
		pos  takfeed.Position
		ok   bool
	}{
		{name: "no fix", ok: false},
		{name: "null island", pos: takfeed.Position{Time: testNow}, ok: true},
		{name: "stale fix", pos: takfeed.Position{Time: testNow.Add(-3 * time.Minute), Lat: 1, Lon: 1}, ok: true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t, "")
			h.pos.set(tc.pos, tc.ok)

			takfeed.New(h.opts).PublishOnce(context.Background())

			evs := h.sender.events(t)
			assert.Empty(t, byType(evs, takfeed.TypeRadio))
			assert.Empty(t, byType(evs, takfeed.TypeLink), "links need this node's own position")
		})
	}
}

func TestPublishOnce_cameraOwnsMarker(t *testing.T) {
	h := newHarness(t, "")
	h.opts.CameraOwnsMarker = true

	takfeed.New(h.opts).PublishOnce(context.Background())

	evs := h.sender.events(t)
	assert.Empty(t, byType(evs, takfeed.TypeRadio))
	assert.Len(t, byType(evs, takfeed.TypeLink), 2, "links still go out on camera nodes")
}

func TestPublishOnce_disabledSendsNothing(t *testing.T) {
	h := newHarness(t, "tak:\n  enable: false\n")

	takfeed.New(h.opts).PublishOnce(context.Background())

	assert.Empty(t, h.sender.events(t))
}

func TestPublishOnce_linksOnlyOwnEdgesAndRetractOnLoss(t *testing.T) {
	h := newHarness(t, "")
	p := takfeed.New(h.opts)

	p.PublishOnce(context.Background())

	links := byType(h.sender.events(t), takfeed.TypeLink)
	require.Len(t, links, 2, "only edges touching this node")

	uids := []string{links[0].GetUid(), links[1].GetUid()}
	assert.ElementsMatch(t, []string{"manet-link-manet01-manet02", "manet-link-manet01-manet03"}, uids)

	// Links refresh every third tick; drop manet03's link and advance.
	topo := threeNodeTopology()
	topo.Edges = topo.Edges[:1]
	h.topo.set(topo)
	p.PublishOnce(context.Background())
	p.PublishOnce(context.Background())
	h.sender.reset()
	p.PublishOnce(context.Background())

	evs := h.sender.events(t)
	assert.Len(t, byType(evs, takfeed.TypeLink), 1)

	deletes := byType(evs, takfeed.TypeDelete)
	require.Len(t, deletes, 1)
	assert.Contains(t, deletes[0].GetDetail().GetXmlDetail(), `uid="manet-link-manet01-manet03"`)
}

func TestPublishOnce_linkUIDIsSymmetric(t *testing.T) {
	// manet02 publishing the same link must produce the same uid.
	h := newHarness(t, "")
	topo := threeNodeTopology()
	topo.Nodes[0].IsSelf = false
	topo.Nodes[1].IsSelf = true
	topo.SelfMac = "bb"
	h.topo.set(topo)
	h.opts.Hostname = "manet02"

	takfeed.New(h.opts).PublishOnce(context.Background())

	uids := make([]string, 0, 2)
	for _, e := range byType(h.sender.events(t), takfeed.TypeLink) {
		uids = append(uids, e.GetUid())
	}

	assert.Contains(t, uids, "manet-link-manet01-manet02")
}

func TestPublishOnce_peerWithoutPositionHasNoLine(t *testing.T) {
	h := newHarness(t, "")
	h.nodes.nodes = []models.MeshNode{row("manet02", "", 40.41, -112.01, true), row("manet03", "", 0, 0, false)}

	takfeed.New(h.opts).PublishOnce(context.Background())

	links := byType(h.sender.events(t), takfeed.TypeLink)
	require.Len(t, links, 1)
	assert.Equal(t, "manet-link-manet01-manet02", links[0].GetUid())
}

func TestPublishOnce_linksDisabled(t *testing.T) {
	h := newHarness(t, "tak:\n  links: false\n")

	takfeed.New(h.opts).PublishOnce(context.Background())

	assert.Empty(t, byType(h.sender.events(t), takfeed.TypeLink))
}

func TestPublishOnce_topologyErrorStillSendsNode(t *testing.T) {
	h := newHarness(t, "")
	h.topo.err = errors.New("batctl unavailable")

	takfeed.New(h.opts).PublishOnce(context.Background())

	nodes := byType(h.sender.events(t), takfeed.TypeRadio)
	require.Len(t, nodes, 1)
	assert.NotContains(t, nodes[0].GetDetail().GetXmlDetail(), "mesh link")
}

func plane(icao string, lat, lon float64) aircraft.Aircraft {
	return aircraft.Aircraft{ICAO: icao, Lat: lat, Lon: lon, HasPos: true, LastSeen: testNow}
}

func TestPublishOnce_aircraftRangeCapAndOrder(t *testing.T) {
	h := newHarness(t, "tak:\n  aircraft:\n    max: 2\n    rangeKm: 50\n")
	h.aircraft.list = []aircraft.Aircraft{
		plane("far", 41.5, -112.0),   // ~122 km: out of range
		plane("mid", 40.60, -112.0),  // ~22 km
		plane("near", 40.45, -112.0), // ~5.6 km
		plane("next", 40.50, -112.0), // ~11 km
		{ICAO: "nopos", LastSeen: testNow},
		{ICAO: "stale", Lat: 40.4, Lon: -112, HasPos: true, Stale: true},
	}

	takfeed.New(h.opts).PublishOnce(context.Background())

	planes := byType(h.sender.events(t), takfeed.TypeAircraft)
	require.Len(t, planes, 2)
	assert.Equal(t, "ICAO-NEAR", planes[0].GetUid())
	assert.Equal(t, "ICAO-NEXT", planes[1].GetUid())
}

func TestPublishOnce_aircraftWithoutFixKeepsNewest(t *testing.T) {
	h := newHarness(t, "tak:\n  aircraft:\n    max: 1\n")
	h.pos.set(takfeed.Position{}, false)

	old := plane("old", 10, 10)
	old.LastSeen = testNow.Add(-30 * time.Second)
	h.aircraft.list = []aircraft.Aircraft{old, plane("new", 60, 60)}

	takfeed.New(h.opts).PublishOnce(context.Background())

	planes := byType(h.sender.events(t), takfeed.TypeAircraft)
	require.Len(t, planes, 1, "no range filter without a fix")
	assert.Equal(t, "ICAO-NEW", planes[0].GetUid())
}

func TestPublishOnce_aircraftDisabled(t *testing.T) {
	h := newHarness(t, "tak:\n  aircraft:\n    enable: false\n")
	h.aircraft.list = []aircraft.Aircraft{plane("near", 40.45, -112.0)}

	takfeed.New(h.opts).PublishOnce(context.Background())

	assert.Empty(t, byType(h.sender.events(t), takfeed.TypeAircraft))
}

func TestPublishOnce_jamLifecycle(t *testing.T) {
	h := newHarness(t, "")
	p := takfeed.New(h.opts)

	h.jams.set([]takfeed.Jam{{ID: "e1", State: takfeed.JamActive, FreqMHz: 915}})
	p.PublishOnce(context.Background())

	jams := byType(h.sender.events(t), takfeed.TypeJamActive)
	require.Len(t, jams, 1)
	assert.Equal(t, "manet01-MANET-jam-e1", jams[0].GetUid())
	assert.InDelta(t, selfLat, jams[0].GetLat(), 1e-9, "jam marker sits on the detecting node")

	h.sender.reset()
	h.jams.set(nil)
	p.PublishOnce(context.Background())

	deletes := byType(h.sender.events(t), takfeed.TypeDelete)
	require.Len(t, deletes, 1, "a cleared jam is removed from the map right away")
	assert.Contains(t, deletes[0].GetDetail().GetXmlDetail(), `uid="manet01-MANET-jam-e1"`)

	h.sender.reset()
	p.PublishOnce(context.Background())
	assert.Empty(t, byType(h.sender.events(t), takfeed.TypeDelete), "a delete is sent once")
}

func TestPublishOnce_disablingRetractsLinksAndJams(t *testing.T) {
	h := newHarness(t, "")
	p := takfeed.New(h.opts)
	h.jams.set([]takfeed.Jam{{ID: "e1", State: takfeed.JamSuspected}})

	p.PublishOnce(context.Background())
	h.sender.reset()

	h.cfg.load(t, "tak:\n  enable: false\n")
	p.PublishOnce(context.Background())

	evs := h.sender.events(t)
	assert.Len(t, byType(evs, takfeed.TypeDelete), 3, "two links and one jam")
	assert.Len(t, evs, 3)
}

func TestPublishOnce_sendErrorsDoNotStopTheTick(t *testing.T) {
	h := newHarness(t, "")
	h.sender.err = errors.New("no interface")
	p := takfeed.New(h.opts)

	assert.NotPanics(t, func() {
		p.PublishOnce(context.Background())
		p.PublishOnce(context.Background())
	})

	h.sender.err = nil

	p.PublishOnce(context.Background())
	assert.NotEmpty(t, h.sender.events(t))
}

func TestRun_returnsOnCancel(t *testing.T) {
	h := newHarness(t, "")
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})

	go func() {
		defer close(done)

		takfeed.New(h.opts).Run(ctx)
	}()

	cancel()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return after cancel")
	}

	assert.NotEmpty(t, byType(h.sender.events(t), takfeed.TypeRadio), "Run publishes immediately")
}

func TestNew_requiresConfigAndSender(t *testing.T) {
	assert.Panics(t, func() { takfeed.New(takfeed.Options{}) })
}
