//go:build integration

package handlers_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"connectrpc.com/connect"
	"connectrpc.com/validate"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	coveragev1 "github.com/openmanet/openmanetd/internal/api/openmanet/coverage/v1"
	"github.com/openmanet/openmanetd/internal/api/openmanet/coverage/v1/coveragev1connect"
	"github.com/openmanet/openmanetd/internal/openmanet/server/handlers"
)

func newCoverageIntegration(t *testing.T, svc *handlers.CoverageService) coveragev1connect.CoverageServiceClient {
	t.Helper()

	mux := http.NewServeMux()
	mux.Handle(coveragev1connect.NewCoverageServiceHandler(svc, connect.WithInterceptors(validate.NewInterceptor())))

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	return coveragev1connect.NewCoverageServiceClient(http.DefaultClient, srv.URL, connect.WithGRPCWeb())
}

func TestCoverageIntegration_map(t *testing.T) {
	client := newCoverageIntegration(t, &handlers.CoverageService{
		Log: zerolog.Nop(), Cfg: coverageConfig(t, true), Map: newCoverageFake(),
	})

	res, err := client.GetCoverageMap(context.Background(), &coveragev1.GetCoverageMapRequest{})
	require.NoError(t, err)

	assert.True(t, res.GetEnabled())
	assert.Equal(t, uint32(50), res.GetLocal().GetCellMeters())
	assert.Len(t, res.GetRemote(), 1)
}

func TestCoverageIntegration_disabled(t *testing.T) {
	client := newCoverageIntegration(t, &handlers.CoverageService{Log: zerolog.Nop(), Cfg: coverageConfig(t, false)})

	res, err := client.GetCoverageMap(context.Background(), &coveragev1.GetCoverageMapRequest{})
	require.NoError(t, err)
	assert.False(t, res.GetEnabled())
}
