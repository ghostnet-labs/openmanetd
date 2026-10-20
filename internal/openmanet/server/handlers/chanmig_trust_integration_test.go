//go:build integration

package handlers_test

import (
	"context"
	"crypto/ed25519"
	"net/http"
	"net/http/httptest"
	"testing"

	"connectrpc.com/connect"
	"connectrpc.com/validate"
	meshjoinv1 "github.com/openmanet/openmanetd/internal/api/openmanet/mesh_join/v1"
	meshjoinconnect "github.com/openmanet/openmanetd/internal/api/openmanet/mesh_join/v1/mesh_joinv1connect"
	"github.com/openmanet/openmanetd/internal/chanmig/platform"
	"github.com/openmanet/openmanetd/internal/config"
	"github.com/openmanet/openmanetd/internal/openmanet/server/handlers"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/emptypb"
)

// newChanmigMeshJoinClient serves MeshJoinService over the seeded wireless
// tree, a fake radio applier and a real ConfigChanmigTrust on cfg.
func newChanmigMeshJoinClient(t *testing.T, cfg *config.Config) meshjoinconnect.MeshJoinServiceClient {
	t.Helper()

	mux := http.NewServeMux()
	mux.Handle(meshjoinconnect.NewMeshJoinServiceHandler(&handlers.MeshJoinService{
		Log:          zerolog.Nop(),
		ConfigReader: newMeshJoinReader(),
		Radios:       newFakeRadioApplier(),
		RegDBPath:    regdbFixturePath(t),
		Hostname:     func() (string, error) { return "alpha", nil },
		ChanmigTrust: &handlers.ConfigChanmigTrust{Cfg: cfg},
	}, connect.WithInterceptors(validate.NewInterceptor())))

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	return meshjoinconnect.NewMeshJoinServiceClient(http.DefaultClient, srv.URL, connect.WithGRPCWeb())
}

func TestIntegration_GetMeshJoinQR_carriesChanmigTrust(t *testing.T) {
	cfg, keyPath := setupChanmigTrustConfig(t, true, 0x0E1A)
	writeKeyFile(t, keyPath, trustKey(1))

	resp, err := newChanmigMeshJoinClient(t, cfg).GetMeshJoinQR(context.Background(), &emptypb.Empty{})
	require.NoError(t, err)

	p := resp.GetPayload()
	require.Len(t, p.GetPlanAuthorityPublicKeys(), 1)
	assert.Equal(t, []byte(trustKey(1)), p.GetPlanAuthorityPublicKeys()[0])
	assert.Equal(t, uint64(0x0E1A), p.GetChanmigMeshId())
}

func TestIntegration_ApplyMeshJoin_adoptsChanmigTrust(t *testing.T) {
	cfg, keyPath := setupChanmigTrustConfig(t, false, 0)
	client := newChanmigMeshJoinClient(t, cfg)
	id := uint64(0x0E1A)

	_, err := client.ApplyMeshJoin(context.Background(), &meshjoinv1.ApplyMeshJoinRequest{
		Payload: joinPayloadWithTrust(&id, trustKey(1), trustKey(2)),
	})
	require.NoError(t, err)

	keys, err := platform.LoadPublicKeys(keyPath)
	require.NoError(t, err)
	require.Len(t, keys, 2)
	assert.Equal(t, id, cfg.GetChanmig().MeshID)

	// A second join from another mesh keeps the adopted trust.
	other := uint64(99)
	_, err = client.ApplyMeshJoin(context.Background(), &meshjoinv1.ApplyMeshJoinRequest{
		Payload: joinPayloadWithTrust(&other, trustKey(3)),
	})
	require.NoError(t, err)

	keys, err = platform.LoadPublicKeys(keyPath)
	require.NoError(t, err)
	require.Len(t, keys, 2)
	assert.True(t, keys[0].Equal(trustKey(1)))
	assert.Equal(t, id, cfg.GetChanmig().MeshID)
}

func TestIntegration_ApplyMeshJoin_rejectsBadChanmigKeys(t *testing.T) {
	nine := make([]ed25519.PublicKey, 0, 9)
	for i := range 9 {
		nine = append(nine, trustKey(byte(i+1)))
	}

	tests := []struct {
		name string
		keys []ed25519.PublicKey
	}{
		{name: "short key", keys: []ed25519.PublicKey{trustKey(1)[:31]}},
		{name: "duplicate keys", keys: []ed25519.PublicKey{trustKey(1), trustKey(1)}},
		{name: "more than 8 keys", keys: nine},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cfg, keyPath := setupChanmigTrustConfig(t, false, 0)

			_, err := newChanmigMeshJoinClient(t, cfg).ApplyMeshJoin(context.Background(), &meshjoinv1.ApplyMeshJoinRequest{
				Payload: joinPayloadWithTrust(nil, tc.keys...),
			})

			var connectErr *connect.Error
			require.ErrorAs(t, err, &connectErr)
			assert.Equal(t, connect.CodeInvalidArgument, connectErr.Code())
			assert.NoFileExists(t, keyPath)
		})
	}
}
