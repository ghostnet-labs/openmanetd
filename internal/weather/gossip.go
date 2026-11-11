package weather

import (
	"net"
	"strings"
	"time"

	weatherv1 "github.com/openmanet/openmanetd/internal/api/openmanet/weather/v1"
)

// maxRemote bounds the remote stations kept; it matches the jam report
// and chanmig peer bounds.
const maxRemote = 256

// GossipRecord is one alfred record: the publishing node's MAC and its
// payload.
type GossipRecord struct {
	Source net.HardwareAddr
	Data   []byte
}

// Gossip is the alfred surface the Monitor needs, bound to the weather
// data type. The daemon adapts *alfred.Client to it.
type Gossip interface {
	Publish(payload []byte) error
	Fetch() ([]GossipRecord, error)
}

// remoteReport is the latest report heard from one node. changed is when
// its collected_at last changed on this node's clock, so ages and expiry
// do not depend on the publisher's clock.
type remoteReport struct {
	changed   time.Time
	report    *weatherv1.WeatherReport
	collected int64
}

// age is how old the remote reading is: time since the report changed
// here, plus how old the reading already was when the publisher built
// the report (both differences on one clock). -1 without a reading.
func (r *remoteReport) age(now time.Time) int32 {
	obs := r.report.GetReading().GetObservedAt()
	if obs == nil {
		return -1
	}

	lag := r.report.GetCollectedAt().AsTime().Sub(obs.AsTime())

	return seconds(now.Sub(r.changed) + max(lag, 0))
}

// GossipOnce publishes this node's report (when it has a sensor) and
// refreshes the remote ones. Errors are counted and logged; the next tick
// retries.
func (m *Monitor) GossipOnce() {
	g := m.opts.Gossip
	if g == nil {
		return
	}

	if local := m.Local(); local != nil {
		payload, err := local.MarshalVT()
		if err == nil {
			err = g.Publish(payload)
		}

		if err != nil {
			m.opts.Log.Warn().Err(err).Msg("weather: publish report failed")
			m.mu.Lock()
			m.stats.PublishErrorsTotal++
			m.mu.Unlock()
		}
	}

	recs, err := g.Fetch()
	if err != nil {
		m.opts.Log.Warn().Err(err).Msg("weather: fetch reports failed")
		m.mu.Lock()
		m.stats.FetchErrorsTotal++
		m.mu.Unlock()

		return
	}

	m.ingest(recs)
}

// ingest decodes fetched records, skips this node's own report and
// expires stations that stopped changing.
func (m *Monitor) ingest(recs []GossipRecord) {
	self, _ := m.identity()
	now := m.opts.Now()

	decoded := make([]*weatherv1.WeatherReport, 0, len(recs))
	bad := 0

	for _, r := range recs {
		rep := &weatherv1.WeatherReport{}
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

	m.mu.Lock()
	defer m.mu.Unlock()

	m.stats.DecodeErrorsTotal += uint64(bad) //nolint:gosec // bad <= len(recs)

	for _, rep := range decoded {
		key := rep.GetPrimaryMac()
		collected := rep.GetCollectedAt().AsTime().UnixNano()

		cur, ok := m.remote[key]
		if !ok {
			if len(m.remote) >= maxRemote {
				continue
			}

			m.remote[key] = &remoteReport{report: rep, collected: collected, changed: now}

			continue
		}

		if collected != cur.collected {
			cur.changed = now
			cur.collected = collected
		}

		cur.report = rep
	}

	for key, r := range m.remote {
		if now.Sub(r.changed) > m.opts.Expiry {
			delete(m.remote, key)
		}
	}
}
