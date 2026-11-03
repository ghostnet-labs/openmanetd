package handlers

import (
	"context"
	"errors"
	"fmt"
	"time"

	"connectrpc.com/connect"
	scannerv1 "github.com/openmanet/openmanetd/internal/api/openmanet/scanner/v1"
	"github.com/openmanet/openmanetd/internal/config"
	"github.com/openmanet/openmanetd/internal/scanner"
	"github.com/openmanet/openmanetd/internal/scanner/aircraft"
	"github.com/openmanet/openmanetd/internal/scanner/receivers"
	"github.com/openmanet/openmanetd/internal/scanner/sites"
	"github.com/rs/zerolog"
	"google.golang.org/protobuf/types/known/durationpb"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// Scanner handler defaults.
const (
	defaultAircraftStreamInterval = time.Second
	defaultListSitesLimit         = 25
)

// ScannerManager is the subset of *scanner.Manager the handler uses.
type ScannerManager interface {
	Enabled() bool
	Status() scanner.Status
	Aircraft(dst []aircraft.Aircraft, includeStale bool) ([]aircraft.Aircraft, []aircraft.FeedStatus, time.Time)
	Sites() ([]sites.Site, error)
	Reference() scanner.Position
	Grants(limit int) ([]scanner.Grant, int)
	Subscribe() (<-chan scanner.Event, func(), error)
	Apply(cfg config.ScannerConfig)
}

// ScannerConfigStore reads and persists the scanner config section
// (*config.Config).
type ScannerConfigStore interface {
	GetScanner() config.ScannerConfig
	PersistScannerConfig(s config.ScannerSettings) error
}

// ScannerService implements scannerv1connect.ScannerServiceHandler.
type ScannerService struct {
	Log zerolog.Logger
	// Cfg persists the scanner section.
	Cfg ScannerConfigStore
	// Manager is the scanner manager. It is always constructed by the
	// daemon (it owns no goroutines while disabled); nil only in tests.
	Manager ScannerManager
}

// GetScannerStatus reports receivers, position, site and decoder state.
func (s *ScannerService) GetScannerStatus(
	_ context.Context,
	_ *scannerv1.GetScannerStatusRequest,
) (*scannerv1.GetScannerStatusResponse, error) {
	if s.Manager == nil {
		return &scannerv1.GetScannerStatusResponse{GeneratedAt: timestamppb.Now()}, nil
	}

	st := s.Manager.Status()
	res := &scannerv1.GetScannerStatusResponse{Enabled: st.Enabled, GeneratedAt: timestamppb.New(st.GeneratedAt)}

	if !st.Enabled {
		return res, nil
	}

	res.Receivers = make([]*scannerv1.Receiver, 0, len(st.Receivers))
	for i := range st.Receivers {
		res.Receivers = append(res.Receivers, scannerReceiverToProto(&st.Receivers[i]))
	}

	res.Position = scannerPositionToProto(st.Position)
	res.ControlChannel = scannerControlToProto(&st.Control)
	res.LastDecodeTime = scannerOptTimestamp(st.Control.LastDecode)
	res.PositionStale = st.Position.Stale
	res.SiteUnavailable = st.Selection == nil
	res.DecoderUnavailable = st.Control.State == scanner.ControlUnavailable
	res.DecodeStale = st.Control.Stale
	res.AircraftStale = st.AircraftStale

	if st.Selection != nil {
		res.SelectedSite = scannerSelectionToProto(st.Selection, st.Control.FrequencyHz)
	}

	return res, nil
}

// ListAircraft returns the merged aircraft table.
func (s *ScannerService) ListAircraft(
	_ context.Context,
	req *scannerv1.ListAircraftRequest,
) (*scannerv1.ListAircraftResponse, error) {
	if s.Manager == nil || !s.Manager.Enabled() {
		return &scannerv1.ListAircraftResponse{GeneratedAt: timestamppb.Now()}, nil
	}

	list, feeds, now := s.Manager.Aircraft(nil, req.GetIncludeStale())

	return &scannerv1.ListAircraftResponse{
		Enabled:     true,
		Aircraft:    scannerAircraftToProto(list, now),
		Feeds:       scannerFeedsToProto(feeds),
		GeneratedAt: timestamppb.New(now),
	}, nil
}

// StreamAircraft sends a snapshot every interval until the client leaves.
func (s *ScannerService) StreamAircraft(
	ctx context.Context,
	req *scannerv1.StreamAircraftRequest,
	stream *connect.ServerStream[scannerv1.StreamAircraftResponse],
) error {
	interval := defaultAircraftStreamInterval
	if req.GetInterval() != nil {
		interval = req.GetInterval().AsDuration()
	}

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	// The snapshot buffer is reused across sends; only the proto messages
	// are allocated per tick.
	var buf []aircraft.Aircraft

	for {
		res := &scannerv1.StreamAircraftResponse{GeneratedAt: timestamppb.Now()}

		if s.Manager != nil && s.Manager.Enabled() {
			var (
				feeds []aircraft.FeedStatus
				now   time.Time
			)

			buf, feeds, now = s.Manager.Aircraft(buf, req.GetIncludeStale())
			res.Enabled = true
			res.Aircraft = scannerAircraftToProto(buf, now)
			res.Feeds = scannerFeedsToProto(feeds)
			res.GeneratedAt = timestamppb.New(now)
		}

		if err := stream.Send(res); err != nil {
			return fmt.Errorf("send aircraft snapshot: %w", err)
		}

		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
	}
}

// ListSites ranks the embedded site table from a point. It works with the
// scanner disabled so the manual-site picker can use it.
func (s *ScannerService) ListSites(
	_ context.Context,
	req *scannerv1.ListSitesRequest,
) (*scannerv1.ListSitesResponse, error) {
	var (
		all []sites.Site
		err error
	)

	if s.Manager != nil {
		all, err = s.Manager.Sites()
	} else {
		all, err = sites.Table()
	}

	if err != nil {
		s.Log.Error().Err(err).Msg("Failed to load P25 site table")

		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("load site table: %w", err))
	}

	ref := scanner.Position{Stale: true}
	if req.Latitude != nil && req.Longitude != nil {
		ref = scanner.Position{Source: scanner.PositionManual, Lat: req.GetLatitude(), Lon: req.GetLongitude()}
	} else if s.Manager != nil {
		ref = s.Manager.Reference()
	}

	limit := int(req.GetLimit())
	if limit == 0 {
		limit = defaultListSitesLimit
	}

	filter := sites.StateFilter(req.GetStates())
	res := &scannerv1.ListSitesResponse{Reference: scannerPositionToProto(ref)}

	if ref.Source == scanner.PositionNone {
		res.Sites = make([]*scannerv1.Site, 0, min(limit, len(all)))

		total := 0

		for i := range all {
			if !filter.Allows(all[i].State) {
				continue
			}

			total++

			if len(res.Sites) < limit {
				res.Sites = append(res.Sites, scannerSiteToProto(&all[i], 0))
			}
		}

		res.Total = uint32(total)

		return res, nil
	}

	ranked, total := sites.Rank(all, ref.Lat, ref.Lon, filter, limit)
	res.Total = uint32(total)

	res.Sites = make([]*scannerv1.Site, 0, len(ranked))
	for _, r := range ranked {
		res.Sites = append(res.Sites, scannerSiteToProto(r.Site, r.DistanceKm))
	}

	return res, nil
}

// ListTalkgroupActivity returns recent grants newest first.
func (s *ScannerService) ListTalkgroupActivity(
	_ context.Context,
	req *scannerv1.ListTalkgroupActivityRequest,
) (*scannerv1.ListTalkgroupActivityResponse, error) {
	if s.Manager == nil {
		return &scannerv1.ListTalkgroupActivityResponse{}, nil
	}

	grants, capacity := s.Manager.Grants(int(req.GetLimit()))
	res := &scannerv1.ListTalkgroupActivityResponse{
		Enabled:         s.Manager.Enabled(),
		HistoryCapacity: uint32(capacity),
		Grants:          make([]*scannerv1.VoiceGrant, 0, len(grants)),
	}

	for i := range grants {
		res.Grants = append(res.Grants, scannerGrantToProto(&grants[i]))
	}

	return res, nil
}

// StreamScannerEvents relays manager events until the client leaves or the
// manager stops.
func (s *ScannerService) StreamScannerEvents(
	ctx context.Context,
	req *scannerv1.StreamScannerEventsRequest,
	stream *connect.ServerStream[scannerv1.StreamScannerEventsResponse],
) error {
	if s.Manager == nil {
		return connect.NewError(connect.CodeFailedPrecondition, scanner.ErrDisabled)
	}

	events, cancel, err := s.Manager.Subscribe()

	switch {
	case errors.Is(err, scanner.ErrTooManySubscribers):
		s.Log.Warn().Err(err).Msg("Refusing scanner event stream")

		return connect.NewError(connect.CodeResourceExhausted, err)
	case err != nil:
		return connect.NewError(connect.CodeFailedPrecondition, err)
	}

	defer cancel()

	if req.GetReplayRecent() {
		grants, _ := s.Manager.Grants(0)
		for i := len(grants) - 1; i >= 0; i-- {
			g := &grants[i]
			if err := stream.Send(&scannerv1.StreamScannerEventsResponse{
				Time:  timestamppb.New(g.Last),
				Event: &scannerv1.StreamScannerEventsResponse_VoiceGrant{VoiceGrant: scannerGrantToProto(g)},
			}); err != nil {
				return fmt.Errorf("send replayed grant: %w", err)
			}
		}
	}

	// The current decoder state goes first so the client gets an initial
	// state (and the response headers) without waiting for an event.
	st := s.Manager.Status()
	if err := stream.Send(&scannerv1.StreamScannerEventsResponse{
		Time:  scannerOptTimestamp(st.GeneratedAt),
		Event: &scannerv1.StreamScannerEventsResponse_ControlChannel{ControlChannel: scannerControlToProto(&st.Control)},
	}); err != nil {
		return fmt.Errorf("send initial scanner state: %w", err)
	}

	for {
		select {
		case <-ctx.Done():
			return nil
		case ev, ok := <-events:
			if !ok {
				return nil
			}

			msg := scannerEventToProto(&ev)
			if msg == nil {
				continue
			}

			if err := stream.Send(msg); err != nil {
				return fmt.Errorf("send scanner event: %w", err)
			}
		}
	}
}

// GetScannerConfig returns the persisted configuration.
func (s *ScannerService) GetScannerConfig(
	_ context.Context,
	_ *scannerv1.GetScannerConfigRequest,
) (*scannerv1.GetScannerConfigResponse, error) {
	if s.Cfg == nil {
		return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("scanner config is unavailable"))
	}

	return &scannerv1.GetScannerConfigResponse{Config: scannerConfigToProto(s.Cfg.GetScanner())}, nil
}

// SetScannerConfig persists and applies the configuration.
func (s *ScannerService) SetScannerConfig(
	_ context.Context,
	req *scannerv1.SetScannerConfigRequest,
) (*scannerv1.SetScannerConfigResponse, error) {
	if s.Cfg == nil {
		return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("scanner config is unavailable"))
	}

	in := req.GetConfig()
	settings := config.ScannerSettings{
		Enable:             in.GetEnabled(),
		Mode:               config.ScannerModeAuto,
		ManualSite:         in.GetManualSiteKey(),
		States:             in.GetStates(),
		HysteresisKm:       in.GetHysteresisKm(),
		PositionStaleAfter: scannerDurationOr(in.GetPositionStaleAfter(), config.DefaultScannerPositionStaleAfter),
		AircraftStaleAfter: scannerDurationOr(in.GetAircraftStaleAfter(), config.DefaultScannerAircraftStaleAfter),
		DecodeStaleAfter:   scannerDurationOr(in.GetDecodeStaleAfter(), config.DefaultScannerDecodeStaleAfter),
	}

	if in.GetMode() == scannerv1.SiteSelectionMode_SITE_SELECTION_MODE_MANUAL {
		settings.Mode = config.ScannerModeManual
	}

	if settings.ManualSite != "" {
		if err := s.checkSiteKey(settings.ManualSite); err != nil {
			return nil, err
		}
	}

	if err := s.Cfg.PersistScannerConfig(settings); err != nil {
		s.Log.Error().Err(err).Msg("Failed to persist scanner config")

		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("persist scanner config: %w", err))
	}

	cfg := s.Cfg.GetScanner()
	if s.Manager != nil {
		s.Manager.Apply(cfg)
	}

	s.Log.Info().Bool("enabled", cfg.Enable).Str("mode", cfg.Mode).Str("manual_site", cfg.ManualSite).
		Msg("Scanner config updated")

	return &scannerv1.SetScannerConfigResponse{Config: scannerConfigToProto(cfg)}, nil
}

func (s *ScannerService) checkSiteKey(key string) error {
	var (
		all []sites.Site
		err error
	)

	if s.Manager != nil {
		all, err = s.Manager.Sites()
	} else {
		all, err = sites.Table()
	}

	if err != nil {
		s.Log.Error().Err(err).Msg("Failed to load P25 site table")

		return connect.NewError(connect.CodeInternal, fmt.Errorf("load site table: %w", err))
	}

	if _, ok := sites.ByKey(all, key); !ok {
		return connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("manual_site_key %q is not in the site table", key))
	}

	return nil
}

func scannerDurationOr(d *durationpb.Duration, def time.Duration) time.Duration {
	if d == nil {
		return def
	}

	return d.AsDuration()
}

func scannerOptTimestamp(t time.Time) *timestamppb.Timestamp {
	if t.IsZero() {
		return nil
	}

	return timestamppb.New(t)
}

// The scanner packages number their enums to match the proto values (the
// zero value is UNSPECIFIED); scanner_test.go pins every mapping.

func scannerReceiverToProto(r *receivers.Receiver) *scannerv1.Receiver {
	return &scannerv1.Receiver{
		Role:           scannerv1.ReceiverRole(r.Role),
		Serial:         r.Serial,
		Hardware:       scannerv1.ReceiverHardware(r.Hardware),
		Present:        r.Present,
		Owner:          r.Owner,
		State:          scannerv1.ReceiverState(r.State),
		LastSampleTime: scannerOptTimestamp(r.LastSample),
		Detail:         r.Detail,
	}
}

func scannerPositionToProto(p scanner.Position) *scannerv1.Position {
	out := &scannerv1.Position{Stale: p.Stale}

	switch p.Source {
	case scanner.PositionGPSD:
		out.Source = scannerv1.PositionSource_POSITION_SOURCE_GPSD
	case scanner.PositionLastKnown:
		out.Source = scannerv1.PositionSource_POSITION_SOURCE_LAST_KNOWN
	case scanner.PositionManual:
		out.Source = scannerv1.PositionSource_POSITION_SOURCE_MANUAL
	default:
		out.Source = scannerv1.PositionSource_POSITION_SOURCE_NONE
		out.Stale = true

		return out
	}

	out.Latitude, out.Longitude, out.FixTime = p.Lat, p.Lon, scannerOptTimestamp(p.FixTime)

	return out
}

func scannerSiteToProto(s *sites.Site, distanceKm float64) *scannerv1.Site {
	return &scannerv1.Site{
		Key:              s.Key,
		State:            s.State,
		System:           s.System,
		Name:             s.Name,
		County:           s.County,
		Latitude:         s.Lat,
		Longitude:        s.Lon,
		DistanceKm:       distanceKm,
		ControlChannelHz: s.ControlHz,
		VoiceChannelHz:   s.VoiceHz,
		Band:             scannerv1.SiteBand(s.Band),
		Phase:            scannerv1.P25Phase(s.Phase),
		PhaseDetail:      s.PhaseDetail,
		Encryption:       scannerv1.SiteEncryption(s.Encryption),
		SpanHz:           s.SpanHz(),
		RtlSdrNeeded:     uint32(s.RTLNeeded),
		WideTunersNeeded: uint32(s.WideNeeded),
		RtlSdrFits:       s.RTLNeeded == 1,
	}
}

func scannerSelectionToProto(sel *scanner.Selection, tunedHz uint64) *scannerv1.SelectedSite {
	mode := scannerv1.SiteSelectionMode_SITE_SELECTION_MODE_AUTO
	if sel.Mode == config.ScannerModeManual {
		mode = scannerv1.SiteSelectionMode_SITE_SELECTION_MODE_MANUAL
	}

	cc := tunedHz
	if cc == 0 {
		cc = sel.Plan.Control
	}

	return &scannerv1.SelectedSite{
		Site:             scannerSiteToProto(sel.Site, sel.DistanceKm),
		Mode:             mode,
		Reason:           scannerv1.SiteSelectionReason(sel.Reason),
		ControlChannelHz: cc,
		WideTuner: &scannerv1.TunerPlan{
			CenterHz:          sel.Plan.CenterHz,
			UsableBandwidthHz: sel.Plan.UsableHz,
			Fits:              sel.Plan.Fits,
			CoveredHz:         sel.Plan.Covered,
			UncoveredHz:       sel.Plan.Uncovered,
		},
		SelectedAt: scannerOptTimestamp(sel.SelectedAt),
	}
}

func scannerControlToProto(c *scanner.ControlStatus) *scannerv1.ControlChannelStatus {
	return &scannerv1.ControlChannelStatus{
		State:          scannerv1.ControlChannelState(c.State),
		FrequencyHz:    c.FrequencyHz,
		LastDecodeTime: scannerOptTimestamp(c.LastDecode),
		Stale:          c.Stale,
		Wacn:           c.WACN,
		SystemId:       c.SystemID,
		Rfss:           c.RFSS,
		SiteId:         c.SiteID,
		Nac:            c.NAC,
		Detail:         c.Detail,
	}
}

func scannerAircraftToProto(list []aircraft.Aircraft, now time.Time) []*scannerv1.Aircraft {
	out := make([]*scannerv1.Aircraft, 0, len(list))

	for i := range list {
		a := &list[i]
		p := &scannerv1.Aircraft{
			Icao:        a.ICAO,
			Callsign:    a.Callsign,
			Squawk:      a.Squawk,
			OnGround:    a.OnGround,
			Source:      scannerv1.AircraftSource(a.Sources),
			LastSeen:    scannerOptTimestamp(a.LastSeen),
			SeenSeconds: max(0, now.Sub(a.LastSeen).Seconds()),
			Stale:       a.Stale,
		}

		// The optional fields point into list; callers send the messages
		// before reusing list for the next snapshot.
		if a.HasPos {
			p.Latitude, p.Longitude = &a.Lat, &a.Lon
		}

		if a.HasAlt && !a.OnGround {
			p.AltitudeFt = &a.AltFt
		}

		if a.HasGS {
			p.GroundSpeedKt = &a.GS
		}

		if a.HasTrack {
			p.TrackDeg = &a.Track
		}

		if a.HasVRate {
			p.VerticalRateFpm = &a.VRate
		}

		if a.HasRSSI {
			p.RssiDbfs = &a.RSSI
		}

		out = append(out, p)
	}

	return out
}

func scannerFeedsToProto(feeds []aircraft.FeedStatus) []*scannerv1.AircraftFeed {
	out := make([]*scannerv1.AircraftFeed, 0, len(feeds))

	for i := range feeds {
		f := &feeds[i]
		p := &scannerv1.AircraftFeed{
			Source:     scannerv1.AircraftSource(f.Source),
			Path:       "/" + f.Path,
			LastUpdate: scannerOptTimestamp(f.Updated),
			Aircraft:   uint32(f.Aircraft),
			Stale:      f.Stale,
		}

		if f.Err != nil {
			p.Error = f.Err.Error()
		}

		out = append(out, p)
	}

	return out
}

func scannerGrantToProto(g *scanner.Grant) *scannerv1.VoiceGrant {
	return &scannerv1.VoiceGrant{
		Talkgroup:   g.Talkgroup,
		SourceUnit:  g.Source,
		FrequencyHz: g.FrequencyHz,
		Channel:     g.Channel,
		Encrypted:   g.Encrypted,
		Emergency:   g.Emergency,
		StartTime:   scannerOptTimestamp(g.Start),
		LastTime:    scannerOptTimestamp(g.Last),
		Grants:      g.Grants,
		SiteKey:     g.SiteKey,
	}
}

func scannerEventToProto(ev *scanner.Event) *scannerv1.StreamScannerEventsResponse {
	out := &scannerv1.StreamScannerEventsResponse{Time: scannerOptTimestamp(ev.Time)}

	switch ev.Kind {
	case scanner.EventGrant:
		out.Event = &scannerv1.StreamScannerEventsResponse_VoiceGrant{VoiceGrant: scannerGrantToProto(&ev.Grant)}
	case scanner.EventControl:
		out.Event = &scannerv1.StreamScannerEventsResponse_ControlChannel{ControlChannel: scannerControlToProto(&ev.Control)}
	case scanner.EventSite:
		site := &scannerv1.SelectedSite{}
		if ev.Site != nil {
			site = scannerSelectionToProto(ev.Site, 0)
		}

		out.Event = &scannerv1.StreamScannerEventsResponse_SiteChanged{SiteChanged: site}
	default:
		return nil
	}

	return out
}

func scannerConfigToProto(c config.ScannerConfig) *scannerv1.ScannerConfig {
	mode := scannerv1.SiteSelectionMode_SITE_SELECTION_MODE_AUTO
	if c.Mode == config.ScannerModeManual {
		mode = scannerv1.SiteSelectionMode_SITE_SELECTION_MODE_MANUAL
	}

	return &scannerv1.ScannerConfig{
		Enabled:            c.Enable,
		Mode:               mode,
		ManualSiteKey:      c.ManualSite,
		States:             c.States,
		HysteresisKm:       c.HysteresisKm,
		PositionStaleAfter: durationpb.New(c.PositionStaleAfter),
		AircraftStaleAfter: durationpb.New(c.AircraftStaleAfter),
		DecodeStaleAfter:   durationpb.New(c.DecodeStaleAfter),
	}
}
