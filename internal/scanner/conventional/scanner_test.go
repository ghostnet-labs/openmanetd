package conventional_test

import (
	"context"
	"errors"
	"math"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/openmanet/openmanetd/internal/scanner"
	"github.com/openmanet/openmanetd/internal/scanner/conventional"
	"github.com/openmanet/openmanetd/internal/scanner/conventional/conventionaltest"
	"github.com/openmanet/openmanetd/internal/scanner/sites"
)

// waitTimeout bounds every wait on the replayed capture.
const waitTimeout = 10 * time.Second

func fixtureChannel(hz uint64, cat sites.ChannelCategory, mode sites.ChannelMode, spacing int) scanner.ConventionalChannel {
	c := &sites.Channel{FrequencyHz: hz, Category: cat, Mode: mode, SpacingHz: spacing, State: "UT", Label: "fixture"}
	c.Key = sites.ChannelKey(c.State, c.Category, hz, c.Label)

	return scanner.ConventionalChannel{Channel: c, DistanceKm: math.NaN()}
}

// fixturePlan is the scan list the fixtures were synthesized for: two
// aviation channels around 120 MHz and two VHF channels around 155.4 MHz,
// which plan into two windows.
func fixturePlan() scanner.ConventionalPlan {
	return scanner.ConventionalPlan{
		Serial: "scan",
		Channels: []scanner.ConventionalChannel{
			fixtureChannel(aviationQuietHz, sites.CategoryAviation, sites.ModeAM, 25000),
			fixtureChannel(aviationLiveHz, sites.CategoryAviation, sites.ModeAM, 25000),
			fixtureChannel(nfmQuietHz, sites.CategoryConventional, sites.ModeNFM, 12500),
			fixtureChannel(nfmLiveHz, sites.CategoryConventional, sites.ModeNFM, 12500),
		},
	}
}

// FixtureRunner replays the committed fixtures by tuned frequency.
func fixtureRunner(t testing.TB) *conventionaltest.Runner {
	t.Helper()

	return &conventionaltest.Runner{Captures: []conventionaltest.Capture{
		{LowHz: aviationCenterHz, HighHz: aviationCenterHz, Data: loadFixture(t, "am_256k_aviation.cu8")},
		{LowHz: nfmCenterHz, HighHz: nfmCenterHz, Data: loadFixture(t, "nfm_256k_vhf.cu8")},
	}}
}

func testOptions(r *conventionaltest.Runner) conventional.Options {
	return conventional.Options{
		Log:        zerolog.Nop(),
		Runner:     r,
		SampleRate: fixtureRate,
		Dwell:      100 * time.Millisecond,
		Hold:       50 * time.Millisecond,
		MaxHold:    250 * time.Millisecond,
		Settle:     5 * time.Millisecond,
		MinBackoff: 5 * time.Millisecond,
		MaxBackoff: 20 * time.Millisecond,
	}
}

func startScanner(t *testing.T, opts conventional.Options) *conventional.Scanner {
	t.Helper()

	s := conventional.New(opts)
	ctx, cancel := context.WithCancel(context.Background())

	var wg sync.WaitGroup

	wg.Go(func() { s.Run(ctx) })
	t.Cleanup(func() {
		cancel()
		wg.Wait()
	})

	return s
}

func receive(t *testing.T, ch <-chan scanner.AudioFrame) scanner.AudioFrame {
	t.Helper()

	select {
	case f, ok := <-ch:
		require.True(t, ok, "audio stream closed")

		return f
	case <-time.After(waitTimeout):
		require.FailNow(t, "no audio frame")
	}

	return scanner.AudioFrame{}
}

func rms(pcm []int16) float64 {
	var acc float64
	for _, v := range pcm {
		acc += float64(v) * float64(v)
	}

	return math.Sqrt(acc / float64(len(pcm)))
}

// TestScanner_fixturesEndToEnd replays the AM and NFM fixtures: the scan
// alternates between the two windows, the squelch opens only on the two
// carrying channels, and their audio arrives tagged with the right key.
func TestScanner_fixturesEndToEnd(t *testing.T) {
	r := fixtureRunner(t)
	s := startScanner(t, testOptions(r))
	plan := fixturePlan()

	audio, cancel, err := s.SubscribeAudio("")
	require.NoError(t, err)
	t.Cleanup(cancel)

	require.NoError(t, s.Scan(plan))

	got := map[string]int{}

	var seq uint32

	for got[plan.Channels[1].Channel.Key] < 10 || got[plan.Channels[3].Channel.Key] < 10 {
		f := receive(t, audio)
		got[f.ChannelKey]++

		assert.Greater(t, f.Sequence, seq, "sequence increases")
		seq = f.Sequence

		assert.Greater(t, rms(f.PCM[:]), 1000.0, "open squelch carries the tone")
	}

	assert.Zero(t, got[plan.Channels[0].Channel.Key], "quiet aviation channel stays squelched")
	assert.Zero(t, got[plan.Channels[2].Channel.Key], "quiet NFM channel stays squelched")

	st := s.Status()
	assert.Equal(t, 2, st.Windows)
	require.Len(t, st.Channels, 4)
	assert.True(t, st.Channels[0].LastHeard.IsZero())
	assert.False(t, st.Channels[1].LastHeard.IsZero())
	assert.True(t, st.Channels[2].LastHeard.IsZero())
	assert.False(t, st.Channels[3].LastHeard.IsZero())
	assert.Equal(t, 1, st.Channels[0].Window)
	assert.Equal(t, 2, st.Channels[3].Window)

	centers := map[uint64]int{}
	for _, st := range r.Starts() {
		centers[st.CenterHz]++

		assert.Equal(t, "rtl_sdr", st.Name)
		assert.Contains(t, st.Args, "scan")
		assert.Contains(t, st.Args, "256000")
	}

	assert.Positive(t, centers[aviationCenterHz])
	assert.Positive(t, centers[nfmCenterHz])
	assert.Len(t, centers, 2, "only the two planned windows are tuned")
}

// TestScanner_pinHoldsWindow subscribes to one channel: frames come only
// from it and the scan stops retuning to the other window.
func TestScanner_pinHoldsWindow(t *testing.T) {
	r := fixtureRunner(t)
	s := startScanner(t, testOptions(r))
	plan := fixturePlan()
	key := plan.Channels[3].Channel.Key

	audio, cancel, err := s.SubscribeAudio(key)
	require.NoError(t, err)
	t.Cleanup(cancel)

	require.NoError(t, s.Scan(plan))

	for range 5 {
		f := receive(t, audio)
		assert.Equal(t, key, f.ChannelKey)
		assert.Equal(t, uint64(nfmLiveHz), f.FrequencyHz)
	}

	before := len(r.Starts())

	for range 25 { // 0.5 s of audio: longer than dwell + max hold
		assert.Equal(t, key, receive(t, audio).ChannelKey)
	}

	for _, st := range r.Starts()[before:] {
		assert.Equal(t, uint64(nfmCenterHz), st.CenterHz, "pinned scan stays on the NFM window")
	}

	assert.Equal(t, scanner.ConventionalHolding, s.Status().State)
	cancel()

	_, ok := <-audio
	for ok {
		_, ok = <-audio
	}
}

func TestScanner_subscriberLimit(t *testing.T) {
	s := conventional.New(conventional.Options{Log: zerolog.Nop(), Runner: &conventionaltest.Runner{}})

	cancels := make([]func(), 0, conventional.MaxAudioSubscribers)

	for range conventional.MaxAudioSubscribers {
		_, c, err := s.SubscribeAudio("")
		require.NoError(t, err)

		cancels = append(cancels, c)
	}

	_, _, err := s.SubscribeAudio("")
	require.ErrorIs(t, err, conventional.ErrTooManyAudioSubscribers)

	cancels[0]()
	cancels[0]() // idempotent

	_, c, err := s.SubscribeAudio("x")
	require.NoError(t, err)
	c()
}

func TestScanner_unavailableAndIdle(t *testing.T) {
	opts := testOptions(fixtureRunner(t))
	opts.LookPath = func(string) (string, error) { return "", exec.ErrNotFound }
	s := startScanner(t, opts)

	assert.Equal(t, scanner.ConventionalIdle, s.Status().State)
	require.NoError(t, s.Scan(fixturePlan()))

	require.Eventually(t, func() bool { return s.Status().State == scanner.ConventionalUnavailable },
		waitTimeout, time.Millisecond)
	assert.Contains(t, s.Status().Detail, "rtl_sdr")

	require.NoError(t, s.Scan(scanner.ConventionalPlan{}))
	require.Eventually(t, func() bool { return s.Status().State == scanner.ConventionalIdle }, waitTimeout, time.Millisecond)

	ctx, cancel := context.WithTimeout(context.Background(), waitTimeout)
	defer cancel()

	require.NoError(t, s.WaitIdle(ctx))
	assert.Empty(t, s.Status().Channels)
}

func TestScanner_captureFailureRetries(t *testing.T) {
	r := &conventionaltest.Runner{StartErr: errors.New("usb_claim_interface error -6")}
	s := startScanner(t, testOptions(r))

	require.NoError(t, s.Scan(fixturePlan()))
	require.Eventually(t, func() bool { return len(r.Starts()) >= 3 }, waitTimeout, time.Millisecond)

	// The runner records a start before the scanner records its failure,
	// so a single Status read can land between the two. Wait for it.
	require.Eventually(t, func() bool {
		st := s.Status()

		return st.State == scanner.ConventionalError && strings.Contains(st.Detail, "usb_claim_interface")
	}, waitTimeout, time.Millisecond)
}

func TestScanner_waitIdleWhileCapturing(t *testing.T) {
	s := startScanner(t, testOptions(fixtureRunner(t)))

	audio, cancel, err := s.SubscribeAudio("")
	require.NoError(t, err)
	t.Cleanup(cancel)

	require.NoError(t, s.Scan(fixturePlan()))
	receive(t, audio)

	short, stop := context.WithTimeout(context.Background(), time.Millisecond)
	defer stop()

	require.Error(t, s.WaitIdle(short), "capturing")

	require.NoError(t, s.Scan(scanner.ConventionalPlan{}))

	ctx, cancelWait := context.WithTimeout(context.Background(), waitTimeout)
	defer cancelWait()

	require.NoError(t, s.WaitIdle(ctx))
	assert.Zero(t, s.Status().CenterHz)
}

func TestScanner_rejectsBadSerial(t *testing.T) {
	s := conventional.New(conventional.Options{Log: zerolog.Nop(), Runner: &conventionaltest.Runner{}})
	plan := fixturePlan()
	plan.Serial = "1"
	require.Error(t, s.Scan(plan), "numeric rtl serial would be read as an index")
}
