package handlers_test

import (
	"context"
	"testing"

	jamv1 "github.com/openmanet/openmanetd/internal/api/openmanet/jamdetect/v1"
	"github.com/openmanet/openmanetd/internal/jamdetect"
	"github.com/openmanet/openmanetd/internal/openmanet/server/handlers"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestJamDetectService_GetJamStatus_disabled(t *testing.T) {
	svc := &handlers.JamDetectService{Log: zerolog.Nop()}

	res, err := svc.GetJamStatus(context.Background(), &jamv1.GetJamStatusRequest{})
	require.NoError(t, err)
	assert.False(t, res.GetEnabled())
	assert.Nil(t, res.GetLocal())
	assert.Empty(t, res.GetRecent())
}

func TestJamDetectService_GetJamStatus_jammed(t *testing.T) {
	det, feed := newJamDetector(t)
	feed(false, 20)
	feed(true, jamdetect.DefaultJamSamples)

	svc := &handlers.JamDetectService{Log: zerolog.Nop(), Detector: det}

	res, err := svc.GetJamStatus(context.Background(), &jamv1.GetJamStatusRequest{})
	require.NoError(t, err)
	assert.True(t, res.GetEnabled())

	local := res.GetLocal()
	assert.Equal(t, "mast-1", local.GetHostname())
	assert.Equal(t, "02:00:00:00:00:01", local.GetPrimaryMac())
	require.Len(t, local.GetRadios(), 1)
	assert.Equal(t, jamv1.JamState_JAM_STATE_JAMMED, local.GetRadios()[0].GetState())
	require.Len(t, local.GetActiveEvents(), 1)
	assert.Equal(t, jamv1.JamState_JAM_STATE_JAMMED, local.GetActiveEvents()[0].GetState())
	assert.Equal(t, int32(-80), local.GetActiveEvents()[0].GetPeakNoiseDbm())

	require.Len(t, res.GetRecent(), 1)
	assert.Equal(t, local.GetActiveEvents()[0].GetId(), res.GetRecent()[0].GetId())
	assert.Empty(t, res.GetRemote())
}
