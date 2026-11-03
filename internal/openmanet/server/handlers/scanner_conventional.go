package handlers

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"math"

	"connectrpc.com/connect"
	scannerv1 "github.com/openmanet/openmanetd/internal/api/openmanet/scanner/v1"
	"github.com/openmanet/openmanetd/internal/config"
	"github.com/openmanet/openmanetd/internal/scanner"
	"github.com/openmanet/openmanetd/internal/scanner/conventional"
	"github.com/openmanet/openmanetd/internal/scanner/sites"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// opusMaxPacket is the largest Opus packet (RFC 6716).
const opusMaxPacket = 1275

// ScannerAudioEncoder encodes one 20 ms frame of 8 kHz mono PCM
// (codec.AudioEncoder satisfies it).
type ScannerAudioEncoder interface {
	EncodeS16(pcm []int16, out []byte) (int, error)
	Close() error
}

// ListConventionalChannels returns the conventional scan list.
func (s *ScannerService) ListConventionalChannels(
	_ context.Context,
	_ *scannerv1.ListConventionalChannelsRequest,
) (*scannerv1.ListConventionalChannelsResponse, error) {
	res := &scannerv1.ListConventionalChannelsResponse{
		GeneratedAt: timestamppb.Now(),
		RadiusKm:    config.DefaultScannerConventionalRadiusKm,
	}

	if s.Cfg != nil {
		res.RadiusKm = cmpOrFloat(s.Cfg.GetScanner().ConventionalRadiusKm, config.DefaultScannerConventionalRadiusKm)
	}

	if s.Manager == nil || !s.Manager.Enabled() {
		return res, nil
	}

	st := s.Manager.Conventional()
	res.Enabled = true
	res.Status = scannerConvStatusToProto(st)
	res.Channels = make([]*scannerv1.ConventionalChannel, 0, len(st.Channels))

	for i := range st.Channels {
		res.Channels = append(res.Channels, scannerConvChannelToProto(&st.Channels[i]))
	}

	return res, nil
}

// StreamAudio relays demodulated conventional audio to the client until it
// leaves or the scanner stops. Frames are 8 kHz PCM, or Opus when asked
// for and an encoder is wired.
func (s *ScannerService) StreamAudio(
	ctx context.Context,
	req *scannerv1.StreamAudioRequest,
	stream *connect.ServerStream[scannerv1.StreamAudioResponse],
) error {
	if s.Manager == nil {
		return connect.NewError(connect.CodeFailedPrecondition, scanner.ErrDisabled)
	}

	frames, cancel, err := s.Manager.SubscribeAudio(req.GetChannelKey())

	switch {
	case errors.Is(err, scanner.ErrUnknownChannel):
		return connect.NewError(connect.CodeNotFound, err)
	case errors.Is(err, scanner.ErrTooManyAudioStreams):
		s.Log.Warn().Err(err).Msg("Refusing scanner audio stream")

		return connect.NewError(connect.CodeResourceExhausted, err)
	case err != nil:
		return connect.NewError(connect.CodeFailedPrecondition, err)
	}

	defer cancel()

	enc, closeEnc := s.audioEncoder(req.GetEncoding())
	defer closeEnc()

	encoding := scannerv1.AudioEncoding_AUDIO_ENCODING_PCM_S16LE
	if enc != nil {
		encoding = scannerv1.AudioEncoding_AUDIO_ENCODING_OPUS
	}

	// The first message confirms the stream (and flushes the response
	// headers) before any squelch opens.
	if err := stream.Send(&scannerv1.StreamAudioResponse{Encoding: encoding, SampleRate: scanner.AudioSampleRate}); err != nil {
		return fmt.Errorf("send audio stream header: %w", err)
	}

	// One response message, timestamp and payload buffer are reused for
	// every frame: Send marshals synchronously.
	var (
		ts  timestamppb.Timestamp
		buf = make([]byte, max(opusMaxPacket, 2*scanner.AudioFrameSamples))
		msg = scannerv1.StreamAudioResponse{Encoding: encoding, SampleRate: scanner.AudioSampleRate, Time: &ts}
	)

	for {
		select {
		case <-ctx.Done():
			return nil
		case f, ok := <-frames:
			if !ok {
				return nil
			}

			payload, err := encodeAudio(enc, &f, buf)
			if err != nil {
				s.Log.Error().Err(err).Msg("Failed to encode scanner audio")

				return connect.NewError(connect.CodeInternal, err)
			}

			ts.Seconds, ts.Nanos = f.Time.Unix(), int32(f.Time.Nanosecond()) //nolint:gosec // nanoseconds fit int32
			msg.Data, msg.Sequence, msg.ChannelKey = payload, f.Sequence, f.ChannelKey
			msg.FrequencyHz, msg.Talkgroup, msg.Level = f.FrequencyHz, f.Talkgroup, float64(f.Level)

			if err := stream.Send(&msg); err != nil {
				return fmt.Errorf("send audio frame: %w", err)
			}
		}
	}
}

// audioEncoder returns an Opus encoder when one is requested and wired,
// else nil (PCM), and its close function.
func (s *ScannerService) audioEncoder(want scannerv1.AudioEncoding) (ScannerAudioEncoder, func()) {
	if want != scannerv1.AudioEncoding_AUDIO_ENCODING_OPUS || s.NewOpusEncoder == nil {
		return nil, func() {}
	}

	enc, err := s.NewOpusEncoder()
	if err != nil {
		s.Log.Warn().Err(err).Msg("Opus encoder unavailable; streaming scanner audio as PCM")

		return nil, func() {}
	}

	return enc, func() {
		if err := enc.Close(); err != nil {
			s.Log.Debug().Err(err).Msg("Closing scanner Opus encoder")
		}
	}
}

// encodeAudio writes f's payload into buf: Opus when enc is set, else
// little-endian PCM.
func encodeAudio(enc ScannerAudioEncoder, f *scanner.AudioFrame, buf []byte) ([]byte, error) {
	if enc != nil {
		n, err := enc.EncodeS16(f.PCM[:], buf)
		if err != nil {
			return nil, fmt.Errorf("encode opus: %w", err)
		}

		return buf[:n], nil
	}

	out := buf[:2*len(f.PCM)]
	for i, v := range f.PCM {
		binary.LittleEndian.PutUint16(out[2*i:], uint16(v)) //nolint:gosec // two's complement bit pattern on purpose
	}

	return out, nil
}

func scannerConvStatusToProto(st scanner.ConventionalStatus) *scannerv1.ConventionalScanStatus {
	return &scannerv1.ConventionalScanStatus{
		State:      scannerConvStateToProto(st.State),
		Detail:     st.Detail,
		CenterHz:   st.CenterHz,
		Window:     uint32(max(st.Window, 0)),     //nolint:gosec // bounded by the window count
		Windows:    uint32(max(st.Windows, 0)),    //nolint:gosec // at most conventional.DefaultMaxWindows
		SampleRate: uint32(max(st.SampleRate, 0)), //nolint:gosec // rtl_sdr rates fit uint32
	}
}

func scannerConvStateToProto(s scanner.ConventionalState) scannerv1.ConventionalScanState {
	switch s {
	case scanner.ConventionalUnavailable:
		return scannerv1.ConventionalScanState_CONVENTIONAL_SCAN_STATE_UNAVAILABLE
	case scanner.ConventionalIdle:
		return scannerv1.ConventionalScanState_CONVENTIONAL_SCAN_STATE_IDLE
	case scanner.ConventionalReceiverBusy:
		return scannerv1.ConventionalScanState_CONVENTIONAL_SCAN_STATE_RECEIVER_BUSY
	case scanner.ConventionalScanning:
		return scannerv1.ConventionalScanState_CONVENTIONAL_SCAN_STATE_SCANNING
	case scanner.ConventionalHolding:
		return scannerv1.ConventionalScanState_CONVENTIONAL_SCAN_STATE_HOLDING
	case scanner.ConventionalError:
		return scannerv1.ConventionalScanState_CONVENTIONAL_SCAN_STATE_ERROR
	default:
		return scannerv1.ConventionalScanState_CONVENTIONAL_SCAN_STATE_UNSPECIFIED
	}
}

func scannerConvChannelToProto(c *scanner.ConventionalChannelStatus) *scannerv1.ConventionalChannel {
	ch := c.Channel
	out := &scannerv1.ConventionalChannel{
		Key:           ch.Key,
		Label:         ch.Label,
		FrequencyHz:   ch.FrequencyHz,
		Mode:          scannerv1.ConventionalMode_CONVENTIONAL_MODE_NFM,
		Category:      scannerConvCategoryToProto(ch.Category),
		State:         ch.State,
		AlwaysOn:      c.AlwaysOn,
		Window:        uint32(max(c.Window, 0)), //nolint:gosec // bounded by the window count
		Active:        c.Active,
		LastHeardTime: scannerOptTimestamp(c.LastHeard),
		Level:         c.Level,
	}

	if conventional.DemodMode(ch) == sites.ModeAM {
		out.Mode = scannerv1.ConventionalMode_CONVENTIONAL_MODE_AM
	}

	if ch.HasPosition {
		lat, lon := ch.Lat, ch.Lon
		out.Latitude, out.Longitude = &lat, &lon
	}

	if !math.IsNaN(c.DistanceKm) {
		d := c.DistanceKm
		out.DistanceKm = &d
	}

	return out
}

func scannerConvCategoryToProto(c sites.ChannelCategory) scannerv1.ConventionalCategory {
	switch c {
	case sites.CategoryConventional:
		return scannerv1.ConventionalCategory_CONVENTIONAL_CATEGORY_CONVENTIONAL
	case sites.CategoryAviation:
		return scannerv1.ConventionalCategory_CONVENTIONAL_CATEGORY_AVIATION
	case sites.CategoryInterop:
		return scannerv1.ConventionalCategory_CONVENTIONAL_CATEGORY_INTEROP
	case sites.CategoryWeather:
		return scannerv1.ConventionalCategory_CONVENTIONAL_CATEGORY_WEATHER
	default:
		return scannerv1.ConventionalCategory_CONVENTIONAL_CATEGORY_UNSPECIFIED
	}
}

func cmpOrFloat(v, def float64) float64 {
	if v == 0 {
		return def
	}

	return v
}
