package jamdetect

import (
	"time"

	jamv1 "github.com/openmanet/openmanetd/internal/api/openmanet/jamdetect/v1"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// Report builds this node's report: every radio's reading, its open
// episodes and its GNSS fix.
func (d *Detector) Report() *jamv1.JamReport {
	mac, host := d.identity()

	d.mu.Lock()
	readings := d.readingsLocked()
	active := d.activeLocked()
	d.mu.Unlock()

	rep := &jamv1.JamReport{
		PrimaryMac:   mac,
		Hostname:     host,
		CollectedAt:  timestamppb.New(d.now()),
		Radios:       make([]*jamv1.RadioJamStatus, 0, len(readings)),
		ActiveEvents: make([]*jamv1.JamEvent, 0, len(active)),
	}

	for i := range readings {
		rep.Radios = append(rep.Radios, ReadingProto(&readings[i]))
	}

	for i := range active {
		rep.ActiveEvents = append(rep.ActiveEvents, EventProto(&active[i]))
	}

	if d.opts.Position != nil {
		if p, ok := d.opts.Position(); ok {
			rep.Position = &jamv1.JamPosition{Latitude: p.Latitude, Longitude: p.Longitude, AltitudeM: p.AltitudeM}
		}
	}

	return rep
}

// ReadingProto converts a reading.
func ReadingProto(r *Reading) *jamv1.RadioJamStatus {
	return &jamv1.RadioJamStatus{
		Radio:                      r.Radio,
		Phy:                        r.PHY,
		Band:                       BandProto(r.Band),
		FreqMhz:                    r.FreqMHz,
		State:                      StateProto(r.State),
		NoiseKnown:                 r.NoiseKnown,
		NoiseDbm:                   int32(r.NoiseDBm), //nolint:gosec // dBm fits
		BaselineNoiseDbm:           r.BaselineNoiseDBm,
		BusyPct:                    r.BusyPct,
		UnexplainedBusyPct:         r.UnexplainedBusyPct,
		BaselineUnexplainedBusyPct: r.BaselineUnexplainedPct,
		Reasons:                    reasonsProto(r.Reasons),
		UpdatedAt:                  timestampOrNil(r.Updated),
	}
}

// EventProto converts an event.
func EventProto(e *Event) *jamv1.JamEvent {
	return &jamv1.JamEvent{
		Id:    e.ID,
		State: StateProto(e.State),
		Radio: &jamv1.RadioJamStatus{
			Radio:                      e.Radio,
			Phy:                        e.PHY,
			Band:                       BandProto(e.Band),
			FreqMhz:                    e.FreqMHz,
			State:                      StateProto(e.State),
			NoiseKnown:                 e.NoiseKnown,
			NoiseDbm:                   int32(e.NoiseDBm), //nolint:gosec // dBm fits
			BaselineNoiseDbm:           e.BaselineNoiseDBm,
			BusyPct:                    e.BusyPct,
			UnexplainedBusyPct:         e.UnexplainedBusyPct,
			BaselineUnexplainedBusyPct: e.BaselineBusyPct,
			Reasons:                    reasonsProto(e.Reasons),
			UpdatedAt:                  timestampOrNil(e.Updated),
		},
		StartedAt:              timestampOrNil(e.Started),
		UpdatedAt:              timestampOrNil(e.Updated),
		ClearedAt:              timestampOrNil(e.Cleared),
		PeakNoiseDbm:           int32(e.PeakNoiseDBm), //nolint:gosec // dBm fits
		PeakUnexplainedBusyPct: e.PeakUnexplainedPct,
	}
}

// StateProto converts a state.
func StateProto(s State) jamv1.JamState {
	switch s {
	case StateWarmingUp:
		return jamv1.JamState_JAM_STATE_WARMING_UP
	case StateClear:
		return jamv1.JamState_JAM_STATE_CLEAR
	case StateSuspected:
		return jamv1.JamState_JAM_STATE_SUSPECTED
	case StateJammed:
		return jamv1.JamState_JAM_STATE_JAMMED
	default:
		return jamv1.JamState_JAM_STATE_UNSPECIFIED
	}
}

// BandProto converts a band.
func BandProto(b Band) jamv1.RadioBand {
	switch b {
	case BandS1G:
		return jamv1.RadioBand_RADIO_BAND_S1G
	case Band2G4:
		return jamv1.RadioBand_RADIO_BAND_2G4
	case Band5G:
		return jamv1.RadioBand_RADIO_BAND_5G
	case Band6G:
		return jamv1.RadioBand_RADIO_BAND_6G
	default:
		return jamv1.RadioBand_RADIO_BAND_UNSPECIFIED
	}
}

func reasonsProto(rs []Reason) []jamv1.JamReason {
	if len(rs) == 0 {
		return nil
	}

	out := make([]jamv1.JamReason, 0, len(rs))

	for _, r := range rs {
		switch r {
		case ReasonNoiseRise:
			out = append(out, jamv1.JamReason_JAM_REASON_NOISE_RISE)
		case ReasonNoiseHigh:
			out = append(out, jamv1.JamReason_JAM_REASON_NOISE_HIGH)
		case ReasonBusyRise:
			out = append(out, jamv1.JamReason_JAM_REASON_BUSY_RISE)
		case ReasonBusyHigh:
			out = append(out, jamv1.JamReason_JAM_REASON_BUSY_HIGH)
		}
	}

	return out
}

func timestampOrNil(t time.Time) *timestamppb.Timestamp {
	if t.IsZero() {
		return nil
	}

	return timestamppb.New(t)
}
