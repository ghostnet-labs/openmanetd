package handlers

import (
	"context"
	"errors"
	"fmt"

	"connectrpc.com/connect"
	jamv1 "github.com/openmanet/openmanetd/internal/api/openmanet/jamdetect/v1"
	"github.com/openmanet/openmanetd/internal/jamdetect"
	"github.com/rs/zerolog"
)

// JamDetector is the jamming detector surface JamDetectService needs.
// *jamdetect.Detector implements it.
type JamDetector interface {
	Report() *jamv1.JamReport
	Remote() []*jamv1.JamReport
	Recent() []jamdetect.Event
	Active() []jamdetect.Event
	Subscribe(ctx context.Context) (<-chan jamdetect.Event, error)
}

// JamDetectService serves jamming detection state.
type JamDetectService struct {
	Log zerolog.Logger
	// Detector is nil when jamdetect.enable is false; every RPC then
	// reports enabled false with empty data. It must be a nil interface,
	// not a typed nil pointer.
	Detector JamDetector
}

// GetJamStatus returns this node's report, every other node's latest
// report and this node's recent episodes.
func (s *JamDetectService) GetJamStatus(
	_ context.Context,
	_ *jamv1.GetJamStatusRequest,
) (*jamv1.GetJamStatusResponse, error) {
	if s.Detector == nil {
		return &jamv1.GetJamStatusResponse{}, nil
	}

	recent := s.Detector.Recent()

	res := &jamv1.GetJamStatusResponse{
		Enabled: true,
		Local:   s.Detector.Report(),
		Remote:  s.Detector.Remote(),
		Recent:  make([]*jamv1.JamEvent, 0, len(recent)),
	}

	for i := range recent {
		res.Recent = append(res.Recent, jamdetect.EventProto(&recent[i]))
	}

	return res, nil
}

// StreamJamEvents sends the open episodes, then every episode change,
// until the client leaves or the detector stops. With detection disabled
// it sends one empty message and ends.
func (s *JamDetectService) StreamJamEvents(
	ctx context.Context,
	_ *jamv1.StreamJamEventsRequest,
	stream *connect.ServerStream[jamv1.StreamJamEventsResponse],
) error {
	if s.Detector == nil {
		if err := stream.Send(&jamv1.StreamJamEventsResponse{}); err != nil {
			return fmt.Errorf("send empty jam state: %w", err)
		}

		return nil
	}

	events, err := s.Detector.Subscribe(ctx)

	switch {
	case errors.Is(err, jamdetect.ErrTooManySubscribers):
		s.Log.Warn().Err(err).Msg("Refusing jam event stream")

		return connect.NewError(connect.CodeResourceExhausted, err)
	case err != nil:
		s.Log.Error().Err(err).Msg("Failed to subscribe to jam events")

		return connect.NewError(connect.CodeInternal, err)
	}

	// Open episodes go first so the client starts with the current state
	// (and gets response headers) without waiting for a change.
	active := s.Detector.Active()

	first := &jamv1.StreamJamEventsResponse{Events: make([]*jamv1.JamEvent, 0, len(active))}
	for i := range active {
		first.Events = append(first.Events, jamdetect.EventProto(&active[i]))
	}

	if err := stream.Send(first); err != nil {
		return fmt.Errorf("send open jam episodes: %w", err)
	}

	for {
		select {
		case <-ctx.Done():
			return nil
		case ev, ok := <-events:
			if !ok {
				return nil
			}

			if err := stream.Send(&jamv1.StreamJamEventsResponse{Events: []*jamv1.JamEvent{jamdetect.EventProto(&ev)}}); err != nil {
				return fmt.Errorf("send jam event: %w", err)
			}
		}
	}
}
