package main

import (
	"io"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseFlags_valid(t *testing.T) {
	dir := t.TempDir()

	opts, err := parseFlags([]string{
		"--static", dir,
		"--api", "http://127.0.0.1:18087",
		"--listen", "127.0.0.1:1",
		"--tls-listen", "127.0.0.1:2",
		"--luci-upstream", "http://127.0.0.1:18088",
	}, io.Discard)
	require.NoError(t, err)
	assert.Equal(t, options{
		static:       dir,
		listen:       "127.0.0.1:1",
		tlsListen:    "127.0.0.1:2",
		api:          "http://127.0.0.1:18087",
		luciUpstream: "http://127.0.0.1:18088",
	}, opts)
}

func TestParseFlags_errors(t *testing.T) {
	dir := t.TempDir()

	tests := []struct {
		name string
		args []string
		want string
	}{
		{name: "missing static", args: []string{"--api", "http://x"}, want: "--static is required"},
		{name: "missing api", args: []string{"--static", dir}, want: "--api is required"},
		{name: "static not a dir", args: []string{"--static", dir + "/nope", "--api", "http://x"}, want: "is not a directory"},
		{name: "unknown flag", args: []string{"--bogus"}, want: "parse flags"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := parseFlags(tc.args, io.Discard)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.want)
		})
	}
}

func TestNewConfig_mapsOptions(t *testing.T) {
	cfg := newConfig(options{
		static:       t.TempDir(),
		listen:       "127.0.0.1:18081",
		tlsListen:    "127.0.0.1:18443",
		api:          "http://127.0.0.1:18087",
		luciUpstream: "http://127.0.0.1:18088",
	})

	assert.Equal(t, "127.0.0.1:18081", cfg.GetOpenMANETFrontendHostPort())
	assert.Equal(t, "127.0.0.1:18443", cfg.GetOpenMANETFrontendTLSHostPort())
	assert.Equal(t, "http://127.0.0.1:18087", cfg.GetOpenMANETCommsAPIAddress())
	assert.True(t, cfg.GetFrontendLuCIProxyEnable())
	assert.Equal(t, "http://127.0.0.1:18088", cfg.GetFrontendLuCIProxyUpstream())
}

func TestNewConfig_luciProxyOffWithoutUpstream(t *testing.T) {
	cfg := newConfig(options{static: t.TempDir(), api: "http://127.0.0.1:18087"})

	assert.False(t, cfg.GetFrontendLuCIProxyEnable())
}

func TestRun_usageErrors(t *testing.T) {
	assert.Equal(t, 2, run([]string{"--api", "http://x"}))
	assert.Equal(t, 0, run([]string{"-h"}))
}
