//go:build integration

package handlers_test

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"connectrpc.com/connect"
	radiov1 "github.com/openmanet/openmanetd/internal/api/openmanet/radio/v1"
	radioconnect "github.com/openmanet/openmanetd/internal/api/openmanet/radio/v1/radiov1connect"
	"github.com/openmanet/openmanetd/internal/openmanet/server/handlers"
	"github.com/openmanet/openmanetd/internal/radiocap"
	"github.com/openmanet/openmanetd/internal/radiogate"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newIntegrationRadioService backs RadioService with a real advisory gate
// over the node fixture (phy0 qualified) that has recorded one HaLow mesh
// rejection. Path selection is not wired, as in production.
func newIntegrationRadioService(t *testing.T) *handlers.RadioService {
	t.Helper()

	gate := newCapacityGate(t, radiogate.ModeAdvisory, radiogate.Settings{QualifiedPHYs: []string{"phy0"}}, nil)

	provider, ok := gate.Source.(*radiogate.Provider)
	require.True(t, ok)

	require.NoError(t, gate.Evaluate(context.Background(), radiogate.Request{
		Origin: "test",
		Radios: []string{"radio3"},
		Requested: []radiocap.Demand{{
			ID: "default_radio3", Purpose: radiocap.PurposeMesh, Role: radiocap.RoleMesh, Channel: s1gChannel(42, 1000),
		}},
	}))

	return &handlers.RadioService{Log: zerolog.Nop(), Gate: gate, Inventory: provider}
}

func newRadioClient(t *testing.T) radioconnect.RadioServiceClient {
	t.Helper()

	srv := newTestServer(t)

	return radioconnect.NewRadioServiceClient(http.DefaultClient, srv.URL, connect.WithGRPCWeb())
}

func TestIntegration_GetRadioCapacity(t *testing.T) {
	client := newRadioClient(t)

	resp, err := client.GetRadioCapacity(context.Background(), &radiov1.GetRadioCapacityRequest{})
	require.NoError(t, err)

	c := resp.GetCapacity()
	assert.Equal(t, radiov1.CapacityGateMode_CAPACITY_GATE_MODE_ADVISORY, c.GetGateMode())
	assert.NotNil(t, c.GetGeneratedAt())
	assert.NotNil(t, c.GetInventoryUpdatedAt())
	require.Len(t, c.GetPhys(), 3)

	var halow int

	for _, p := range c.GetPhys() {
		assert.NotEmpty(t, p.GetBands(), p.GetName())
		assert.Positive(t, p.GetCapacity().GetMaxInterfaces(), p.GetName())
		assert.True(t, p.GetAvailable(), p.GetName())

		if p.GetIsHalow() {
			halow++

			assert.Equal(t, "phy2", p.GetName())
		}

		assert.Equal(t, p.GetName() == "phy0", p.GetQualified(), p.GetName())
	}

	assert.Equal(t, 1, halow)
}

func TestIntegration_ListAdmissionDecisions(t *testing.T) {
	client := newRadioClient(t)

	resp, err := client.ListAdmissionDecisions(context.Background(), &radiov1.ListAdmissionDecisionsRequest{Limit: 64})
	require.NoError(t, err)
	require.Len(t, resp.GetDecisions(), 1)

	d := resp.GetDecisions()[0]
	assert.Equal(t, radiov1.CapacityGateMode_CAPACITY_GATE_MODE_ADVISORY, d.GetGateMode())
	assert.Equal(t, radiov1.AdmissionOutcome_ADMISSION_OUTCOME_REJECT, d.GetOutcome())
	assert.Equal(t, radiov1.AdmissionReason_ADMISSION_REASON_UNQUALIFIED_ONLY, d.GetReason())
	assert.Equal(t, radiov1.PhysicalQualification_PHYSICAL_QUALIFICATION_NOT_EVALUATED, d.GetPhysicalQualification())
	assert.NotEmpty(t, d.GetExplanation())
	require.Len(t, d.GetRequested(), 1)
	assert.Equal(t, radiov1.DemandRole_DEMAND_ROLE_MESH, d.GetRequested()[0].GetRole())
}

func TestIntegration_ListPathDecisions_notWired(t *testing.T) {
	client := newRadioClient(t)

	resp, err := client.ListPathDecisions(context.Background(), &radiov1.ListPathDecisionsRequest{
		Limit:        10,
		Destination:  "node-7",
		TrafficClass: radiov1.TrafficClass_TRAFFIC_CLASS_VOICE,
	})
	require.NoError(t, err)
	assert.Empty(t, resp.GetDecisions())
}

func TestIntegration_RadioService_validation(t *testing.T) {
	client := newRadioClient(t)
	ctx := context.Background()

	admission := []struct {
		name    string
		limit   uint32
		wantErr bool
	}{
		{name: "zero", limit: 0, wantErr: true},
		{name: "minimum", limit: 1},
		{name: "maximum", limit: 64},
		{name: "above maximum", limit: 65, wantErr: true},
	}

	for _, tc := range admission {
		t.Run("admission "+tc.name, func(t *testing.T) {
			_, err := client.ListAdmissionDecisions(ctx, &radiov1.ListAdmissionDecisionsRequest{Limit: tc.limit})
			if !tc.wantErr {
				require.NoError(t, err)

				return
			}

			assertConnectCode(t, err, connect.CodeInvalidArgument)
		})
	}

	paths := []struct {
		name    string
		req     *radiov1.ListPathDecisionsRequest
		wantErr bool
	}{
		{name: "zero limit", req: &radiov1.ListPathDecisionsRequest{}, wantErr: true},
		{name: "limit above maximum", req: &radiov1.ListPathDecisionsRequest{Limit: 65}, wantErr: true},
		{name: "destination at max length", req: &radiov1.ListPathDecisionsRequest{Limit: 1, Destination: strings.Repeat("a", 128)}},
		{
			name:    "destination too long",
			req:     &radiov1.ListPathDecisionsRequest{Limit: 1, Destination: strings.Repeat("a", 129)},
			wantErr: true,
		},
		{
			name:    "undefined traffic class",
			req:     &radiov1.ListPathDecisionsRequest{Limit: 1, TrafficClass: radiov1.TrafficClass(99)},
			wantErr: true,
		},
	}

	for _, tc := range paths {
		t.Run("paths "+tc.name, func(t *testing.T) {
			_, err := client.ListPathDecisions(ctx, tc.req)
			if !tc.wantErr {
				require.NoError(t, err)

				return
			}

			assertConnectCode(t, err, connect.CodeInvalidArgument)
		})
	}
}
