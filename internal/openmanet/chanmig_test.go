package openmanet

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"errors"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/openmanet/go-alfred"
	"github.com/rs/zerolog"
	"github.com/spf13/viper"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	netv1 "github.com/openmanet/openmanetd/internal/api/openmanet/network/v1"
	"github.com/openmanet/openmanetd/internal/chanmig"
	"github.com/openmanet/openmanetd/internal/chanmig/platform"
	"github.com/openmanet/openmanetd/internal/config"
	"github.com/openmanet/openmanetd/internal/mgmt"
)

var errFakeAlfred = errors.New("fake alfred failure")

type chanmigFixture struct {
	cfg  config.ChanmigConfig
	deps chanmigDeps
	uci  *fakeWirelessUCI
	pub  ed25519.PublicKey
	priv ed25519.PrivateKey
	dir  string
}

func newChanmigFixture(t *testing.T) *chanmigFixture {
	t.Helper()

	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)

	dir := t.TempDir()
	pubPath := filepath.Join(dir, "authority.pub")
	require.NoError(t, os.WriteFile(pubPath, []byte(base64.StdEncoding.EncodeToString(pub)+"\n"), 0o600))

	uci := newFakeWirelessUCI()
	uci.addDevice("radio0", "s1g", "28", "2 MHz")

	return &chanmigFixture{
		pub:  pub,
		priv: priv,
		dir:  dir,
		uci:  uci,
		cfg: config.ChanmigConfig{
			Enable:         true,
			MeshID:         9,
			PublicKeyFile:  pubPath,
			PrivateKeyFile: filepath.Join(dir, "authority.key"),
			StoreFile:      filepath.Join(dir, "plans.bin"),
			HaLow:          config.ChanmigBandConfig{Initial: "28/2", Allowed: []string{"28/2", "30/4"}},
		},
		deps: chanmigDeps{
			gossip: alfredGossip{client: &fakeAlfredClient{}},
			reader: uci,
			reload: func(context.Context) error { return nil },
			mac: func() (net.HardwareAddr, error) {
				return net.HardwareAddr{0x02, 0, 0, 0, 0, 0x01}, nil
			},
			clock: platform.SystemClock{Probe: func() bool { return false }},
			log:   zerolog.Nop(),
			boot:  1,
		},
	}
}

func (f *chanmigFixture) writePrivateKey(t *testing.T, priv ed25519.PrivateKey, mode os.FileMode) {
	t.Helper()

	der, err := x509.MarshalPKCS8PrivateKey(priv)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(f.cfg.PrivateKeyFile, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}), mode))
	require.NoError(t, os.Chmod(f.cfg.PrivateKeyFile, mode))
}

func TestChanmigDataType_doesNotCollide(t *testing.T) {
	for v := range netv1.DataType_name {
		assert.NotEqual(t, int32(platform.AlfredDataType), v,
			"alfred data type %d is already used by openmanet.network.v1.DataType", platform.AlfredDataType)
	}
}

func TestStartChanmig_offByDefault(t *testing.T) {
	cfg := config.NewWithoutWatch(viper.New())

	assert.Nil(t, startChanmig(context.Background(), cfg, nil, zerolog.Nop()))
	assert.Nil(t, startChanmig(context.Background(), cfg, &mgmt.ManagementConfig{}, zerolog.Nop()))
}

func TestStartChanmig_requiresAlfred(t *testing.T) {
	v := viper.New()
	v.Set("chanmig.enable", true)
	cfg := config.NewWithoutWatch(v)

	assert.Nil(t, startChanmig(context.Background(), cfg, nil, zerolog.Nop()), "alfred disabled")
	assert.Nil(t, startChanmig(context.Background(), cfg, &mgmt.ManagementConfig{}, zerolog.Nop()),
		"alfred client not started")
}

func TestBuildChanmig_member(t *testing.T) {
	f := newChanmigFixture(t)

	svc, err := buildChanmig(f.cfg, f.deps)
	require.NoError(t, err)

	_, err = svc.Node().Propose(context.Background(), nil, time.Minute)
	require.ErrorIs(t, err, chanmig.ErrNoSigner, "no private key file means not a coordinator")
}

func TestBuildChanmig_coordinator(t *testing.T) {
	f := newChanmigFixture(t)
	f.writePrivateKey(t, f.priv, 0o600)

	svc, err := buildChanmig(f.cfg, f.deps)
	require.NoError(t, err)

	ctx := context.Background()
	require.NoError(t, svc.Node().Start(ctx))

	sp, err := svc.Node().Propose(ctx, []chanmig.Assignment{{Band: chanmig.BandHaLow, Channel: 30, WidthMHz: 4}}, time.Minute)
	require.NoError(t, err)
	assert.Equal(t, uint64(9), sp.Plan.MeshID)
	assert.Equal(t, chanmig.NodeID(0x020000000001), sp.Plan.SignerID, "node id derives from the MAC")
}

func TestBuildChanmig_nodeIDOverride(t *testing.T) {
	f := newChanmigFixture(t)
	f.writePrivateKey(t, f.priv, 0o600)
	f.cfg.NodeID = 77
	f.deps.mac = func() (net.HardwareAddr, error) { return nil, errFakeAlfred }

	svc, err := buildChanmig(f.cfg, f.deps)
	require.NoError(t, err)

	ctx := context.Background()
	require.NoError(t, svc.Node().Start(ctx))

	sp, err := svc.Node().Propose(ctx, []chanmig.Assignment{{Band: chanmig.BandHaLow, Channel: 30, WidthMHz: 4}}, time.Minute)
	require.NoError(t, err)
	assert.Equal(t, chanmig.NodeID(77), sp.Plan.SignerID)
}

func TestBuildChanmig_errors(t *testing.T) {
	_, otherPriv, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)

	cases := []struct {
		mutate func(t *testing.T, f *chanmigFixture)
		name   string
		want   string
	}{
		{
			name:   "no bands",
			mutate: func(_ *testing.T, f *chanmigFixture) { f.cfg.HaLow = config.ChanmigBandConfig{} },
			want:   "no band has an initial assignment",
		},
		{
			name:   "bad assignment",
			mutate: func(_ *testing.T, f *chanmigFixture) { f.cfg.HaLow.Initial = "28/3" },
			want:   "halow initial",
		},
		{
			name:   "missing public key",
			mutate: func(_ *testing.T, f *chanmigFixture) { f.cfg.PublicKeyFile = filepath.Join(f.dir, "absent.pub") },
			want:   "public key",
		},
		{
			name: "untrusted private key",
			mutate: func(t *testing.T, f *chanmigFixture) {
				t.Helper()
				f.writePrivateKey(t, otherPriv, 0o600)
			},
			want: "does not match any trusted public key",
		},
		{
			name: "world-readable private key",
			mutate: func(t *testing.T, f *chanmigFixture) {
				t.Helper()
				f.writePrivateKey(t, f.priv, 0o644)
			},
			want: "group or world",
		},
		{
			name: "mac lookup fails",
			mutate: func(_ *testing.T, f *chanmigFixture) {
				f.deps.mac = func() (net.HardwareAddr, error) { return nil, errFakeAlfred }
			},
			want: "node id",
		},
		{
			name: "invalid timing override",
			mutate: func(_ *testing.T, f *chanmigFixture) {
				f.cfg.Timing.Dwell = time.Second // below two advert intervals
			},
			want: "dwell",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newChanmigFixture(t)
			tc.mutate(t, f)

			svc, err := buildChanmig(f.cfg, f.deps)
			require.ErrorContains(t, err, tc.want)
			assert.Nil(t, svc)
		})
	}
}

func TestBuildChanmig_runAndWait(t *testing.T) {
	f := newChanmigFixture(t)
	f.uci.addDevice("radio2", "5g", "36", "VHT80")
	f.cfg.WiFi5G = config.ChanmigBandConfig{Radio: "radio2", Initial: "149/80"}

	client := &fakeAlfredClient{}
	f.deps.gossip = alfredGossip{client: client}

	svc, err := buildChanmig(f.cfg, f.deps)
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	go svc.Run(ctx)

	t.Cleanup(func() {
		cancel()
		<-svc.Done()
	})

	// Start tunes the 5 GHz radio to its provisioned channel and the node
	// begins advertising under the chanmig alfred data type.
	require.Eventually(t, func() bool { return len(client.setCalls()) > 0 }, 5*time.Second, time.Millisecond)

	ch, _ := f.uci.Get("wireless", "radio2", "channel")
	assert.Equal(t, []string{"149"}, ch)

	cancel()
	waitChanmig(svc, zerolog.Nop())

	select {
	case <-svc.Done():
	default:
		require.Fail(t, "chanmig still running after wait")
	}

	waitChanmig(nil, zerolog.Nop()) // disabled: returns at once
}

func TestAlfredGossip(t *testing.T) {
	src := net.HardwareAddr{2, 0, 0, 0, 0, 9}
	client := &fakeAlfredClient{records: []alfred.Record{
		{Source: src, Version: platform.AlfredDataVersion, Data: []byte("current")},
		{Source: src, Version: platform.AlfredDataVersion + 1, Data: []byte("future format")},
	}}
	g := alfredGossip{client: client}

	require.NoError(t, g.Publish([]byte("bundle")))
	assert.Equal(t, []fakeAlfredSet{{
		dataType: platform.AlfredDataType, version: platform.AlfredDataVersion, payload: []byte("bundle"),
	}}, client.setCalls())

	recs, err := g.Fetch()
	require.NoError(t, err)
	assert.Equal(t, []platform.GossipRecord{{Source: src, Data: []byte("current")}}, recs)
	assert.Equal(t, []uint8{platform.AlfredDataType}, client.requestCalls())

	client.setErr = errFakeAlfred
	require.ErrorIs(t, g.Publish([]byte("x")), errFakeAlfred)

	client.requestErr = errFakeAlfred
	_, err = g.Fetch()
	require.ErrorIs(t, err, errFakeAlfred)
}

func TestChanmigTiming_overlay(t *testing.T) {
	base := platform.AlfredTiming()

	assert.Equal(t, base, chanmigTiming(base, config.ChanmigTiming{}), "zero overrides keep the defaults")

	got := chanmigTiming(base, config.ChanmigTiming{AdvertInterval: 12 * time.Second, Stagger: 10 * time.Second})
	want := base
	want.AdvertInterval = 12 * time.Second
	want.Stagger = 10 * time.Second
	assert.Equal(t, want, got)

	full := config.ChanmigTiming{
		AdvertInterval: 1, PeerWindow: 2, VerifyBound: 3, FallbackBound: 4, PeerLossTimeout: 5,
		ColdBootGrace: 6, Dwell: 7, AbortMargin: 8, MaxLead: 9, RadioRetry: 10, Stagger: 11,
	}
	assert.Equal(t, chanmig.Timing{
		AdvertInterval: 1, PeerWindow: 2, VerifyBound: 3, FallbackBound: 4, PeerLossTimeout: 5,
		ColdBootGrace: 6, Dwell: 7, AbortMargin: 8, MaxLead: 9, RadioRetry: 10, Stagger: 11,
	}, chanmigTiming(base, full), "every field is overlaid")
}
