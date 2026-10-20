//go:build integration

package handlers_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"connectrpc.com/connect"
	"connectrpc.com/validate"
	chanmigv1 "github.com/openmanet/openmanetd/internal/api/openmanet/chanmig/v1"
	chanmigconnect "github.com/openmanet/openmanetd/internal/api/openmanet/chanmig/v1/chanmigv1connect"
	radiov1 "github.com/openmanet/openmanetd/internal/api/openmanet/radio/v1"
	"github.com/openmanet/openmanetd/internal/openmanet/server/handlers"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/durationpb"
)

func newChanmigIntegrationClient(t *testing.T) chanmigconnect.ChannelMigrationServiceClient {
	t.Helper()

	srv := newTestServer(t)

	return chanmigconnect.NewChannelMigrationServiceClient(http.DefaultClient, srv.URL, connect.WithGRPCWeb())
}

func TestIntegration_GetChannelMigrationStatus(t *testing.T) {
	client := newChanmigIntegrationClient(t)

	resp, err := client.GetChannelMigrationStatus(context.Background(), &chanmigv1.GetChannelMigrationStatusRequest{})
	require.NoError(t, err)

	st := resp.GetStatus()
	assert.True(t, st.GetEnabled())
	assert.True(t, st.GetCoordinator())
	assert.Equal(t, uint64(0x0E1A), st.GetMeshId())
	assert.Equal(t, chanmigv1.MigrationPhase_MIGRATION_PHASE_BOOTING, st.GetPhase())
	require.Len(t, st.GetBands(), 2)
	assert.Equal(t, radiov1.Band_BAND_HALOW, st.GetBands()[0].GetBand())
	assert.Equal(t, radiov1.Band_BAND_5_GHZ, st.GetBands()[1].GetBand())
	require.Len(t, st.GetTrustedKeys(), 1)
	assert.Len(t, st.GetTrustedKeys()[0].GetPublicKey(), 32)
	assert.Len(t, st.GetTrustedKeys()[0].GetFingerprint(), 64)
}

func TestIntegration_ProposeChannelPlan(t *testing.T) {
	client := newChanmigIntegrationClient(t)

	resp, err := client.ProposeChannelPlan(context.Background(), &chanmigv1.ProposeChannelPlanRequest{
		Assignments: []*chanmigv1.ChannelAssignment{protoAssignment(radiov1.Band_BAND_HALOW, 3, 1)},
		Lead:        durationpb.New(time.Minute),
	})
	require.NoError(t, err)
	assert.Equal(t, uint64(1), resp.GetPlan().GetVersion())

	st, err := client.GetChannelMigrationStatus(context.Background(), &chanmigv1.GetChannelMigrationStatusRequest{})
	require.NoError(t, err)
	assert.Equal(t, uint64(1), st.GetStatus().GetCurrentPlan().GetVersion())
	assert.True(t, st.GetStatus().GetCoordinating())
	assert.Equal(t, uint32(3), st.GetStatus().GetBands()[0].GetTarget().GetChannel())
}

func TestIntegration_ProposeChannelPlan_errors(t *testing.T) {
	client := newChanmigIntegrationClient(t)

	tests := []struct {
		req  *chanmigv1.ProposeChannelPlanRequest
		name string
		want connect.Code
	}{
		{
			name: "outside allowed list (node policy)",
			req:  proposeRequest(time.Minute, protoAssignment(radiov1.Band_BAND_HALOW, 9, 1)),
			want: connect.CodeInvalidArgument,
		},
		{
			name: "lead above 10 minutes (validator)",
			req:  proposeRequest(11*time.Minute, protoAssignment(radiov1.Band_BAND_HALOW, 2, 1)),
			want: connect.CodeInvalidArgument,
		},
		{
			name: "negative lead",
			req:  proposeRequest(-time.Second, protoAssignment(radiov1.Band_BAND_HALOW, 2, 1)),
			want: connect.CodeInvalidArgument,
		},
		{
			name: "missing lead",
			req: &chanmigv1.ProposeChannelPlanRequest{
				Assignments: []*chanmigv1.ChannelAssignment{protoAssignment(radiov1.Band_BAND_HALOW, 2, 1)},
			},
			want: connect.CodeInvalidArgument,
		},
		{
			name: "no assignments",
			req:  proposeRequest(time.Minute),
			want: connect.CodeInvalidArgument,
		},
		{
			name: "duplicate band",
			req: proposeRequest(time.Minute,
				protoAssignment(radiov1.Band_BAND_HALOW, 2, 1), protoAssignment(radiov1.Band_BAND_HALOW, 3, 1)),
			want: connect.CodeInvalidArgument,
		},
		{
			name: "width not legal for band",
			req:  proposeRequest(time.Minute, protoAssignment(radiov1.Band_BAND_HALOW, 2, 20)),
			want: connect.CodeInvalidArgument,
		},
		{
			name: "unmanaged band",
			req:  proposeRequest(time.Minute, protoAssignment(radiov1.Band_BAND_6_GHZ, 5, 80)),
			want: connect.CodeInvalidArgument,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := client.ProposeChannelPlan(context.Background(), tc.req)

			var connectErr *connect.Error
			require.ErrorAs(t, err, &connectErr)
			assert.Equal(t, tc.want, connectErr.Code())
		})
	}
}

func TestIntegration_ChannelMigration_disabled(t *testing.T) {
	mux := http.NewServeMux()
	mux.Handle(chanmigconnect.NewChannelMigrationServiceHandler(&handlers.ChannelMigrationService{Log: zerolog.Nop()},
		connect.WithInterceptors(validate.NewInterceptor())))

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	client := chanmigconnect.NewChannelMigrationServiceClient(http.DefaultClient, srv.URL, connect.WithGRPCWeb())

	resp, err := client.GetChannelMigrationStatus(context.Background(), &chanmigv1.GetChannelMigrationStatusRequest{})
	require.NoError(t, err)
	assert.False(t, resp.GetStatus().GetEnabled())

	_, err = client.ProposeChannelPlan(context.Background(), proposeRequest(time.Minute, protoAssignment(radiov1.Band_BAND_HALOW, 2, 1)))

	var connectErr *connect.Error
	require.ErrorAs(t, err, &connectErr)
	assert.Equal(t, connect.CodeFailedPrecondition, connectErr.Code())
	assert.Contains(t, connectErr.Message(), "not running")
}
