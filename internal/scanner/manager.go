// Package scanner ties the node's aircraft receivers and its P25 site
// monitor together: it polls dump1090/skyaware978 aircraft.json, checks
// which receivers are plugged in and who owns them, picks the nearest P25
// trunked site to the GPS fix (with hysteresis), centers the wide tuner on
// it, steers the control-channel decoder and keeps a bounded talkgroup
// history.
//
// All work runs on one goroutine started by Enable and stopped by Disable;
// a disabled manager owns no goroutines.
package scanner

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/openmanet/openmanetd/internal/config"
	"github.com/openmanet/openmanetd/internal/gpsd"
	"github.com/openmanet/openmanetd/internal/scanner/aircraft"
	"github.com/openmanet/openmanetd/internal/scanner/receivers"
	"github.com/openmanet/openmanetd/internal/scanner/sites"
	"github.com/rs/zerolog"
)

// Loop timing.
const (
	DefaultPollInterval     = time.Second
	DefaultReceiverInterval = 5 * time.Second
	// tuneRetry is how long after a failed Tune the manager retries.
	tuneRetry = 10 * time.Second
	// stopTuneTimeout bounds the zero-plan Tune sent on shutdown.
	stopTuneTimeout = 2 * time.Second
	// maxSubscribers bounds concurrent event streams.
	maxSubscribers = 8
	// subscriberBuffer is each stream's event queue: enough for a burst of
	// grant updates between two client reads; a slower client loses events
	// rather than stalling the manager.
	subscriberBuffer = 32
)

// ErrTooManySubscribers is returned by Subscribe when maxSubscribers
// streams are open.
var ErrTooManySubscribers = errors.New("too many scanner event streams")

// PositionSource is the GPS fix provider (*gpsd.GPSService).
type PositionSource interface {
	GetPosition() gpsd.PositionReport
}

// Options are the manager's dependencies. Nil fields fall back to
// production defaults, except Position and Monitor, which stay absent.
type Options struct {
	Log zerolog.Logger
	// Position is the GPS fix; nil means no position source.
	Position PositionSource
	// Monitor is the P25 control-channel decoder; nil means none is wired.
	Monitor ControlChannelMonitor
	// UCI reads dump1090/dump978-fa receiver ownership; nil skips it.
	UCI receivers.UCIGetter
	// RootFS resolves the aircraft.json paths (absolute paths are taken
	// relative to its root). Defaults to os.DirFS("/").
	RootFS fs.FS
	// SysFS is rooted at /sys/bus/usb/devices. Defaults to
	// os.DirFS(receivers.DefaultSysfsRoot).
	SysFS fs.FS
	// Now is the clock; defaults to time.Now.
	Now func() time.Time
	// Sites is the site table; defaults to sites.Table().
	Sites []sites.Site
	// PollInterval and ReceiverInterval default to DefaultPollInterval and
	// DefaultReceiverInterval.
	PollInterval     time.Duration
	ReceiverInterval time.Duration
}

// Manager is the scanner subsystem.
type Manager struct { //nolint:govet // fieldalignment: lifeMu and mu stay above the fields they guard per .claude/rules/concurrency.md
	log     zerolog.Logger
	gps     PositionSource
	monitor ControlChannelMonitor
	uci     receivers.UCIGetter
	rootFS  fs.FS
	sysFS   fs.FS
	now     func() time.Time
	tracker *aircraft.Tracker
	kick    chan struct{}
	// sites overrides the embedded table (tests); nil decodes the
	// embedded table lazily on first use.
	sites    []sites.Site
	poll     time.Duration
	recvPoll time.Duration

	// run-goroutine state, touched only by run and its helpers.
	selector    sites.Selector
	lastRecv    time.Time
	tuneFailed  time.Time
	tunedKey    string
	needsRetune bool

	// tableOnce lazily decodes the embedded site table when sites is nil.
	tableOnce sync.Once
	table     []sites.Site
	tableErr  error

	lifeMu sync.Mutex // protects cancel and done
	cancel context.CancelFunc
	done   chan struct{}

	mu          sync.Mutex // protects the fields below
	cfg         config.ScannerConfig
	enabled     bool
	receivers   []receivers.Receiver
	devices     []receivers.Device
	claims      receivers.Claims
	sysErr      error
	position    Position
	lastFix     Position
	selection   *Selection
	control     ControlStatus
	lastEvent   time.Time
	tunedAt     time.Time
	grants      *history
	subscribers map[chan Event]struct{}
}

// NewManager builds a manager for cfg. It starts nothing; call Enable (or
// Apply with Enable true).
func NewManager(opts Options, cfg config.ScannerConfig) *Manager {
	m := &Manager{
		log:         opts.Log,
		gps:         opts.Position,
		monitor:     opts.Monitor,
		uci:         opts.UCI,
		rootFS:      opts.RootFS,
		sysFS:       opts.SysFS,
		now:         opts.Now,
		sites:       opts.Sites,
		poll:        opts.PollInterval,
		recvPoll:    opts.ReceiverInterval,
		kick:        make(chan struct{}, 1),
		cfg:         cfg,
		grants:      newHistory(cmpOr(cfg.TalkgroupHistory, config.DefaultScannerTalkgroupHistory)),
		subscribers: make(map[chan Event]struct{}, maxSubscribers),
	}

	if m.rootFS == nil {
		m.rootFS = os.DirFS("/")
	}

	if m.sysFS == nil {
		m.sysFS = os.DirFS(receivers.DefaultSysfsRoot)
	}

	if m.now == nil {
		m.now = time.Now
	}

	if m.poll <= 0 {
		m.poll = DefaultPollInterval
	}

	if m.recvPoll <= 0 {
		m.recvPoll = DefaultReceiverInterval
	}

	m.tracker = aircraft.NewTracker([]aircraft.Feed{
		{Path: fsPath(cmpOr(cfg.Dump1090Path, config.DefaultScannerDump1090Path)), Source: aircraft.Source1090},
		{Path: fsPath(cmpOr(cfg.Skyaware978Path, config.DefaultScannerSkyaware978Path)), Source: aircraft.Source978},
	}, cfg.MaxAircraft, cfg.AircraftStaleAfter)

	m.control = ControlStatus{State: m.idleControlState()}

	return m
}

func cmpOr[T comparable](v, def T) T {
	var zero T
	if v == zero {
		return def
	}

	return v
}

// fsPath turns an absolute path into an fs.FS path.
func fsPath(p string) string { return strings.TrimPrefix(p, "/") }

// Sites returns the site table and any error decoding it.
func (m *Manager) Sites() ([]sites.Site, error) {
	if m.sites != nil {
		return m.sites, nil
	}

	m.tableOnce.Do(func() {
		m.table, m.tableErr = sites.Table()
	})

	if m.tableErr != nil {
		return nil, fmt.Errorf("scanner site table: %w", m.tableErr)
	}

	return m.table, nil
}

// Enabled reports whether the manager goroutine is running.
func (m *Manager) Enabled() bool {
	m.mu.Lock()
	defer m.mu.Unlock()

	return m.enabled
}

// Config returns the configuration in force.
func (m *Manager) Config() config.ScannerConfig {
	m.mu.Lock()
	defer m.mu.Unlock()

	cfg := m.cfg
	cfg.States = slices.Clone(cfg.States)

	return cfg
}

// Apply installs cfg and starts or stops the manager to match cfg.Enable.
func (m *Manager) Apply(cfg config.ScannerConfig) {
	m.mu.Lock()
	m.cfg = cfg
	m.cfg.States = slices.Clone(cfg.States)
	m.mu.Unlock()

	m.tracker.SetStaleAfter(cfg.AircraftStaleAfter)

	if cfg.Enable {
		m.Enable()

		select {
		case m.kick <- struct{}{}:
		default: // a re-evaluation is already pending
		}

		return
	}

	m.Disable()
}

// Enable starts the manager goroutine if it is not running.
func (m *Manager) Enable() {
	m.lifeMu.Lock()
	defer m.lifeMu.Unlock()

	if m.cancel != nil {
		return
	}

	ctx, cancel := context.WithCancel(context.Background())
	m.cancel, m.done = cancel, make(chan struct{})

	m.mu.Lock()
	m.enabled = true
	m.mu.Unlock()

	go m.run(ctx, m.done)

	m.log.Info().Msg("scanner manager started")
}

// Disable stops the manager goroutine and waits for it to exit. It is safe
// to call when not running.
func (m *Manager) Disable() {
	m.lifeMu.Lock()
	defer m.lifeMu.Unlock()

	if m.cancel == nil {
		return
	}

	m.cancel()
	<-m.done
	m.cancel, m.done = nil, nil

	m.log.Info().Msg("scanner manager stopped")
}

func (m *Manager) run(ctx context.Context, done chan struct{}) {
	defer close(done)
	defer m.shutdown()

	ticker := time.NewTicker(m.poll)
	defer ticker.Stop()

	var events <-chan any
	if m.monitor != nil {
		events = m.monitor.Events()
	}

	m.tick(ctx, m.now())

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			m.tick(ctx, m.now())
		case <-m.kick:
			m.tick(ctx, m.now())
		case ev, ok := <-events:
			if !ok {
				events = nil

				m.decoderStopped()

				continue
			}

			m.handleEvent(ev, m.now())
		}
	}
}

// shutdown stops the decoder and clears runtime state.
func (m *Manager) shutdown() {
	if m.monitor != nil && m.tunedKey != "" {
		ctx, cancel := context.WithTimeout(context.Background(), stopTuneTimeout)
		if err := m.monitor.Tune(ctx, TunePlan{}); err != nil {
			m.log.Warn().Err(err).Msg("scanner: stopping control-channel decoder failed")
		}

		cancel()
	}

	m.selector.Reset()
	m.tunedKey, m.needsRetune, m.lastRecv, m.tuneFailed = "", false, time.Time{}, time.Time{}

	m.mu.Lock()
	defer m.mu.Unlock()

	m.enabled = false
	m.selection = nil
	m.receivers = nil
	m.position = Position{}
	m.control = ControlStatus{State: m.idleControlState()}
	m.lastEvent, m.tunedAt = time.Time{}, time.Time{}

	for ch := range m.subscribers {
		close(ch)
		delete(m.subscribers, ch)
	}
}

// tick runs one poll cycle.
func (m *Manager) tick(ctx context.Context, now time.Time) {
	m.tracker.Poll(m.rootFS, now)

	m.mu.Lock()
	cfg := m.cfg
	m.mu.Unlock()

	if m.lastRecv.IsZero() || now.Sub(m.lastRecv) >= m.recvPoll {
		m.lastRecv = now
		m.refreshDevices()
	}

	m.updatePosition(cfg, now)
	m.updateSelection(ctx, cfg, now)
	m.refreshReceivers(cfg, now)
}

func (m *Manager) refreshDevices() {
	devices, err := receivers.Scan(m.sysFS)
	claims := receivers.ReadClaims(m.uci, "", "")

	m.mu.Lock()
	defer m.mu.Unlock()

	m.devices, m.sysErr = devices, err
	m.claims = claims
}

func (m *Manager) updatePosition(cfg config.ScannerConfig, now time.Time) {
	staleAfter := cmpOr(cfg.PositionStaleAfter, config.DefaultScannerPositionStaleAfter)

	var p Position

	if m.gps != nil {
		r := m.gps.GetPosition()
		if r.Valid && r.Mode >= 2 {
			ts := r.Timestamp
			if ts.IsZero() {
				ts = now
			}

			p = Position{Source: PositionGPSD, Lat: r.Latitude, Lon: r.Longitude, FixTime: ts}
		}
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	switch {
	case p.Source == PositionGPSD:
		m.lastFix = p
	case m.lastFix.Source != PositionNone:
		p = m.lastFix
		p.Source = PositionLastKnown
	}

	p.Stale = p.Source == PositionNone || now.Sub(p.FixTime) > staleAfter
	if p.Source == PositionGPSD && p.Stale {
		p.Source = PositionLastKnown
	}

	m.position = p
}

func (m *Manager) updateSelection(ctx context.Context, cfg config.ScannerConfig, now time.Time) {
	m.mu.Lock()
	pos := m.position
	m.mu.Unlock()

	m.selector.HysteresisKm = cfg.HysteresisKm

	all, err := m.Sites()
	if err != nil {
		m.log.Error().Err(err).Msg("scanner: no site table; site selection disabled")
	}

	if cfg.Mode == config.ScannerModeManual {
		site, ok := sites.ByKey(all, cfg.ManualSite)
		if ok {
			m.selector.Pin(site)
		} else {
			m.selector.Reset()
		}
	} else {
		fix := sites.Fix{Lat: pos.Lat, Lon: pos.Lon, Valid: pos.Source != PositionNone, Fresh: !pos.Stale}
		m.selector.Update(all, fix, sites.StateFilter(cfg.States))
	}

	site, reason := m.selector.Current()
	key := ""

	if site != nil {
		key = site.Key
	}

	changed := key != m.tunedKey || m.needsRetune
	if changed && !m.tuneFailed.IsZero() && now.Sub(m.tuneFailed) < tuneRetry && key == m.tunedKey {
		changed = false
	}

	m.mu.Lock()
	sel := m.buildSelectionLocked(site, reason, cfg, pos, now)
	m.mu.Unlock()

	if !changed {
		return
	}

	m.retune(ctx, sel, now)
}

// buildSelectionLocked refreshes m.selection for site (nil clears it),
// keeping selected_at across ticks for the same site.
func (m *Manager) buildSelectionLocked(site *sites.Site, reason sites.Reason, cfg config.ScannerConfig,
	pos Position, now time.Time,
) *Selection {
	if site == nil {
		m.selection = nil

		return nil
	}

	sel := m.selection
	if sel == nil || sel.Site.Key != site.Key {
		plan := sites.PlanTuner(site, cmpOr(cfg.WideUsableHz, config.DefaultScannerWideUsableHz))
		sel = &Selection{Site: site, Plan: plan, SelectedAt: now}
	} else {
		cp := *sel
		sel = &cp
	}

	sel.Reason = reason
	sel.Mode = cfg.Mode

	sel.DistanceKm = 0
	if pos.Source != PositionNone {
		sel.DistanceKm = sites.DistanceKm(pos.Lat, pos.Lon, site.Lat, site.Lon)
	}

	m.selection = sel

	return sel
}

// retune points the decoder at sel (or stops it when sel is nil) and
// publishes the site change. It runs on the manager goroutine without
// holding mu across Tune.
func (m *Manager) retune(ctx context.Context, sel *Selection, now time.Time) {
	newKey := ""
	if sel != nil {
		newKey = sel.Site.Key
	}

	siteChanged := newKey != m.tunedKey

	if m.monitor == nil {
		m.tunedKey, m.needsRetune = newKey, false
		m.publishSite(sel, now, siteChanged)

		return
	}

	plan := TunePlan{}
	if sel != nil {
		plan = TunePlan{
			SiteKey:   sel.Site.Key,
			ControlHz: sel.Plan.CoveredControl(sel.Site),
			VoiceHz:   coveredVoice(sel),
			CenterHz:  sel.Plan.CenterHz,
			UsableHz:  sel.Plan.UsableHz,
		}
	}

	err := m.monitor.Tune(ctx, plan)

	m.mu.Lock()
	m.tunedAt = now

	switch {
	case err != nil:
		m.control = ControlStatus{State: ControlError, FrequencyHz: plan.firstControl(), Detail: err.Error()}
		m.tuneFailed, m.needsRetune = now, true
	case sel == nil:
		m.control = ControlStatus{State: ControlIdle}
		m.tuneFailed, m.needsRetune = time.Time{}, false
	default:
		m.control = ControlStatus{State: ControlHunting, FrequencyHz: plan.firstControl()}
		m.tuneFailed, m.needsRetune = time.Time{}, false
	}

	cc := m.control
	m.mu.Unlock()

	if err != nil {
		m.log.Warn().Err(err).Str("site", newKey).Msg("scanner: tuning control-channel decoder failed")
	}

	m.tunedKey = newKey
	m.publishSite(sel, now, siteChanged)
	m.publish(Event{Time: now, Kind: EventControl, Control: cc})
}

func (p *TunePlan) firstControl() uint64 {
	if len(p.ControlHz) == 0 {
		return 0
	}

	return p.ControlHz[0]
}

func coveredVoice(sel *Selection) []uint64 {
	out := make([]uint64, 0, len(sel.Site.VoiceHz))

	for _, f := range sel.Site.VoiceHz {
		if _, ok := slices.BinarySearch(sel.Plan.Covered, f); ok {
			out = append(out, f)
		}
	}

	return out
}

func (m *Manager) publishSite(sel *Selection, now time.Time, changed bool) {
	if !changed {
		return
	}

	if sel != nil {
		m.log.Info().Str("site", sel.Site.Key).Str("name", sel.Site.Name).Float64("distance_km", sel.DistanceKm).
			Msg("scanner: selected P25 site")
	}

	var cp *Selection

	if sel != nil {
		c := *sel
		cp = &c
	}

	m.publish(Event{Time: now, Kind: EventSite, Site: cp})
}

// decoderStopped handles the monitor closing its event channel.
func (m *Manager) decoderStopped() {
	m.mu.Lock()
	m.control = ControlStatus{State: ControlError, FrequencyHz: m.control.FrequencyHz, Detail: "decoder stopped"}
	cc := m.control
	m.mu.Unlock()

	m.publish(Event{Time: m.now(), Kind: EventControl, Control: cc})
}

// handleEvent applies one decoder event.
func (m *Manager) handleEvent(ev any, now time.Time) {
	switch ev.(type) {
	case SyncState, SiteStatus, AdjacentSite, VoiceGrant:
	default:
		return // not part of the decoder contract
	}

	m.mu.Lock()

	if m.tunedKey == "" {
		m.mu.Unlock()

		return // late event after a stop: ignore
	}

	m.lastEvent = now
	prevState := m.control.State

	var out Event

	switch e := ev.(type) {
	case SyncState:
		if m.control.State == ControlHunting || m.control.State == ControlLocked {
			m.control.State = ControlHunting
			if e.Locked {
				m.control.State = ControlLocked
			}
		}
	case SiteStatus:
		m.control.LastDecode = now
		m.control.WACN, m.control.SystemID, m.control.RFSS = e.WACN, e.SystemID, e.RFSS
		m.control.SiteID, m.control.NAC = e.Site, e.NAC

		if e.ControlChannelHz != 0 {
			m.control.FrequencyHz = e.ControlChannelHz
		}
	case AdjacentSite:
		m.control.LastDecode = now
	case VoiceGrant:
		m.control.LastDecode = now
		g := m.grants.add(e, m.tunedKey, now)
		out = Event{Time: now, Kind: EventGrant, Grant: g}
	}

	// Any decoded message implies sync.
	if !m.control.LastDecode.IsZero() && m.control.LastDecode.Equal(now) && m.control.State == ControlHunting {
		m.control.State = ControlLocked
	}

	cc := m.control
	m.mu.Unlock()

	if out.Kind != EventNone {
		m.publish(out)
	}

	if cc.State != prevState {
		m.publish(Event{Time: now, Kind: EventControl, Control: cc})
	}
}

func (m *Manager) refreshReceivers(cfg config.ScannerConfig, now time.Time) {
	feeds := m.tracker.Feeds(now)

	m.mu.Lock()
	defer m.mu.Unlock()

	claims := m.claims
	claims.Wide, claims.Scan = cfg.WideSerial, cfg.ScanSerial

	act := make(map[receivers.Role]receivers.Activity, 4)

	for _, f := range feeds {
		role, enabled, name := receivers.RoleADSB1090, claims.ADSBEnabled, "dump1090"
		if f.Source == aircraft.Source978 {
			role, enabled, name = receivers.RoleUAT978, claims.UATEnabled, "dump978-fa"
		}

		a := receivers.Activity{LastSample: f.Updated, Fresh: !f.Stale}

		switch {
		case f.Err != nil && !errors.Is(f.Err, fs.ErrNotExist):
			a.Err = f.Err
		case m.uci != nil && !enabled:
			a.Idle = name + " is disabled in UCI"
		case f.Err != nil:
			a.Idle = "aircraft.json not found (" + name + " not running?)"
		default:
			a.Idle = "aircraft.json not updated recently"
		}

		act[role] = a
	}

	decodeStale := cmpOr(cfg.DecodeStaleAfter, config.DefaultScannerDecodeStaleAfter)
	wide := receivers.Activity{LastSample: m.lastEvent}

	switch {
	case m.monitor == nil:
		wide.Idle = "control-channel decoder not wired"
	case m.selection == nil:
		wide.Idle = "no P25 site selected"
	case m.control.State == ControlError:
		wide.Err = errors.New(m.control.Detail)
	default:
		wide.Fresh = !m.lastEvent.IsZero() && now.Sub(m.lastEvent) <= decodeStale
		wide.Idle = "no decoder output yet"
	}

	act[receivers.RoleWide] = wide
	act[receivers.RoleScan] = receivers.Activity{Idle: "conventional/aviation scanning not wired yet"}

	m.receivers = receivers.Assess(claims, m.devices, act)
	if m.sysErr != nil {
		for i := range m.receivers {
			if m.receivers[i].State == receivers.StateMissing {
				m.receivers[i].Detail = "USB scan failed: " + m.sysErr.Error()
			}
		}
	}

	m.control.Stale = m.decodeStaleLocked(decodeStale, now)
}

func (m *Manager) decodeStaleLocked(decodeStale time.Duration, now time.Time) bool {
	if m.control.State != ControlHunting && m.control.State != ControlLocked {
		return false
	}

	ref := m.control.LastDecode
	if ref.IsZero() {
		ref = m.tunedAt
	}

	return now.Sub(ref) > decodeStale
}

func (m *Manager) idleControlState() ControlState {
	if m.monitor == nil {
		return ControlUnavailable
	}

	return ControlIdle
}

// publish fans ev out to subscribers without blocking; a full subscriber
// queue drops the event.
func (m *Manager) publish(ev Event) {
	m.mu.Lock()
	defer m.mu.Unlock()

	for ch := range m.subscribers {
		select {
		case ch <- ev:
		default:
		}
	}
}

// Subscribe opens an event stream. The channel is closed when the manager
// stops; call the returned cancel when done reading.
func (m *Manager) Subscribe() (<-chan Event, func(), error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if !m.enabled {
		return nil, nil, ErrDisabled
	}

	if len(m.subscribers) >= maxSubscribers {
		return nil, nil, ErrTooManySubscribers
	}

	ch := make(chan Event, subscriberBuffer)
	m.subscribers[ch] = struct{}{}

	cancel := func() {
		m.mu.Lock()
		defer m.mu.Unlock()

		if _, ok := m.subscribers[ch]; ok {
			delete(m.subscribers, ch)
			close(ch)
		}
	}

	return ch, cancel, nil
}

// ErrDisabled is returned by Subscribe when the manager is not running.
var ErrDisabled = errors.New("scanner is disabled")
