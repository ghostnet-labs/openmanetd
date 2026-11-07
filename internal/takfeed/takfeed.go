// Package takfeed puts everything a node knows onto every ATAK map on the
// mesh: this node (radio or mast icon, with battery and mesh health), its
// direct mesh links as colored lines, aircraft its scanner hears, and
// jamming its radios detect.
//
// It sends plain Cursor-on-Target to the ATAK SA multicast group
// (239.2.3.1:6969). ATAK listens there out of the box, and the mesh
// floods multicast to every node and every bridged AP client, so no
// plugin and no phone setup is needed. Each node only publishes what it
// sees itself; ATAK merges items by uid, so a link both ends publish, or
// an aircraft two nodes hear, still shows once.
//
// One goroutine (Run) owns all state. Every source is optional; a nil
// source just leaves its items off the map.
package takfeed

import (
	"cmp"
	"context"
	"database/sql"
	"math"
	"slices"
	"time"

	"github.com/coreywagehoft/go-tak/pkg/cot"
	"github.com/coreywagehoft/go-tak/pkg/cotproto"
	meshtopov1 "github.com/openmanet/openmanetd/internal/api/openmanet/mesh_topology/v1"
	"github.com/openmanet/openmanetd/internal/config"
	"github.com/openmanet/openmanetd/internal/database/models"
	"github.com/openmanet/openmanetd/internal/scanner/aircraft"
	"github.com/openmanet/openmanetd/internal/system"
	"github.com/rs/zerolog"
	"google.golang.org/protobuf/types/known/emptypb"
)

const (
	// linkEvery publishes links on every third tick (30 s by default):
	// links change slowly and each one costs a mesh-wide flood.
	linkEvery = 3
	// DefaultMaxPositionAge is how old a fix may be before this node
	// stops placing itself (and its links and jammers) on the map.
	DefaultMaxPositionAge = 2 * time.Minute
	// maxTracked bounds the link and jam uids remembered for deletes.
	maxTracked = 64
	// earthRadiusKm is the mean Earth radius for haversine distances.
	earthRadiusKm = 6371.0
)

// ConfigSource returns the live tak settings; read every tick so edits
// to config.yml apply without a restart.
type ConfigSource interface {
	GetTAK() config.TAKConfig
}

// Sender delivers one encoded CoT datagram to the mesh.
type Sender interface {
	Send(data []byte) error
}

// PositionSource returns this node's current fix; ok is false with no fix.
type PositionSource interface {
	Position() (Position, bool)
}

// TopologySource is the mesh topology RPC handler.
type TopologySource interface {
	GetMeshTopology(ctx context.Context, req *emptypb.Empty) (*meshtopov1.GetMeshTopologyResponse, error)
}

// NodeStore lists mesh nodes with the positions they gossip over alfred.
type NodeStore interface {
	ListMeshNodes(ctx context.Context) ([]models.MeshNode, error)
}

// BatterySource reads the node's battery monitor.
type BatterySource interface {
	GetBatteryStatus() (*system.BatteryStatus, error)
}

// AircraftSource is the scanner manager's merged aircraft table.
type AircraftSource interface {
	Aircraft(dst []aircraft.Aircraft, includeStale bool) ([]aircraft.Aircraft, []aircraft.FeedStatus, time.Time)
}

// JamSource lists this node's own active jamming episodes (not ones
// gossiped from other nodes: those nodes publish their own markers).
type JamSource interface {
	ActiveJams() []Jam
}

// Options wires a Publisher. Config and Sender are required.
type Options struct {
	Config   ConfigSource
	Sender   Sender
	Position PositionSource
	Topology TopologySource
	Nodes    NodeStore
	Battery  BatterySource
	Aircraft AircraftSource
	Jams     JamSource
	// Now defaults to time.Now.
	Now func() time.Time
	Log zerolog.Logger
	// Callsign is this node's ATAK callsign and marker uid. It must match
	// the uid gpsd's camera marker uses.
	Callsign string
	// Hostname is this node's hostname as the mesh topology reports it.
	Hostname string
	// Platform is the board model shown in the marker's device info.
	Platform string
	// MaxPositionAge defaults to DefaultMaxPositionAge.
	MaxPositionAge time.Duration
	// CameraOwnsMarker is true on camera nodes: gpsd publishes the node
	// as a camera sensor under the same uid, so this feed skips its own
	// node marker there to avoid the two fighting over one map item.
	CameraOwnsMarker bool
}

// Publisher sends the feed. Only Run's goroutine touches its fields.
type Publisher struct {
	opts         Options
	links        map[string]struct{}
	jams         map[string]struct{}
	aircraftBuf  []aircraft.Aircraft
	mesh         meshSummary
	tick         uint64
	sendFailing  bool
	haveMeshRead bool
}

type meshSummary struct {
	selfIP    string
	links     []Link
	neighbors int
	nodes     int
	gateway   bool
}

// New returns a Publisher. It panics if Config or Sender is nil, which
// is a wiring bug.
func New(opts Options) *Publisher {
	if opts.Config == nil || opts.Sender == nil {
		panic("takfeed: Config and Sender are required")
	}

	if opts.Now == nil {
		opts.Now = time.Now
	}

	if opts.MaxPositionAge <= 0 {
		opts.MaxPositionAge = DefaultMaxPositionAge
	}

	return &Publisher{
		opts:  opts,
		links: make(map[string]struct{}, 8),
		jams:  make(map[string]struct{}, 4),
	}
}

// Run publishes immediately and then every tak.interval until ctx ends.
func (p *Publisher) Run(ctx context.Context) {
	interval := p.opts.Config.GetTAK().Interval

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	p.PublishOnce(ctx)

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			p.PublishOnce(ctx)

			if next := p.opts.Config.GetTAK().Interval; next != interval {
				interval = next
				ticker.Reset(interval)
			}
		}
	}
}

// PublishOnce runs one tick. Run calls it; tests call it directly.
func (p *Publisher) PublishOnce(ctx context.Context) {
	cfg := p.opts.Config.GetTAK()
	now := p.opts.Now()
	tick := p.tick
	p.tick++

	if !cfg.Enable {
		// Pull anything we put up so a disabled feed doesn't leave
		// jammers or links hanging on the map until they go stale.
		p.retractAll(now)

		return
	}

	pos, havePos := p.position(now)

	if tick%linkEvery == 0 {
		p.refreshMesh(ctx, pos, havePos)
	}

	if havePos && !p.opts.CameraOwnsMarker {
		p.send(NodeEvent(p.nodeInfo(cfg, pos), now))
	}

	if tick%linkEvery == 0 {
		p.publishLinks(cfg, now, havePos)
	}

	if cfg.AircraftEnable {
		p.publishAircraft(cfg, now, pos, havePos)
	}

	p.publishJams(cfg, now, pos, havePos)
}

func (p *Publisher) position(now time.Time) (Position, bool) {
	if p.opts.Position == nil {
		return Position{}, false
	}

	pos, ok := p.opts.Position.Position()
	if !ok || (pos.Lat == 0 && pos.Lon == 0) {
		return Position{}, false
	}

	if !pos.Time.IsZero() && now.Sub(pos.Time) > p.opts.MaxPositionAge {
		return Position{}, false
	}

	return pos, true
}

func (p *Publisher) nodeInfo(cfg config.TAKConfig, pos Position) *NodeInfo {
	n := &NodeInfo{
		Callsign:  p.opts.Callsign,
		Role:      cfg.Role,
		Platform:  p.opts.Platform,
		Pos:       pos,
		IP:        p.mesh.selfIP,
		Neighbors: p.mesh.neighbors,
		MeshNodes: p.mesh.nodes,
		Gateway:   p.mesh.gateway,
		HaveMesh:  p.haveMeshRead,
	}

	if p.opts.Battery == nil {
		return n
	}

	st, err := p.opts.Battery.GetBatteryStatus()
	if err != nil || st == nil || !st.Present {
		return n
	}

	n.Battery = &Battery{Percent: st.ChargePercent, Volts: st.VoltageVolts}

	return n
}

// refreshMesh reads the topology and gossiped node positions once per
// link period and keeps the summary the node marker and links use.
func (p *Publisher) refreshMesh(ctx context.Context, pos Position, havePos bool) {
	if p.opts.Topology == nil {
		return
	}

	resp, err := p.opts.Topology.GetMeshTopology(ctx, &emptypb.Empty{})
	if err != nil || resp.GetTopology() == nil {
		p.opts.Log.Debug().Err(err).Msg("ATAK feed: mesh topology unavailable")

		return
	}

	var nodes []models.MeshNode

	if p.opts.Nodes != nil {
		nodes, err = p.opts.Nodes.ListMeshNodes(ctx)
		if err != nil {
			p.opts.Log.Debug().Err(err).Msg("ATAK feed: mesh node list unavailable")
		}
	}

	p.mesh = summarizeMesh(resp.GetTopology(), nodes, p.opts.Hostname, p.opts.Callsign, pos, havePos)
	p.haveMeshRead = true
}

func summarizeMesh(
	topo *meshtopov1.MeshTopology,
	nodes []models.MeshNode,
	hostname, callsign string,
	pos Position,
	havePos bool,
) meshSummary {
	s := meshSummary{nodes: len(topo.GetNodes())}

	byMAC := make(map[string]*meshtopov1.MeshNode, len(topo.GetNodes()))

	var selfMAC string

	for _, n := range topo.GetNodes() {
		byMAC[n.GetMac()] = n

		if n.GetIsSelf() {
			selfMAC = n.GetMac()
			s.gateway = n.GetIsGateway()

			// Name ourselves the way peers see us, so both ends of a
			// link derive the same uid.
			if h := n.GetHostname(); h != "" {
				hostname = h
			}
		}
	}

	if selfMAC == "" {
		selfMAC = topo.GetSelfMac()
	}

	where := make(map[string]Endpoint, len(nodes))

	for i := range nodes {
		n := &nodes[i]
		if n.Hostname == hostname {
			s.selfIP = n.IpAddr
		}

		if lat, lon, ok := validLatLon(n.Latitude, n.Longitude); ok {
			where[n.Hostname] = Endpoint{Name: n.Hostname, Lat: lat, Lon: lon}
		}
	}

	self := Endpoint{Name: callsign, Lat: pos.Lat, Lon: pos.Lon}

	for _, e := range topo.GetEdges() {
		peerMAC := ""

		switch selfMAC {
		case e.GetFromMac():
			peerMAC = e.GetToMac()
		case e.GetToMac():
			peerMAC = e.GetFromMac()
		default:
			continue
		}

		s.neighbors++

		peer, ok := byMAC[peerMAC]
		if !ok || !havePos {
			continue
		}

		end, ok := where[peer.GetHostname()]
		if !ok || len(s.links) >= maxTracked {
			continue
		}

		s.links = append(s.links, Link{
			UID:       linkUID(hostname, peer.GetHostname()),
			Algorithm: topo.GetAlgorithm(),
			A:         self,
			B:         end,
			Metric:    e.GetMetric(),
			BLOS:      e.GetBlos(),
		})
	}

	return s
}

// linkUID is the same on both ends of a link, so the two nodes that
// publish it update one line in ATAK.
func linkUID(a, b string) string {
	if b < a {
		a, b = b, a
	}

	return "manet-link-" + a + "-" + b
}

func validLatLon(lat, lon sql.NullFloat64) (float64, float64, bool) {
	if !lat.Valid || !lon.Valid || (lat.Float64 == 0 && lon.Float64 == 0) {
		return 0, 0, false
	}

	if math.Abs(lat.Float64) > 90 || math.Abs(lon.Float64) > 180 {
		return 0, 0, false
	}

	return lat.Float64, lon.Float64, true
}

func (p *Publisher) publishLinks(cfg config.TAKConfig, now time.Time, havePos bool) {
	current := p.mesh.links
	if !cfg.Links || !havePos {
		current = nil
	}

	stale := cfg.Interval * linkEvery * 3

	seen := make(map[string]struct{}, len(current))

	for i := range current {
		seen[current[i].UID] = struct{}{}
		p.links[current[i].UID] = struct{}{}
		p.send(LinkEvent(&current[i], now, stale))
	}

	p.retractMissing(p.links, seen, now)
}

func (p *Publisher) publishAircraft(cfg config.TAKConfig, now time.Time, pos Position, havePos bool) {
	if p.opts.Aircraft == nil || cfg.AircraftMax == 0 {
		return
	}

	list, _, _ := p.opts.Aircraft.Aircraft(p.aircraftBuf[:0], false)
	p.aircraftBuf = list

	picked := pickAircraft(list, cfg, pos, havePos)
	for i := range picked {
		p.send(AircraftEvent(&picked[i], p.opts.Callsign, now))
	}
}

// pickAircraft keeps positioned aircraft inside the range limit, nearest
// first (newest first without a fix), up to the per-tick cap. It sorts
// and truncates list in place.
func pickAircraft(list []aircraft.Aircraft, cfg config.TAKConfig, pos Position, havePos bool) []aircraft.Aircraft {
	out := list[:0]

	for i := range list {
		if !list[i].HasPos || list[i].Stale {
			continue
		}

		if havePos && cfg.AircraftRangeKm > 0 && distanceKm(pos.Lat, pos.Lon, list[i].Lat, list[i].Lon) > cfg.AircraftRangeKm {
			continue
		}

		out = append(out, list[i])
	}

	if havePos {
		slices.SortFunc(out, func(a, b aircraft.Aircraft) int {
			return cmp.Compare(distanceKm(pos.Lat, pos.Lon, a.Lat, a.Lon), distanceKm(pos.Lat, pos.Lon, b.Lat, b.Lon))
		})
	} else {
		slices.SortFunc(out, func(a, b aircraft.Aircraft) int { return b.LastSeen.Compare(a.LastSeen) })
	}

	return out[:min(len(out), cfg.AircraftMax)]
}

func (p *Publisher) publishJams(cfg config.TAKConfig, now time.Time, pos Position, havePos bool) {
	var active []Jam

	if p.opts.Jams != nil && cfg.Jamming && havePos {
		active = p.opts.Jams.ActiveJams()
	}

	stale := cfg.Interval * 3
	seen := make(map[string]struct{}, len(active))

	for i := range active {
		uid := JamUID(p.opts.Callsign, active[i].ID)
		if _, tracked := p.jams[uid]; !tracked && len(p.jams) >= maxTracked {
			continue
		}

		seen[uid] = struct{}{}
		p.jams[uid] = struct{}{}
		p.send(JamEvent(&active[i], p.opts.Callsign, pos, now, stale))
	}

	p.retractMissing(p.jams, seen, now)
}

// retractMissing deletes from ATAK every tracked uid not in seen.
func (p *Publisher) retractMissing(tracked, seen map[string]struct{}, now time.Time) {
	for uid := range tracked {
		if _, ok := seen[uid]; ok {
			continue
		}

		p.send(DeleteEvent(uid, now))
		delete(tracked, uid)
	}
}

func (p *Publisher) retractAll(now time.Time) {
	p.retractMissing(p.links, nil, now)
	p.retractMissing(p.jams, nil, now)
}

func (p *Publisher) send(msg *cotproto.TakMessage) {
	data, err := cot.MakeProtoMeshPacketV1(msg)
	if err == nil {
		err = p.opts.Sender.Send(data)
	}

	if err != nil {
		if !p.sendFailing {
			p.opts.Log.Warn().Err(err).Str("uid", msg.GetCotEvent().GetUid()).Msg("ATAK feed: send failed; further failures logged at debug")
		} else {
			p.opts.Log.Debug().Err(err).Str("uid", msg.GetCotEvent().GetUid()).Msg("ATAK feed: send failed")
		}

		p.sendFailing = true

		return
	}

	if p.sendFailing {
		p.opts.Log.Info().Msg("ATAK feed: sending again")
	}

	p.sendFailing = false
}

// distanceKm is the great-circle distance between two points.
func distanceKm(lat1, lon1, lat2, lon2 float64) float64 {
	const rad = math.Pi / 180

	dLat := (lat2 - lat1) * rad
	dLon := (lon2 - lon1) * rad
	a := math.Sin(dLat/2)*math.Sin(dLat/2) +
		math.Cos(lat1*rad)*math.Cos(lat2*rad)*math.Sin(dLon/2)*math.Sin(dLon/2)

	return 2 * earthRadiusKm * math.Asin(math.Min(1, math.Sqrt(a)))
}
