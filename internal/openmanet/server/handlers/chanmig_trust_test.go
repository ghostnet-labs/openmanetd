package handlers_test

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	meshjoinv1 "github.com/openmanet/openmanetd/internal/api/openmanet/mesh_join/v1"
	"github.com/openmanet/openmanetd/internal/chanmig/platform"
	"github.com/openmanet/openmanetd/internal/config"
	"github.com/openmanet/openmanetd/internal/meshjoin"
	"github.com/openmanet/openmanetd/internal/openmanet/server/handlers"
	"github.com/spf13/viper"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/emptypb"
)

// trustKey returns a deterministic Ed25519 public key.
func trustKey(seed byte) ed25519.PublicKey {
	s := make([]byte, ed25519.SeedSize)
	for i := range s {
		s[i] = seed
	}

	pub, _ := ed25519.NewKeyFromSeed(s).Public().(ed25519.PublicKey)

	return pub
}

// setupChanmigTrustConfig writes a config whose chanmig.publicKeyFile
// points into a temp dir and returns it with the key file path. meshID 0
// leaves chanmig.meshId out.
func setupChanmigTrustConfig(t *testing.T, enable bool, meshID uint64) (*config.Config, string) {
	t.Helper()

	dir := t.TempDir()
	keyPath := filepath.Join(dir, "chanmig", "authority.pub")

	var b strings.Builder

	b.WriteString("chanmig:\n  enable: " + strconv.FormatBool(enable) + "\n  publicKeyFile: " + keyPath + "\n")

	if meshID != 0 {
		b.WriteString("  meshId: " + strconv.FormatUint(meshID, 10) + "\n")
	}

	cfgPath := filepath.Join(dir, "config.yml")
	require.NoError(t, os.WriteFile(cfgPath, []byte(b.String()), 0o600))

	v := viper.New()
	v.SetConfigFile(cfgPath)
	require.NoError(t, v.ReadInConfig())

	return config.NewWithoutWatch(v), keyPath
}

func writeKeyFile(t *testing.T, path string, keys ...ed25519.PublicKey) {
	t.Helper()

	var b strings.Builder
	for _, k := range keys {
		b.WriteString(base64.StdEncoding.EncodeToString(k) + "\n")
	}

	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(t, os.WriteFile(path, []byte(b.String()), 0o600))
}

// ── ConfigChanmigTrust ────────────────────────────────────────────────────────

func TestConfigChanmigTrust_JoinTrust(t *testing.T) {
	t.Run("disabled", func(t *testing.T) {
		cfg, keyPath := setupChanmigTrustConfig(t, false, 5)
		writeKeyFile(t, keyPath, trustKey(1))

		_, ok, err := (&handlers.ConfigChanmigTrust{Cfg: cfg}).JoinTrust()
		require.NoError(t, err)
		assert.False(t, ok)
	})

	t.Run("enabled", func(t *testing.T) {
		cfg, keyPath := setupChanmigTrustConfig(t, true, 5)
		writeKeyFile(t, keyPath, trustKey(1), trustKey(2))

		trust, ok, err := (&handlers.ConfigChanmigTrust{Cfg: cfg}).JoinTrust()
		require.NoError(t, err)
		require.True(t, ok)
		require.Len(t, trust.Keys, 2)
		assert.True(t, trust.Keys[1].Equal(trustKey(2)))
		assert.Equal(t, uint64(5), trust.MeshID)
		assert.True(t, trust.HasMeshID)
	})

	t.Run("enabled without key file", func(t *testing.T) {
		cfg, _ := setupChanmigTrustConfig(t, true, 5)

		_, ok, err := (&handlers.ConfigChanmigTrust{Cfg: cfg}).JoinTrust()
		require.Error(t, err)
		assert.False(t, ok)
	})
}

func TestConfigChanmigTrust_InstallJoinTrust_writesKeysAndMeshID(t *testing.T) {
	cfg, keyPath := setupChanmigTrustConfig(t, false, 0)
	store := &handlers.ConfigChanmigTrust{Cfg: cfg}

	res, err := store.InstallJoinTrust(handlers.JoinTrust{
		Keys: []ed25519.PublicKey{trustKey(1), trustKey(2)}, MeshID: 42, HasMeshID: true,
	})
	require.NoError(t, err)
	assert.Equal(t, keyPath, res.KeyFile)
	assert.True(t, res.KeysWritten)
	assert.True(t, res.MeshIDSet)

	keys, err := platform.LoadPublicKeys(keyPath)
	require.NoError(t, err)
	require.Len(t, keys, 2)
	assert.True(t, keys[0].Equal(trustKey(1)))

	fi, err := os.Stat(keyPath)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o644), fi.Mode().Perm())

	assert.Equal(t, uint64(42), cfg.GetChanmig().MeshID)

	data, err := os.ReadFile(cfg.GetConfigFilePath())
	require.NoError(t, err)
	assert.Contains(t, string(data), "meshId: 42")
}

func TestConfigChanmigTrust_InstallJoinTrust_keepsExisting(t *testing.T) {
	cfg, keyPath := setupChanmigTrustConfig(t, true, 9)
	writeKeyFile(t, keyPath, trustKey(7))

	res, err := (&handlers.ConfigChanmigTrust{Cfg: cfg}).InstallJoinTrust(handlers.JoinTrust{
		Keys: []ed25519.PublicKey{trustKey(1)}, MeshID: 42, HasMeshID: true,
	})
	require.NoError(t, err)
	assert.False(t, res.KeysWritten)
	assert.False(t, res.MeshIDSet)

	keys, err := platform.LoadPublicKeys(keyPath)
	require.NoError(t, err)
	require.Len(t, keys, 1)
	assert.True(t, keys[0].Equal(trustKey(7)), "existing keys are never overwritten")
	assert.Equal(t, uint64(9), cfg.GetChanmig().MeshID, "existing mesh ID is kept")
}

func TestConfigChanmigTrust_InstallJoinTrust_meshIDSetWhenKeysExist(t *testing.T) {
	cfg, keyPath := setupChanmigTrustConfig(t, false, 0)
	writeKeyFile(t, keyPath, trustKey(1))

	res, err := (&handlers.ConfigChanmigTrust{Cfg: cfg}).InstallJoinTrust(handlers.JoinTrust{
		Keys: []ed25519.PublicKey{trustKey(1)}, MeshID: 42, HasMeshID: true,
	})
	require.NoError(t, err)
	assert.False(t, res.KeysWritten)
	assert.True(t, res.MeshIDSet)
	assert.Equal(t, uint64(42), cfg.GetChanmig().MeshID)
}

func TestConfigChanmigTrust_InstallJoinTrust_noKeysOrNoMeshID(t *testing.T) {
	cfg, keyPath := setupChanmigTrustConfig(t, false, 0)
	store := &handlers.ConfigChanmigTrust{Cfg: cfg}

	res, err := store.InstallJoinTrust(handlers.JoinTrust{MeshID: 42, HasMeshID: true})
	require.NoError(t, err)
	assert.Equal(t, handlers.JoinTrustResult{}, res, "trust without keys changes nothing")
	assert.NoFileExists(t, keyPath)
	assert.Zero(t, cfg.GetChanmig().MeshID)

	res, err = store.InstallJoinTrust(handlers.JoinTrust{Keys: []ed25519.PublicKey{trustKey(1)}})
	require.NoError(t, err)
	assert.True(t, res.KeysWritten)
	assert.False(t, res.MeshIDSet)
	assert.Zero(t, cfg.GetChanmig().MeshID)
}

func TestConfigChanmigTrust_InstallJoinTrust_errors(t *testing.T) {
	t.Run("key file not writable", func(t *testing.T) {
		cfg, keyPath := setupChanmigTrustConfig(t, false, 0)
		// A regular file where the key directory should be.
		require.NoError(t, os.WriteFile(filepath.Dir(keyPath), nil, 0o600))

		_, err := (&handlers.ConfigChanmigTrust{Cfg: cfg}).InstallJoinTrust(handlers.JoinTrust{
			Keys: []ed25519.PublicKey{trustKey(1)}, MeshID: 42, HasMeshID: true,
		})
		require.Error(t, err)
		assert.Zero(t, cfg.GetChanmig().MeshID, "mesh ID is not set when the keys failed")
	})

	t.Run("config not persistable", func(t *testing.T) {
		cfg := config.NewWithoutWatch(viper.New())

		_, err := (&handlers.ConfigChanmigTrust{Cfg: cfg}).InstallJoinTrust(handlers.JoinTrust{
			Keys: []ed25519.PublicKey{trustKey(1)}, MeshID: 42, HasMeshID: true,
		})
		require.Error(t, err)
	})
}

// ── MeshJoinService channel-migration trust ───────────────────────────────────

func TestGetMeshJoinQR_IncludesChanmigTrust(t *testing.T) {
	svc := newTestMeshJoinService(newMeshJoinReader())
	svc.ChanmigTrust = &fakeChanmigTrust{enabled: true, trust: handlers.JoinTrust{
		Keys:   []ed25519.PublicKey{trustKey(1), trustKey(2), trustKey(1)},
		MeshID: 0x0E1A, HasMeshID: true,
	}}

	resp, err := svc.GetMeshJoinQR(context.Background(), &emptypb.Empty{})
	require.NoError(t, err)

	p := resp.GetPayload()
	require.Len(t, p.GetPlanAuthorityPublicKeys(), 2, "duplicate keys are dropped")
	assert.Equal(t, []byte(trustKey(1)), p.GetPlanAuthorityPublicKeys()[0])
	assert.Equal(t, []byte(trustKey(2)), p.GetPlanAuthorityPublicKeys()[1])
	require.NotNil(t, p.ChanmigMeshId)
	assert.Equal(t, uint64(0x0E1A), p.GetChanmigMeshId())

	decoded, err := meshjoin.Decode(resp.GetPayloadText())
	require.NoError(t, err)
	assert.Len(t, decoded.GetPlanAuthorityPublicKeys(), 2)
	assert.Equal(t, uint64(0x0E1A), decoded.GetChanmigMeshId())
}

func TestGetMeshJoinQR_ChanmigTrustOmitted(t *testing.T) {
	tests := []struct {
		trust handlers.ChanmigTrust
		name  string
	}{
		{name: "no trust store", trust: nil},
		{name: "chanmig disabled", trust: &fakeChanmigTrust{trust: handlers.JoinTrust{Keys: []ed25519.PublicKey{trustKey(1)}}}},
		{name: "key file unreadable", trust: &fakeChanmigTrust{enabled: true, trustErr: errors.New("no key file")}},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			svc := newTestMeshJoinService(newMeshJoinReader())
			svc.ChanmigTrust = tc.trust

			resp, err := svc.GetMeshJoinQR(context.Background(), &emptypb.Empty{})
			require.NoError(t, err, "the QR is still produced")
			assert.Empty(t, resp.GetPayload().GetPlanAuthorityPublicKeys())
			assert.Nil(t, resp.GetPayload().ChanmigMeshId)
		})
	}
}

func joinPayloadWithTrust(meshID *uint64, keys ...ed25519.PublicKey) *meshjoinv1.MeshJoinPayload {
	p := joinPayload()
	for _, k := range keys {
		p.PlanAuthorityPublicKeys = append(p.PlanAuthorityPublicKeys, k)
	}

	p.ChanmigMeshId = meshID

	return p
}

func TestApplyMeshJoin_InstallsChanmigTrust(t *testing.T) {
	svc, _ := newTestMeshJoinApplyService(t, newMeshJoinReader())
	trust := &fakeChanmigTrust{}
	svc.ChanmigTrust = trust

	id := uint64(0x0E1A)

	_, err := svc.ApplyMeshJoin(context.Background(), &meshjoinv1.ApplyMeshJoinRequest{
		Payload: joinPayloadWithTrust(&id, trustKey(1), trustKey(2)),
	})
	require.NoError(t, err)

	installs := trust.installs()
	require.Len(t, installs, 1)
	require.Len(t, installs[0].Keys, 2)
	assert.True(t, installs[0].Keys[1].Equal(trustKey(2)))
	assert.True(t, installs[0].HasMeshID)
	assert.Equal(t, id, installs[0].MeshID)

	_, err = svc.ApplyMeshJoin(context.Background(), &meshjoinv1.ApplyMeshJoinRequest{
		Payload: joinPayloadWithTrust(nil, trustKey(3)),
	})
	require.NoError(t, err)

	installs = trust.installs()
	require.Len(t, installs, 2)
	assert.False(t, installs[1].HasMeshID, "an absent mesh ID is not installed as zero")
}

func TestApplyMeshJoin_ChanmigTrustSkipped(t *testing.T) {
	t.Run("payload without keys", func(t *testing.T) {
		svc, _ := newTestMeshJoinApplyService(t, newMeshJoinReader())
		trust := &fakeChanmigTrust{}
		svc.ChanmigTrust = trust

		id := uint64(5)

		_, err := svc.ApplyMeshJoin(context.Background(), &meshjoinv1.ApplyMeshJoinRequest{Payload: joinPayloadWithTrust(&id)})
		require.NoError(t, err)
		assert.Empty(t, trust.installs())
	})

	t.Run("radio apply failed", func(t *testing.T) {
		svc, applier := newTestMeshJoinApplyService(t, newMeshJoinReader())
		applier.applyErr = errors.New("uci commit failed")
		trust := &fakeChanmigTrust{}
		svc.ChanmigTrust = trust

		_, err := svc.ApplyMeshJoin(context.Background(), &meshjoinv1.ApplyMeshJoinRequest{
			Payload: joinPayloadWithTrust(nil, trustKey(1)),
		})
		require.Error(t, err)
		assert.Empty(t, trust.installs(), "trust is adopted only after the join succeeds")
	})

	t.Run("install failure does not fail the join", func(t *testing.T) {
		svc, _ := newTestMeshJoinApplyService(t, newMeshJoinReader())
		trust := &fakeChanmigTrust{installErr: errors.New("read-only filesystem")}
		svc.ChanmigTrust = trust

		resp, err := svc.ApplyMeshJoin(context.Background(), &meshjoinv1.ApplyMeshJoinRequest{
			Payload: joinPayloadWithTrust(nil, trustKey(1)),
		})
		require.NoError(t, err)
		assert.Len(t, resp.GetRadios(), 2)
		assert.Len(t, trust.installs(), 1)
	})
}

func TestApplyMeshJoin_ChanmigTrustEndToEnd(t *testing.T) {
	cfg, keyPath := setupChanmigTrustConfig(t, false, 0)
	svc, _ := newTestMeshJoinApplyService(t, newMeshJoinReader())
	svc.ChanmigTrust = &handlers.ConfigChanmigTrust{Cfg: cfg}

	id := uint64(77)

	_, err := svc.ApplyMeshJoin(context.Background(), &meshjoinv1.ApplyMeshJoinRequest{
		Payload: joinPayloadWithTrust(&id, trustKey(4)),
	})
	require.NoError(t, err)

	keys, err := platform.LoadPublicKeys(keyPath)
	require.NoError(t, err)
	require.Len(t, keys, 1)
	assert.True(t, keys[0].Equal(trustKey(4)))
	assert.Equal(t, id, cfg.GetChanmig().MeshID)
}
