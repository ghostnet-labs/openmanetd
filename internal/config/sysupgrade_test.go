package config

import (
	"testing"

	"github.com/spf13/viper"
	"github.com/stretchr/testify/assert"
)

func TestGetSysupgradeReleasesRepo(t *testing.T) {
	tests := []struct {
		name         string
		setValue     *string
		want         string
		wantRejected string
	}{
		{name: "default when unset", setValue: nil, want: DefaultSysupgradeReleasesRepo},
		{name: "default when empty", setValue: strPtr(""), want: DefaultSysupgradeReleasesRepo},
		{name: "default when whitespace", setValue: strPtr("   "), want: DefaultSysupgradeReleasesRepo},
		{name: "override", setValue: strPtr("ghostnet-labs/firmware"), want: "ghostnet-labs/firmware"},
		{name: "override trims whitespace", setValue: strPtr(" ghostnet-labs/firmware "), want: "ghostnet-labs/firmware"},
		{name: "override with dots and underscores", setValue: strPtr("my_org.x/fw.builds-2"), want: "my_org.x/fw.builds-2"},
		{name: "invalid missing slash", setValue: strPtr("firmware"), want: DefaultSysupgradeReleasesRepo, wantRejected: "firmware"},
		{name: "invalid extra segment", setValue: strPtr("a/b/c"), want: DefaultSysupgradeReleasesRepo, wantRejected: "a/b/c"},
		{name: "invalid empty owner", setValue: strPtr("/firmware"), want: DefaultSysupgradeReleasesRepo, wantRejected: "/firmware"},
		{name: "invalid empty name", setValue: strPtr("owner/"), want: DefaultSysupgradeReleasesRepo, wantRejected: "owner/"},
		{name: "invalid url", setValue: strPtr("https://github.com/a/b"), want: DefaultSysupgradeReleasesRepo, wantRejected: "https://github.com/a/b"},
		{name: "invalid characters", setValue: strPtr("own er/fw?x=1"), want: DefaultSysupgradeReleasesRepo, wantRejected: "own er/fw?x=1"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			v := viper.New()
			if tc.setValue != nil {
				v.Set("sysupgrade.releasesRepo", *tc.setValue)
			}

			cfg := NewWithoutWatch(v)

			assert.Equal(t, tc.want, cfg.GetSysupgradeReleasesRepo())
			assert.Equal(t, tc.wantRejected, cfg.GetSysupgradeReleasesRepoRejected())
		})
	}
}

func TestGetSysupgradeOnlineCheck(t *testing.T) {
	tests := []struct {
		name     string
		setValue *bool
		want     bool
	}{
		{name: "default when unset", setValue: nil, want: true},
		{name: "explicit true", setValue: boolPtr(true), want: true},
		{name: "explicit false", setValue: boolPtr(false), want: false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			v := viper.New()
			if tc.setValue != nil {
				v.Set("sysupgrade.onlineCheck", *tc.setValue)
			}

			assert.Equal(t, tc.want, NewWithoutWatch(v).GetSysupgradeOnlineCheck())
		})
	}
}

func TestSysupgradeConfig_FromYAML(t *testing.T) {
	cfg := newConfigFromYAML(t, "sysupgrade:\n  releasesRepo: ghostnet-labs/firmware\n  onlineCheck: false\n")

	assert.Equal(t, "ghostnet-labs/firmware", cfg.GetSysupgradeReleasesRepo())
	assert.Empty(t, cfg.GetSysupgradeReleasesRepoRejected())
	assert.False(t, cfg.GetSysupgradeOnlineCheck())
}

func TestSysupgradeConfig_Defaults(t *testing.T) {
	assert.Equal(t, "OpenMANET/firmware", DefaultSysupgradeReleasesRepo, "default repo must stay upstream")
	assert.True(t, DefaultSysupgradeOnlineCheck)

	cfg := newConfigFromYAML(t, "logLevel: info\n")
	assert.Equal(t, DefaultSysupgradeReleasesRepo, cfg.GetSysupgradeReleasesRepo())
	assert.True(t, cfg.GetSysupgradeOnlineCheck())
}
