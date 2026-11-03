package conventional_test

import (
	"testing"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/openmanet/openmanetd/internal/scanner"
	"github.com/openmanet/openmanetd/internal/scanner/conventional"
	"github.com/openmanet/openmanetd/internal/scanner/conventional/conventionaltest"
	"github.com/openmanet/openmanetd/internal/scanner/sites"
)

// benchBlock is one rtl_sdr pipe read (64 KiB of cu8).
const benchBlock = 32768

// eightChannelConsumer builds a 2.048 MS/s window demodulating eight NFM
// channels in parallel, with a subscriber attached so open-squelch frames
// are fanned out too.
func eightChannelConsumer(tb testing.TB) (func([]complex64), []complex64) {
	tb.Helper()

	s := conventional.New(conventional.Options{Log: zerolog.Nop(), Runner: &conventionaltest.Runner{}})

	plan := scanner.ConventionalPlan{Serial: "scan"}
	carriers := make([]conventionaltest.Carrier, 0, 8)

	for i := range 8 {
		off := -700_000 + i*200_000
		plan.Channels = append(plan.Channels,
			fixtureChannel(uint64(155_000_000+off), sites.CategoryConventional, sites.ModeNFM, 12500))
		carriers = append(carriers, conventionaltest.Carrier{OffsetHz: float64(off), ToneHz: 1000, Amplitude: 0.1, DeviationHz: 2500})
	}

	consume, err := s.ConsumerForTest(plan)
	require.NoError(tb, err)

	audio, cancel, err := s.SubscribeAudio("")
	require.NoError(tb, err)
	tb.Cleanup(cancel)

	go func() {
		for range audio { //nolint:revive // drain
		}
	}()

	iq := conventionaltest.Synthesize(conventional.DefaultSampleRate, float64(benchBlock)/conventional.DefaultSampleRate, carriers, -40, 1)

	return consume, iq
}

func TestConsume_zeroAlloc(t *testing.T) {
	consume, iq := eightChannelConsumer(t)
	consume(iq) // warm up: scratch growth

	allocs := testing.AllocsPerRun(20, func() { consume(iq) })
	assert.Zero(t, allocs)
}

// BenchmarkConsume_8ch measures one 16 ms capture block (32768 samples at
// 2.048 MS/s) through eight parallel NFM channels.
func BenchmarkConsume_8ch(b *testing.B) {
	consume, iq := eightChannelConsumer(b)
	consume(iq)

	b.ReportAllocs()
	b.SetBytes(int64(len(iq)) * 2) // cu8 bytes represented

	for b.Loop() {
		consume(iq)
	}
}
