//go:build integration

package handlers_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"connectrpc.com/connect"
	"connectrpc.com/validate"
	jamv1 "github.com/openmanet/openmanetd/internal/api/openmanet/jamdetect/v1"
	"github.com/openmanet/openmanetd/internal/api/openmanet/jamdetect/v1/jamdetectv1connect"
	"github.com/openmanet/openmanetd/internal/jamdetect"
	"github.com/openmanet/openmanetd/internal/openmanet/server/handlers"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newJamDetectIntegration(t *testing.T, det handlers.JamDetector) jamdetectv1connect.JamDetectServiceClient {
	t.Helper()

	mux := http.NewServeMux()
	mux.Handle(jamdetectv1connect.NewJamDetectServiceHandler(&handlers.JamDetectService{
		Log:      zerolog.Nop(),
		Detector: det,
	}, connect.WithInterceptors(validate.NewInterceptor())))

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	return jamdetectv1connect.NewJamDetectServiceClient(http.DefaultClient, srv.URL, connect.WithGRPCWeb())
}

func TestJamDetectIntegration_disabled(t *testing.T) {
	client := newJamDetectIntegration(t, nil)

	res, err := client.GetJamStatus(context.Background(), &jamv1.GetJamStatusRequest{})
	require.NoError(t, err)
	assert.False(t, res.GetEnabled())

	stream, err := client.StreamJamEvents(context.Background(), &jamv1.StreamJamEventsRequest{})
	require.NoError(t, err)
	require.True(t, stream.Receive())
	assert.Empty(t, stream.Msg().GetEvents())
	assert.False(t, stream.Receive())
	require.NoError(t, stream.Err())
}

func TestJamDetectIntegration_streamEpisode(t *testing.T) {
	det, feed := newJamDetector(t)
	feed(false, 20)

	client := newJamDetectIntegration(t, det)

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	stream, err := client.StreamJamEvents(ctx, &jamv1.StreamJamEventsRequest{})
	require.NoError(t, err)

	require.True(t, stream.Receive(), "initial open-episode message")
	assert.Empty(t, stream.Msg().GetEvents())

	feed(true, jamdetect.DefaultJamSamples)

	want := []jamv1.JamState{jamv1.JamState_JAM_STATE_SUSPECTED, jamv1.JamState_JAM_STATE_JAMMED}
	for _, st := range want {
		require.True(t, stream.Receive())
		require.Len(t, stream.Msg().GetEvents(), 1)
		assert.Equal(t, st, stream.Msg().GetEvents()[0].GetState())
		assert.Equal(t, jamv1.RadioBand_RADIO_BAND_5G, stream.Msg().GetEvents()[0].GetRadio().GetBand())
	}

	res, err := client.GetJamStatus(ctx, &jamv1.GetJamStatusRequest{})
	require.NoError(t, err)
	require.Len(t, res.GetLocal().GetActiveEvents(), 1)

	cancel()
}
