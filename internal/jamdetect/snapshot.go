package jamdetect

// Snapshot is the "jamdetect" instrumentation section. Field semantics
// are documented in docs/instrumentation-snapshot.md; keep that file in
// sync when adding or renaming fields here.
type Snapshot struct {
	SamplesTotal             uint64 `json:"samples_total"`
	SurveyErrorsTotal        uint64 `json:"survey_errors_total"`
	EventsTotal              uint64 `json:"events_total"`
	JammedEventsTotal        uint64 `json:"jammed_events_total"`
	EventsDroppedTotal       uint64 `json:"events_dropped_total"`
	GossipPublishErrorsTotal uint64 `json:"gossip_publish_errors_total"`
	GossipFetchErrorsTotal   uint64 `json:"gossip_fetch_errors_total"`
	GossipDecodeErrorsTotal  uint64 `json:"gossip_decode_errors_total"`
	RadiosMonitored          uint32 `json:"radios_monitored"`
	RadiosWarmingUp          uint32 `json:"radios_warming_up"`
	RadiosSuspected          uint32 `json:"radios_suspected"`
	RadiosJammed             uint32 `json:"radios_jammed"`
	RemoteNodes              uint32 `json:"remote_nodes"`
	RemoteNodesJammed        uint32 `json:"remote_nodes_jammed"`
	Subscribers              uint32 `json:"subscribers"`
}

// Snapshotter adapts a Detector to the instrumentation registry. Refresh
// copies counters under the detector's mutex without allocating; Data
// returns a stable pointer to the owned snapshot.
type Snapshotter struct {
	Detector *Detector
	data     Snapshot
}

// Refresh implements instrumentation.Snapshotter.
func (s *Snapshotter) Refresh() {
	d := s.Detector
	if d == nil {
		return
	}

	d.mu.Lock()
	defer d.mu.Unlock()

	s.data = d.stats
	s.data.RadiosMonitored = uint32(len(d.trackers)) //nolint:gosec // bounded by maxRadios
	s.data.Subscribers = uint32(len(d.subs))         //nolint:gosec // bounded by maxSubscribers
	s.data.RemoteNodes = uint32(len(d.remote))       //nolint:gosec // bounded by maxRemote

	for _, t := range d.trackers {
		switch t.reading.State {
		case StateWarmingUp:
			s.data.RadiosWarmingUp++
		case StateSuspected:
			s.data.RadiosSuspected++
		case StateJammed:
			s.data.RadiosJammed++
		case StateClear:
		}
	}

	for _, r := range d.remote {
		for _, ev := range r.report.GetActiveEvents() {
			if StateProto(StateJammed) == ev.GetState() {
				s.data.RemoteNodesJammed++

				break
			}
		}
	}
}

// Data implements instrumentation.Snapshotter.
func (s *Snapshotter) Data() any { return &s.data }
