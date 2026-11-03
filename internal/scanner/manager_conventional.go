package scanner

import (
	"context"
	"slices"
	"strconv"
	"strings"

	"github.com/openmanet/openmanetd/internal/config"
	"github.com/openmanet/openmanetd/internal/scanner/receivers"
	"github.com/openmanet/openmanetd/internal/scanner/sites"
)

// Conventional detail strings.
const (
	convBusyDetail     = "scan receiver decodes the P25 control channel (no wide receiver serial configured)"
	convNoSerialDetail = "no scan receiver serial configured"
	convNoneDetail     = "no conventional channels selected"
)

// updateConventional rebuilds the conventional channel list when the
// position, state, radius or scan receiver ownership changed, and hands
// the plan to the conventional scanner. It runs on the manager goroutine.
func (m *Manager) updateConventional(ctx context.Context, cfg config.ScannerConfig) {
	ref := m.conventionalRef(cfg)

	if m.convList == nil || ref.changed(m.convRef) {
		m.convRef = ref
		m.convList = m.conventionalChannels(ref)
	}

	plan := ConventionalPlan{}
	detail := ""

	switch {
	case ref.busy:
		detail = convBusyDetail
	case ref.serial == "":
		detail = convNoSerialDetail
	case len(m.convList) == 0:
		detail = convNoneDetail
	default:
		plan = ConventionalPlan{Serial: ref.serial, Channels: m.convList, UsableHz: ref.usable}
	}

	m.mu.Lock()
	m.convChannels, m.convBusy, m.convDetail = m.convList, ref.busy, detail
	m.mu.Unlock()

	if m.conv == nil {
		return
	}

	key := planKey(&plan)
	if key == m.convKey {
		return
	}

	if plan.Serial != "" {
		// The P25 decoder may still be closing the scan receiver after a
		// zero plan; opening it twice fails with a USB claim error.
		if rel, ok := m.monitor.(ReceiverReleaser); ok {
			wctx, cancel := context.WithTimeout(ctx, releaseTimeout)
			err := rel.WaitReleased(wctx, plan.Serial)

			cancel()

			if err != nil {
				m.log.Debug().Err(err).Msg("scanner: scan receiver not released yet; conventional scan waits")

				return
			}
		}

		m.holder = holderConventional
	}

	if err := m.conv.Scan(plan); err != nil {
		m.log.Warn().Err(err).Msg("scanner: conventional scan plan rejected")

		m.mu.Lock()
		m.convDetail = err.Error()
		m.mu.Unlock()
	}

	m.convKey = key

	if plan.Serial == "" && m.holder == holderConventional {
		m.holder = holderNone
	}
}

// stopConventional takes the scan receiver back from the conventional
// scanner and waits (bounded) for its capture to exit.
func (m *Manager) stopConventional() {
	if m.conv == nil || m.convKey == "" {
		if m.holder == holderConventional {
			m.holder = holderNone
		}

		return
	}

	if err := m.conv.Scan(ConventionalPlan{}); err != nil {
		m.log.Warn().Err(err).Msg("scanner: stopping conventional scan failed")
	}

	ctx, cancel := context.WithTimeout(context.Background(), releaseTimeout)
	defer cancel()

	if err := m.conv.WaitIdle(ctx); err != nil {
		m.log.Warn().Err(err).Msg("scanner: conventional capture did not stop in time")
	}

	m.convKey = ""

	if m.holder == holderConventional {
		m.holder = holderNone
	}
}

func (m *Manager) conventionalRef(cfg config.ScannerConfig) convRef {
	m.mu.Lock()
	pos, sel := m.position, m.selection
	m.mu.Unlock()

	ref := convRef{
		serial: cfg.ScanSerial,
		radius: cmpOr(cfg.ConventionalRadiusKm, config.DefaultScannerConventionalRadiusKm),
		usable: cmpOr(cfg.RTLUsableHz, config.DefaultScannerRTLUsableHz),
		busy:   m.holder == holderP25,
	}

	switch {
	case pos.Source != PositionNone:
		ref.lat, ref.lon, ref.valid = pos.Lat, pos.Lon, true
	case sel != nil && sel.Mode == config.ScannerModeManual:
		ref.lat, ref.lon, ref.valid = sel.Site.Lat, sel.Site.Lon, true
	}

	if ref.valid {
		all, _ := m.Sites() // a table error is logged by updateSelection
		if near, ok := sites.Nearest(all, ref.lat, ref.lon, nil); ok {
			ref.state = near.Site.State
		}
	}

	return ref
}

// changed reports whether the channel list built for old is stale.
func (r convRef) changed(old convRef) bool {
	if r.state != old.state || r.serial != old.serial || r.radius != old.radius || r.usable != old.usable ||
		r.valid != old.valid || r.busy != old.busy {
		return true
	}

	return r.valid && sites.DistanceKm(r.lat, r.lon, old.lat, old.lon) >= convMoveKm
}

func (m *Manager) conventionalChannels(ref convRef) []ConventionalChannel {
	all, err := m.Channels()
	if err != nil {
		m.log.Error().Err(err).Msg("scanner: no conventional channel table")

		return []ConventionalChannel{}
	}

	near := sites.NearbyChannels(all, sites.ChannelQuery{
		State: ref.state, Lat: ref.lat, Lon: ref.lon, RadiusKm: ref.radius, Valid: ref.valid,
	})

	out := make([]ConventionalChannel, 0, len(near))
	for _, n := range near {
		out = append(out, ConventionalChannel(n))
	}

	return out
}

// planKey identifies a plan so an unchanged one is not re-sent.
func planKey(p *ConventionalPlan) string {
	if p.Serial == "" {
		return ""
	}

	var b strings.Builder

	b.Grow(16 + 11*len(p.Channels))
	b.WriteString(p.Serial)
	b.WriteByte('|')
	b.WriteString(strconv.FormatUint(p.UsableHz, 10))

	for _, c := range p.Channels {
		b.WriteByte('|')
		b.WriteString(c.Channel.Key)
	}

	return b.String()
}

// Conventional returns the conventional scanner status: the channel list
// in force with live and last-heard state.
func (m *Manager) Conventional() ConventionalStatus {
	m.mu.Lock()
	enabled, list, busy, detail := m.enabled, m.convChannels, m.convBusy, m.convDetail
	m.mu.Unlock()

	listed := func(state ConventionalState, detail string) ConventionalStatus {
		st := ConventionalStatus{State: state, Detail: detail, Channels: make([]ConventionalChannelStatus, 0, len(list))}
		for _, c := range list {
			st.Channels = append(st.Channels, ConventionalChannelStatus{ConventionalChannel: c})
		}

		return st
	}

	switch {
	case !enabled:
		return ConventionalStatus{State: ConventionalIdle}
	case m.conv == nil:
		return listed(ConventionalUnavailable, "conventional scanner not wired")
	case busy:
		return listed(ConventionalReceiverBusy, detail)
	}

	st := m.conv.Status()
	if st.State == ConventionalIdle && detail != "" {
		st.Detail = detail
	}

	return st
}

// SubscribeAudio streams demodulated audio of channelKey ("" for every
// channel). The stream (and its pin on the channel's window) ends when
// cancel is called or the manager stops.
func (m *Manager) SubscribeAudio(channelKey string) (<-chan AudioFrame, func(), error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if !m.enabled {
		return nil, nil, ErrDisabled
	}

	if m.conv == nil {
		return nil, nil, ErrConventionalUnavailable
	}

	if channelKey != "" && !slices.ContainsFunc(m.convChannels, func(c ConventionalChannel) bool {
		return c.Channel.Key == channelKey
	}) {
		return nil, nil, ErrUnknownChannel
	}

	ch, convCancel, err := m.conv.SubscribeAudio(channelKey)
	if err != nil {
		return nil, nil, err //nolint:wrapcheck // the scanner's sentinel (too many streams) is the caller's answer
	}

	id := m.audioNext
	m.audioNext++
	m.audioSubs[id] = convCancel

	cancel := func() {
		m.mu.Lock()
		delete(m.audioSubs, id)
		m.mu.Unlock()

		convCancel()
	}

	return ch, cancel, nil
}

// conventionalActivity maps the conventional scanner state onto the scan
// receiver's activity. It takes the scanner's lock, so it is called
// without m.mu held.
func (m *Manager) conventionalActivity() receivers.Activity {
	st := m.Conventional()

	switch st.State {
	case ConventionalScanning, ConventionalHolding:
		return receivers.Activity{LastSample: m.now(), Fresh: true}
	case ConventionalError:
		return receivers.Activity{Err: errorString(st.Detail)}
	case ConventionalUnavailable:
		return receivers.Activity{Idle: "conventional scanner unavailable: " + st.Detail}
	default:
		return receivers.Activity{Idle: cmpOr(st.Detail, "conventional scanner idle")}
	}
}

type errorString string

func (e errorString) Error() string { return string(e) }
