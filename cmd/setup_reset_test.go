package cmd

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/openmanet/openmanetd/internal/config"
	"github.com/spf13/viper"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeSetupResetConfig is a hand-rolled setupResetConfig.
type fakeSetupResetConfig struct {
	mu         sync.Mutex // protects the fields below
	resetCalls int
	resetErr   error
}

func (f *fakeSetupResetConfig) PersistSetupReset() error {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.resetCalls++

	return f.resetErr
}

func (f *fakeSetupResetConfig) getResetCalls() int {
	f.mu.Lock()
	defer f.mu.Unlock()

	return f.resetCalls
}

func TestResetSetup_PersistsAndClearsLuci(t *testing.T) {
	cfg := &fakeSetupResetConfig{}
	clearCalls := 0
	clearLuci := func() error {
		require.Equal(t, 1, cfg.getResetCalls(), "config.yml must be reset before UCI")

		clearCalls++

		return nil
	}

	var out bytes.Buffer

	require.NoError(t, resetSetup(cfg, clearLuci, &out))

	assert.Equal(t, 1, cfg.getResetCalls())
	assert.Equal(t, 1, clearCalls)
	assert.Contains(t, out.String(), "setup.enabled=true")
	assert.Contains(t, out.String(), "setup.complete=false")
	assert.Contains(t, out.String(), "auth.enable=false")
	assert.Contains(t, out.String(), "luci.wizard.used=0")
	assert.Contains(t, out.String(), "/etc/init.d/openmanetd restart")
}

func TestResetSetup_PersistFailureSkipsLuci(t *testing.T) {
	cfg := &fakeSetupResetConfig{resetErr: errors.New("disk full")}
	clearCalls := 0
	clearLuci := func() error {
		clearCalls++

		return nil
	}

	var out bytes.Buffer

	err := resetSetup(cfg, clearLuci, &out)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "setup-reset failed")
	assert.Contains(t, err.Error(), "disk full")
	assert.Zero(t, clearCalls, "UCI must not be touched when config.yml could not be reset")
	assert.Empty(t, out.String())
}

func TestResetSetup_LuciFailureReported(t *testing.T) {
	cfg := &fakeSetupResetConfig{}
	clearLuci := func() error { return errors.New("uci commit failed") }

	var out bytes.Buffer

	err := resetSetup(cfg, clearLuci, &out)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "cleared config.yml flags but failed to clear luci.wizard.used")
	assert.Contains(t, err.Error(), "uci commit failed")
	assert.Equal(t, 1, cfg.getResetCalls())
	assert.Empty(t, out.String())
}

// TestResetSetup_StockConfigReopensWizard runs resetSetup against a real
// *config.Config over a stock config.yml (no setup section at all, so
// setup.enabled resolves to the default false). Before GHO-75 the reset
// left setup.enabled unset and the wizard stayed hidden.
func TestResetSetup_StockConfigReopensWizard(t *testing.T) {
	cfgPath := filepath.Join(t.TempDir(), "config.yml")
	require.NoError(t, os.WriteFile(cfgPath, []byte("logLevel: info\n"), 0o600))

	v := viper.New()
	v.SetConfigFile(cfgPath)
	require.NoError(t, v.ReadInConfig())

	cfg := config.NewWithoutWatch(v)
	require.False(t, cfg.GetSetupEnabled(), "precondition: wizard disabled on a stock config")

	var out bytes.Buffer

	require.NoError(t, resetSetup(cfg, func() error { return nil }, &out))

	reread := viper.New()
	reread.SetConfigFile(cfgPath)
	require.NoError(t, reread.ReadInConfig())

	reloaded := config.NewWithoutWatch(reread)
	assert.True(t, reloaded.GetSetupEnabled())
	assert.False(t, reloaded.GetSetupComplete())
	assert.False(t, reloaded.GetAuthEnable())
	assert.Equal(t, "info", reread.GetString("logLevel"))
}
