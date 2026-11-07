package system

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRestartForNewAddress_ReloadsThenRestartsSelf(t *testing.T) {
	var calls []string

	run := func(_ context.Context, name, action string) error {
		calls = append(calls, name+" "+action)

		return nil
	}
	detach := func(script string) error {
		calls = append(calls, "detach "+script)

		return nil
	}

	require.NoError(t, restartForNewAddress(context.Background(), run, detach))
	assert.Equal(t, []string{"dnsmasq reload", "firewall reload", "detach " + selfRestartCmd}, calls)
}

func TestRestartForNewAddress_ReloadErrorStopsBeforeRestart(t *testing.T) {
	detached := false
	run := func(_ context.Context, _, _ string) error { return errors.New("boom") }
	detach := func(string) error {
		detached = true

		return nil
	}

	err := restartForNewAddress(context.Background(), run, detach)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "reload dnsmasq")
	assert.False(t, detached)
}

func TestRestartForNewAddress_DetachError(t *testing.T) {
	run := func(_ context.Context, _, _ string) error { return nil }
	detach := func(string) error { return errors.New("no shell") }

	err := restartForNewAddress(context.Background(), run, detach)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "restart openmanetd")
}
