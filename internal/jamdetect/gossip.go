package jamdetect

import (
	"net"
	"slices"
	"strings"
	"time"

	jamv1 "github.com/openmanet/openmanetd/internal/api/openmanet/jamdetect/v1"
	"google.golang.org/protobuf/proto"
)

// maxRemote bounds the remote reports kept; it matches the chanmig peer
// table bound.
const maxRemote = 256

// GossipRecord is one alfred record: the publishing node's MAC and its
// payload.
type GossipRecord struct {
	Source net.HardwareAddr
	Data   []byte
}

// Gossip is the alfred surface the detector needs, bound to the jam
// report data type. The daemon adapts *alfred.Client to it.
type Gossip interface {
	Publish(payload []byte) error
	Fetch() ([]GossipRecord, error)
}

// remoteReport is the latest report heard from one node. changed is when
// its collected_at last changed, measured on this node's clock, so expiry
// does not depend on the publisher's clock.
type remoteReport struct {
	changed   time.Time
	report    *jamv1.JamReport
	collected int64
}

// GossipOnce publishes this node's report and refreshes the remote ones.
// Errors are counted and logged; the next tick retries.
func (d *Detector) GossipOnce() {
	g := d.opts.Gossip
	if g == nil {
		return
	}

	payload, err := d.Report().MarshalVT()
	if err == nil {
		err = g.Publish(payload)
	}

	if err != nil {
		d.opts.Log.Warn().Err(err).Msg("jamdetect: publish report failed")
		d.mu.Lock()
		d.stats.GossipPublishErrorsTotal++
		d.mu.Unlock()
	}

	recs, err := g.Fetch()
	if err != nil {
		d.opts.Log.Warn().Err(err).Msg("jamdetect: fetch reports failed")
		d.mu.Lock()
		d.stats.GossipFetchErrorsTotal++
		d.mu.Unlock()

		return
	}

	d.ingest(recs)
}

// ingest decodes fetched records, skips this node's own report and
// expires reports that stopped changing.
func (d *Detector) ingest(recs []GossipRecord) {
	self, _ := d.identity()
	now := d.now()

	decoded := make([]*jamv1.JamReport, 0, len(recs))
	bad := 0

	for _, r := range recs {
		rep := &jamv1.JamReport{}
		if err := rep.UnmarshalVT(r.Data); err != nil {
			bad++

			continue
		}

		rep.PrimaryMac = strings.ToLower(rep.GetPrimaryMac())
		if rep.GetPrimaryMac() == "" && len(r.Source) > 0 {
			rep.PrimaryMac = strings.ToLower(r.Source.String())
		}

		if rep.GetPrimaryMac() == "" || (self != "" && rep.GetPrimaryMac() == self) {
			continue
		}

		decoded = append(decoded, rep)
	}

	d.mu.Lock()
	defer d.mu.Unlock()

	d.stats.GossipDecodeErrorsTotal += uint64(bad) //nolint:gosec // bad <= len(recs)

	for _, rep := range decoded {
		key := rep.GetPrimaryMac()
		collected := rep.GetCollectedAt().AsTime().UnixNano()

		cur, ok := d.remote[key]
		if !ok {
			if len(d.remote) >= maxRemote {
				continue
			}

			d.remote[key] = &remoteReport{report: rep, collected: collected, changed: now}

			continue
		}

		if collected != cur.collected {
			cur.changed = now
			cur.collected = collected
		}

		cur.report = rep
	}

	for key, r := range d.remote {
		if now.Sub(r.changed) > d.opts.GossipExpiry {
			delete(d.remote, key)
		}
	}
}

// Remote returns the latest report of every other node, sorted by
// hostname then MAC. The reports are copies.
func (d *Detector) Remote() []*jamv1.JamReport {
	d.mu.Lock()
	defer d.mu.Unlock()

	out := make([]*jamv1.JamReport, 0, len(d.remote))
	for _, r := range d.remote {
		cp, _ := proto.Clone(r.report).(*jamv1.JamReport)
		out = append(out, cp)
	}

	slices.SortFunc(out, func(a, b *jamv1.JamReport) int {
		if c := strings.Compare(a.GetHostname(), b.GetHostname()); c != 0 {
			return c
		}

		return strings.Compare(a.GetPrimaryMac(), b.GetPrimaryMac())
	})

	return out
}

func (d *Detector) identity() (primaryMAC, hostname string) {
	if d.opts.Identity == nil {
		return "", ""
	}

	mac, host := d.opts.Identity()

	return strings.ToLower(mac), host
}
