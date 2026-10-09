package openmanet

import (
	"context"
	"testing"

	"github.com/openmanet/openmanetd/internal/config"
	"github.com/openmanet/openmanetd/internal/hardware"
	"github.com/rs/zerolog"
	"github.com/spf13/viper"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestHardwareAPI_nilManagerIsNilInterface(t *testing.T) {
	assert.Nil(t, hardwareAPI(nil), "a typed nil must not reach the handler interface")

	m := hardware.New(hardware.Options{Log: zerolog.Nop()})
	t.Cleanup(m.Close)
	assert.NotNil(t, hardwareAPI(m))
}

func TestStartHardwareManager_disabled(t *testing.T) {
	v := viper.New()
	v.Set("hardware.enable", false)

	assert.Nil(t, startHardwareManager(context.Background(), config.NewWithoutWatch(v)))
}

func TestWaitHardwareManager(t *testing.T) {
	waitHardwareManager(nil, zerolog.Nop()) // no manager: returns at once

	m := hardware.New(hardware.Options{Log: zerolog.Nop()})
	ctx, cancel := context.WithCancel(context.Background())

	go m.Run(ctx)

	cancel()

	waitHardwareManager(m, zerolog.Nop())

	select {
	case <-m.Done():
	default:
		require.Fail(t, "manager still running after wait")
	}
}
