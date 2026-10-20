package server

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/openmanet/openmanetd/internal/config"
	"github.com/openmanet/openmanetd/internal/radiocap"
	"github.com/openmanet/openmanetd/internal/radiogate"
	"github.com/rs/zerolog"
	"github.com/spf13/viper"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func iwListFixture(t *testing.T) []byte {
	t.Helper()

	_, here, _, ok := runtime.Caller(0)
	require.True(t, ok)

	root := here
	for range 4 {
		root = filepath.Dir(root)
	}

	data, err := os.ReadFile(filepath.Join(root, "testfixtures", "radiocap", "node_mt7916_halow.txt"))
	require.NoError(t, err)

	return data
}

// newTestRadioGate builds the gate from v with `iw list` and HaLow
// identification replaced by fixtures (no UCI/ubus/iwinfo is wired).
func newTestRadioGate(t *testing.T, v *viper.Viper, log zerolog.Logger) *radiogate.Gate {
	t.Helper()

	g := newRadioCapacityGate(config.NewWithoutWatch(v), log, nil, nil, nil)

	p, ok := g.Source.(*radiogate.Provider)
	require.True(t, ok)

	out := iwListFixture(t)
	p.Exec = func(context.Context) ([]byte, error) { return out, nil }
	p.HaLowPHYs = func(context.Context) ([]string, error) { return []string{"phy2"}, nil }

	return g
}

func halowRequest(t *testing.T) radiogate.Request {
	t.Helper()

	ch, ok := radiogate.UCIChannel(radiocap.BandS1G, "28", 8, 0)
	require.True(t, ok)

	return radiogate.Request{
		Origin: "test",
		Requested: []radiocap.Demand{{
			ID: "default_radio3", Purpose: radiocap.PurposeMesh, Role: radiocap.RoleMesh, Channel: ch,
		}},
	}
}

func TestNewRadioCapacityGate_defaultIsAdvisory(t *testing.T) {
	g := newTestRadioGate(t, viper.New(), zerolog.Nop())

	require.NoError(t, g.Evaluate(context.Background(), halowRequest(t)), "advisory never blocks")

	recs := g.Recent()
	require.Len(t, recs, 1)
	assert.Equal(t, radiogate.ModeAdvisory, recs[0].Mode)
	assert.Equal(t, radiocap.ReasonUnqualifiedOnly, recs[0].Decision.Reason, "no PHY is qualified by default")
}

func TestNewRadioCapacityGate_readsConfig(t *testing.T) {
	v := viper.New()
	v.Set("radio.capacityGate", "enforce")

	g := newTestRadioGate(t, v, zerolog.Nop())

	var denied *radiogate.DeniedError
	require.ErrorAs(t, g.Evaluate(context.Background(), halowRequest(t)), &denied)

	// Qualification evidence and policy come from config.
	v.Set("radio.capacity.qualifiedPhys", []string{"phy2"})
	v.Set("radio.capacityGate", "enforce")

	g = newTestRadioGate(t, v, zerolog.Nop())
	require.NoError(t, g.Evaluate(context.Background(), halowRequest(t)))

	v2 := viper.New()
	v2.Set("radio.capacityGate", "enforce")
	v2.Set("radio.capacity.allowUnqualified", true)

	g = newTestRadioGate(t, v2, zerolog.Nop())
	require.NoError(t, g.Evaluate(context.Background(), halowRequest(t)))
	assert.True(t, g.Recent()[0].Decision.UsesUnqualified)

	v3 := viper.New()
	v3.Set("radio.capacityGate", "off")

	g = newTestRadioGate(t, v3, zerolog.Nop())
	require.NoError(t, g.Evaluate(context.Background(), halowRequest(t)))
	assert.Empty(t, g.Recent())
}

func TestNewRadioCapacityGate_warnsOnRejectedMode(t *testing.T) {
	var logs bytes.Buffer

	v := viper.New()
	v.Set("radio.capacityGate", "strict")

	g := newTestRadioGate(t, v, zerolog.New(&logs))

	assert.Contains(t, logs.String(), "radio.capacityGate")
	assert.Contains(t, logs.String(), "strict")

	require.NoError(t, g.Evaluate(context.Background(), halowRequest(t)))
	assert.Equal(t, radiogate.ModeAdvisory, g.Recent()[0].Mode)
}
