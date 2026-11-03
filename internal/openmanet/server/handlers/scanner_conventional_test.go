package handlers_test

// scanner_conventional_test.go — conventional scan list and audio stream:
// unit tests against a fake manager, and an end-to-end run that replays the
// synthesized AM and NFM fixtures through the real conventional scanner and
// scanner.Manager into StreamAudio.

import (
	"context"
	"encoding/binary"
	"errors"
	"math"
	"os"
	"path/filepath"
	"testing"
	"testing/fstest"
	"time"

	"connectrpc.com/connect"
	scannerv1 "github.com/openmanet/openmanetd/internal/api/openmanet/scanner/v1"
	"github.com/openmanet/openmanetd/internal/comms/codec"
	"github.com/openmanet/openmanetd/internal/config"
	"github.com/openmanet/openmanetd/internal/openmanet/server/handlers"
	"github.com/openmanet/openmanetd/internal/scanner"
	"github.com/openmanet/openmanetd/internal/scanner/conventional"
	"github.com/openmanet/openmanetd/internal/scanner/conventional/conventionaltest"
	"github.com/openmanet/openmanetd/internal/scanner/sites"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func convTestChannel(state string, cat sites.ChannelCategory, hz uint64, label string, pos ...float64) *sites.Channel {
	c := &sites.Channel{State: state, Category: cat, Mode: sites.ModeNFM, SpacingHz: 12500, FrequencyHz: hz, Label: label}
	if cat == sites.CategoryAviation {
		c.Mode, c.SpacingHz = sites.ModeAM, 25000
	}

	if len(pos) == 2 {
		c.Lat, c.Lon, c.HasPosition = pos[0], pos[1], true
	}

	c.Key = sites.ChannelKey(c.State, c.Category, c.FrequencyHz, c.Label)

	return c
}

func TestScannerService_ListConventionalChannels(t *testing.T) {
	t.Run("disabled", func(t *testing.T) {
		svc := &handlers.ScannerService{Log: zerolog.Nop(), Manager: &fakeScannerManager{}}
		res, err := svc.ListConventionalChannels(context.Background(), &scannerv1.ListConventionalChannelsRequest{})
		require.NoError(t, err)
		assert.False(t, res.GetEnabled())
		assert.Empty(t, res.GetChannels())
		assert.InDelta(t, config.DefaultScannerConventionalRadiusKm, res.GetRadiusKm(), 1e-9)
	})

	tower := convTestChannel("UT", sites.CategoryAviation, 118_300_000, "SLC Tower", 40.78, -111.97)
	guard := convTestChannel("US", sites.CategoryAviation, 121_500_000, "guard")
	wx := convTestChannel("UT", sites.CategoryWeather, 162_550_000, "WX", 40.6, -112.0)

	mgr := &fakeScannerManager{enabled: true, conv: scanner.ConventionalStatus{
		State: scanner.ConventionalHolding, CenterHz: 118_800_000, Window: 1, Windows: 2, SampleRate: 2_048_000,
		Channels: []scanner.ConventionalChannelStatus{
			{ConventionalChannel: scanner.ConventionalChannel{Channel: tower, DistanceKm: 12.5}, Window: 1, Active: true,
				LastHeard: scannerT0, Level: -21.5},
			{ConventionalChannel: scanner.ConventionalChannel{Channel: guard, DistanceKm: math.NaN(), AlwaysOn: true}, Window: 1},
			{ConventionalChannel: scanner.ConventionalChannel{Channel: wx, DistanceKm: 20}},
		},
	}}
	mgr.status = enabledScannerStatus()
	svc := &handlers.ScannerService{Log: zerolog.Nop(), Manager: mgr}

	res, err := svc.ListConventionalChannels(context.Background(), &scannerv1.ListConventionalChannelsRequest{})
	require.NoError(t, err)
	assert.True(t, res.GetEnabled())

	st := res.GetStatus()
	assert.Equal(t, scannerv1.ConventionalScanState_CONVENTIONAL_SCAN_STATE_HOLDING, st.GetState())
	assert.Equal(t, uint64(118_800_000), st.GetCenterHz())
	assert.Equal(t, uint32(1), st.GetWindow())
	assert.Equal(t, uint32(2), st.GetWindows())
	assert.Equal(t, uint32(2_048_000), st.GetSampleRate())

	require.Len(t, res.GetChannels(), 3)

	c := res.GetChannels()[0]
	assert.Equal(t, tower.Key, c.GetKey())
	assert.Equal(t, "SLC Tower", c.GetLabel())
	assert.Equal(t, scannerv1.ConventionalMode_CONVENTIONAL_MODE_AM, c.GetMode())
	assert.Equal(t, scannerv1.ConventionalCategory_CONVENTIONAL_CATEGORY_AVIATION, c.GetCategory())
	assert.InDelta(t, 40.78, c.GetLatitude(), 1e-9)
	assert.InDelta(t, 12.5, c.GetDistanceKm(), 1e-9)
	assert.True(t, c.GetActive())
	assert.Equal(t, scannerT0.Unix(), c.GetLastHeardTime().GetSeconds())
	assert.InDelta(t, -21.5, c.GetLevel(), 1e-9)

	g := res.GetChannels()[1]
	assert.True(t, g.GetAlwaysOn())
	assert.Nil(t, g.DistanceKm, "no distance for channels without a location")
	assert.Nil(t, g.Latitude)
	assert.Nil(t, g.GetLastHeardTime())

	w := res.GetChannels()[2]
	assert.Equal(t, scannerv1.ConventionalMode_CONVENTIONAL_MODE_NFM, w.GetMode())
	assert.Equal(t, scannerv1.ConventionalCategory_CONVENTIONAL_CATEGORY_WEATHER, w.GetCategory())
	assert.Zero(t, w.GetWindow(), "not scanned")

	status, err := svc.GetScannerStatus(context.Background(), &scannerv1.GetScannerStatusRequest{})
	require.NoError(t, err)
	assert.Equal(t, scannerv1.ConventionalScanState_CONVENTIONAL_SCAN_STATE_HOLDING, status.GetConventional().GetState())
}

// fakeOpus is a ScannerAudioEncoder that writes the first PCM sample.
type fakeOpus struct {
	encodeErr error
	closed    bool
}

func (f *fakeOpus) EncodeS16(pcm []int16, out []byte) (int, error) {
	if f.encodeErr != nil {
		return 0, f.encodeErr
	}

	binary.LittleEndian.PutUint16(out, uint16(pcm[0])) //nolint:gosec // test payload

	return 2, nil
}

func (f *fakeOpus) Close() error {
	f.closed = true

	return nil
}

func audioFrame(key string, seq uint32, first int16) scanner.AudioFrame {
	f := scanner.AudioFrame{Time: scannerT0, ChannelKey: key, FrequencyHz: 155_475_000, Sequence: seq, Level: 0.2}
	for i := range f.PCM {
		f.PCM[i] = first + int16(i) //nolint:gosec // small test ramp
	}

	return f
}

func TestScannerService_StreamAudio(t *testing.T) {
	t.Run("pcm frames", func(t *testing.T) {
		mgr := &fakeScannerManager{enabled: true, audio: make(chan scanner.AudioFrame, 4)}
		client := newScannerTestClient(t, &handlers.ScannerService{Log: zerolog.Nop(), Manager: mgr})

		mgr.audio <- audioFrame("C-1", 1, -5)

		mgr.audio <- audioFrame("C-1", 3, 100)

		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		stream, err := client.StreamAudio(ctx, &scannerv1.StreamAudioRequest{ChannelKey: "C-1"})
		require.NoError(t, err)

		require.True(t, stream.Receive(), stream.Err())
		head := stream.Msg()
		assert.Empty(t, head.GetData(), "first message only confirms the stream")
		assert.Equal(t, scannerv1.AudioEncoding_AUDIO_ENCODING_PCM_S16LE, head.GetEncoding())
		assert.Equal(t, uint32(8000), head.GetSampleRate())

		require.True(t, stream.Receive(), stream.Err())
		m := stream.Msg()
		require.Len(t, m.GetData(), 320)
		assert.Equal(t, int16(-5), int16(binary.LittleEndian.Uint16(m.GetData())))           //nolint:gosec // decode
		assert.Equal(t, int16(-5+159), int16(binary.LittleEndian.Uint16(m.GetData()[318:]))) //nolint:gosec // decode
		assert.Equal(t, uint32(1), m.GetSequence())
		assert.Equal(t, "C-1", m.GetChannelKey())
		assert.Equal(t, uint64(155_475_000), m.GetFrequencyHz())
		assert.Equal(t, scannerT0.Unix(), m.GetTime().GetSeconds())
		assert.InDelta(t, 0.2, m.GetLevel(), 1e-6)

		require.True(t, stream.Receive(), stream.Err())
		assert.Equal(t, uint32(3), stream.Msg().GetSequence())
		assert.Equal(t, int16(100), int16(binary.LittleEndian.Uint16(stream.Msg().GetData()))) //nolint:gosec // decode

		close(mgr.audio)
		assert.False(t, stream.Receive(), "manager closed the stream")
		require.NoError(t, stream.Err())
		require.Eventually(t, func() bool { return mgr.getAudioCancels() == 1 }, 5*time.Second, time.Millisecond)
	})

	t.Run("opus requested", func(t *testing.T) {
		mgr := &fakeScannerManager{enabled: true, audio: make(chan scanner.AudioFrame, 1)}
		enc := &fakeOpus{}
		client := newScannerTestClient(t, &handlers.ScannerService{
			Log: zerolog.Nop(), Manager: mgr,
			NewOpusEncoder: func() (handlers.ScannerAudioEncoder, error) { return enc, nil },
		})

		mgr.audio <- audioFrame("C-2", 1, 7)

		close(mgr.audio)

		stream, err := client.StreamAudio(context.Background(), &scannerv1.StreamAudioRequest{
			Encoding: scannerv1.AudioEncoding_AUDIO_ENCODING_OPUS,
		})
		require.NoError(t, err)
		require.True(t, stream.Receive())
		assert.Equal(t, scannerv1.AudioEncoding_AUDIO_ENCODING_OPUS, stream.Msg().GetEncoding())
		require.True(t, stream.Receive())
		assert.Equal(t, []byte{7, 0}, stream.Msg().GetData())
		assert.False(t, stream.Receive())
		assert.True(t, enc.closed)
	})

	t.Run("opus unavailable falls back to pcm", func(t *testing.T) {
		mgr := &fakeScannerManager{enabled: true, audio: make(chan scanner.AudioFrame)}
		close(mgr.audio)

		client := newScannerTestClient(t, &handlers.ScannerService{
			Log: zerolog.Nop(), Manager: mgr,
			NewOpusEncoder: func() (handlers.ScannerAudioEncoder, error) { return nil, errors.New("no libopus") },
		})

		stream, err := client.StreamAudio(context.Background(), &scannerv1.StreamAudioRequest{
			Encoding: scannerv1.AudioEncoding_AUDIO_ENCODING_OPUS,
		})
		require.NoError(t, err)
		require.True(t, stream.Receive())
		assert.Equal(t, scannerv1.AudioEncoding_AUDIO_ENCODING_PCM_S16LE, stream.Msg().GetEncoding())
	})

	t.Run("encode failure", func(t *testing.T) {
		mgr := &fakeScannerManager{enabled: true, audio: make(chan scanner.AudioFrame, 1)}
		mgr.audio <- audioFrame("C-2", 1, 7)

		client := newScannerTestClient(t, &handlers.ScannerService{
			Log: zerolog.Nop(), Manager: mgr,
			NewOpusEncoder: func() (handlers.ScannerAudioEncoder, error) { return &fakeOpus{encodeErr: errors.New("bad")}, nil },
		})

		stream, err := client.StreamAudio(context.Background(), &scannerv1.StreamAudioRequest{
			Encoding: scannerv1.AudioEncoding_AUDIO_ENCODING_OPUS,
		})
		require.NoError(t, err)
		require.True(t, stream.Receive())
		assert.False(t, stream.Receive())

		var connectErr *connect.Error
		require.ErrorAs(t, stream.Err(), &connectErr)
		assert.Equal(t, connect.CodeInternal, connectErr.Code())
	})

	errCases := []struct {
		name string
		err  error
		code connect.Code
	}{
		{name: "unknown channel", err: scanner.ErrUnknownChannel, code: connect.CodeNotFound},
		{name: "too many", err: scanner.ErrTooManyAudioStreams, code: connect.CodeResourceExhausted},
		{name: "disabled", err: scanner.ErrDisabled, code: connect.CodeFailedPrecondition},
		{name: "no conventional", err: scanner.ErrConventionalUnavailable, code: connect.CodeFailedPrecondition},
	}

	for _, tc := range errCases {
		t.Run(tc.name, func(t *testing.T) {
			client := newScannerTestClient(t, &handlers.ScannerService{
				Log: zerolog.Nop(), Manager: &fakeScannerManager{audioErr: tc.err},
			})

			stream, err := client.StreamAudio(context.Background(), &scannerv1.StreamAudioRequest{ChannelKey: "C-x"})
			require.NoError(t, err)
			assert.False(t, stream.Receive())

			var connectErr *connect.Error
			require.ErrorAs(t, stream.Err(), &connectErr)
			assert.Equal(t, tc.code, connectErr.Code())
		})
	}

	t.Run("validation", func(t *testing.T) {
		client := newScannerTestClient(t, &handlers.ScannerService{Log: zerolog.Nop(), Manager: &fakeScannerManager{}})

		stream, err := client.StreamAudio(context.Background(), &scannerv1.StreamAudioRequest{Encoding: 9})
		require.NoError(t, err)
		assert.False(t, stream.Receive())

		var connectErr *connect.Error
		require.ErrorAs(t, stream.Err(), &connectErr)
		assert.Equal(t, connect.CodeInvalidArgument, connectErr.Code())
	})

	t.Run("nil manager", func(t *testing.T) {
		client := newScannerTestClient(t, &handlers.ScannerService{Log: zerolog.Nop()})

		stream, err := client.StreamAudio(context.Background(), &scannerv1.StreamAudioRequest{})
		require.NoError(t, err)
		assert.False(t, stream.Receive())

		var connectErr *connect.Error
		require.ErrorAs(t, stream.Err(), &connectErr)
		assert.Equal(t, connect.CodeFailedPrecondition, connectErr.Code())
	})
}

// Fixture geometry (testfixtures/sdr/conventional/README.md).
const (
	convFixtureRate   = 256_000
	convAviationHz    = 120_000_000
	convAviationLive  = 120_050_000
	convAviationQuiet = 119_950_000
	convNFMHz         = 155_425_000
	convNFMLive       = 155_450_000
	convNFMQuiet      = 155_400_000
)

func convFixture(t *testing.T, name string) []byte {
	t.Helper()

	data, err := os.ReadFile(filepath.Join(filepath.Dir(filepath.Dir(p25FixturePath(t))), "conventional", name))
	require.NoError(t, err)

	return data
}

// p25FixturePath returns testfixtures/sdr/p25 (located like p25Fixture).
func p25FixturePath(t *testing.T) string {
	t.Helper()

	dir, err := os.Getwd()
	require.NoError(t, err)

	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return filepath.Join(dir, "testfixtures", "sdr", "p25", "x")
		}

		parent := filepath.Dir(dir)
		require.NotEqual(t, dir, parent, "module root not found")
		dir = parent
	}
}

// TestScannerService_conventionalFixtureEndToEnd replays the synthesized
// AM (aviation) and NFM captures through the real conventional scanner
// and scanner.Manager: the channel list shows the scanned channels, the
// squelch opens only on the two carrying channels, and StreamAudio
// delivers their audio as PCM and as Opus that decodes back to the tone.
func TestScannerService_conventionalFixtureEndToEnd(t *testing.T) {
	runner := &conventionaltest.Runner{Captures: []conventionaltest.Capture{
		{LowHz: convAviationHz, HighHz: convAviationHz, Data: convFixture(t, "am_256k_aviation.cu8")},
		{LowHz: convNFMHz, HighHz: convNFMHz, Data: convFixture(t, "nfm_256k_vhf.cu8")},
	}}
	conv := conventional.New(conventional.Options{
		Log: zerolog.Nop(), Runner: runner, SampleRate: convFixtureRate,
		Dwell: 100 * time.Millisecond, Hold: 50 * time.Millisecond, MaxHold: 250 * time.Millisecond,
		Settle: 5 * time.Millisecond, MinBackoff: 5 * time.Millisecond,
	})

	convCtx, convCancel := context.WithCancel(context.Background())
	convDone := make(chan struct{})

	go func() {
		defer close(convDone)

		conv.Run(convCtx)
	}()

	// Nationwide entries need no position fix.
	channels := []sites.Channel{
		*convTestChannel("US", sites.CategoryAviation, convAviationQuiet, "Quiet Tower"),
		*convTestChannel("US", sites.CategoryAviation, convAviationLive, "Live Tower"),
		*convTestChannel("US", sites.CategoryInterop, convNFMQuiet, "Quiet VHF"),
		*convTestChannel("US", sites.CategoryInterop, convNFMLive, "Live VHF"),
	}

	mgr := scanner.NewManager(scanner.Options{
		Log: zerolog.Nop(), Conventional: conv, RootFS: fstest.MapFS{}, SysFS: fstest.MapFS{},
		Sites: []sites.Site{}, Channels: channels, PollInterval: 20 * time.Millisecond,
	}, config.ScannerConfig{})

	t.Cleanup(func() {
		mgr.Disable()
		convCancel()
		<-convDone
	})

	mgr.Apply(config.ScannerConfig{
		Enable: true, Mode: config.ScannerModeAuto, ScanSerial: "scan", WideSerial: "44a061dc2e395e8f",
		TalkgroupHistory: 16, ConventionalRadiusKm: 80,
	})

	client := newScannerTestClient(t, &handlers.ScannerService{
		Log: zerolog.Nop(), Manager: mgr,
		NewOpusEncoder: func() (handlers.ScannerAudioEncoder, error) {
			return codec.NewOpusEncoder(scanner.AudioSampleRate, 1, 12_000, 5, 0) //nolint:wrapcheck // test
		},
	})

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	var list *scannerv1.ListConventionalChannelsResponse

	require.Eventually(t, func() bool {
		res, err := client.ListConventionalChannels(ctx, &scannerv1.ListConventionalChannelsRequest{})
		list = res

		return err == nil && len(res.GetChannels()) == 4 && res.GetStatus().GetWindows() == 2
	}, 10*time.Second, 10*time.Millisecond)

	keys := map[uint64]string{}
	for _, c := range list.GetChannels() {
		keys[c.GetFrequencyHz()] = c.GetKey()
	}

	// Every channel: frames arrive from both live channels, never from the
	// quiet ones.
	stream, err := client.StreamAudio(ctx, &scannerv1.StreamAudioRequest{})
	require.NoError(t, err)
	require.True(t, stream.Receive(), stream.Err())

	got := map[string]int{}
	for got[keys[convAviationLive]] < 5 || got[keys[convNFMLive]] < 5 {
		require.True(t, stream.Receive(), stream.Err())

		m := stream.Msg()
		got[m.GetChannelKey()]++

		require.Len(t, m.GetData(), 320)
		assert.Greater(t, pcmRMS(m.GetData()), 1000.0)
	}

	require.NoError(t, stream.Close())
	assert.Zero(t, got[keys[convAviationQuiet]], "quiet aviation channel stays squelched")
	assert.Zero(t, got[keys[convNFMQuiet]], "quiet NFM channel stays squelched")

	// One channel as Opus: pinned to its window, decodes back to audio.
	opus, err := client.StreamAudio(ctx, &scannerv1.StreamAudioRequest{
		ChannelKey: keys[convAviationLive], Encoding: scannerv1.AudioEncoding_AUDIO_ENCODING_OPUS,
	})
	require.NoError(t, err)
	require.True(t, opus.Receive(), opus.Err())
	assert.Equal(t, scannerv1.AudioEncoding_AUDIO_ENCODING_OPUS, opus.Msg().GetEncoding())

	dec, err := codec.NewOpusDecoder(scanner.AudioSampleRate, 1)
	require.NoError(t, err)

	pcm := make([]int16, scanner.AudioFrameSamples)

	var loud int

	for range 20 {
		require.True(t, opus.Receive(), opus.Err())
		assert.Equal(t, keys[convAviationLive], opus.Msg().GetChannelKey())

		n, err := dec.DecodeS16(opus.Msg().GetData(), pcm)
		require.NoError(t, err)
		require.Equal(t, scanner.AudioFrameSamples, n)

		if pcmRMS16(pcm) > 1000 {
			loud++
		}
	}

	require.NoError(t, opus.Close())
	assert.Greater(t, loud, 10, "Opus frames decode back to the tone")

	res, err := client.ListConventionalChannels(ctx, &scannerv1.ListConventionalChannelsRequest{})
	require.NoError(t, err)

	for _, c := range res.GetChannels() {
		live := c.GetFrequencyHz() == convAviationLive || c.GetFrequencyHz() == convNFMLive
		assert.Equal(t, live, c.GetLastHeardTime() != nil, c.GetLabel())
	}

	status, err := client.GetScannerStatus(ctx, &scannerv1.GetScannerStatusRequest{})
	require.NoError(t, err)

	for _, r := range status.GetReceivers() {
		if r.GetRole() == scannerv1.ReceiverRole_RECEIVER_ROLE_SCAN {
			assert.NotContains(t, r.GetDetail(), "not wired")
		}
	}
}

func pcmRMS(b []byte) float64 {
	pcm := make([]int16, len(b)/2)
	for i := range pcm {
		pcm[i] = int16(binary.LittleEndian.Uint16(b[2*i:])) //nolint:gosec // decode
	}

	return pcmRMS16(pcm)
}

func pcmRMS16(pcm []int16) float64 {
	var acc float64
	for _, v := range pcm {
		acc += float64(v) * float64(v)
	}

	return math.Sqrt(acc / float64(len(pcm)))
}
