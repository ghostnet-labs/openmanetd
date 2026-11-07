package config

import (
	"strings"
	"testing"

	"github.com/spf13/viper"
)

func TestGetFrontendLuCIProxyEnable(t *testing.T) {
	tests := []struct {
		name     string
		setValue *bool
		want     bool
	}{
		{name: "returns configured true", setValue: boolPtr(true), want: true},
		{name: "returns configured false", setValue: boolPtr(false), want: false},
		{name: "defaults to off when not set", setValue: nil, want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			v := viper.New()
			if tt.setValue != nil {
				v.Set("frontend.luciProxy.enable", *tt.setValue)
			}

			cfg := NewWithoutWatch(v)
			if got := cfg.GetFrontendLuCIProxyEnable(); got != tt.want {
				t.Errorf("GetFrontendLuCIProxyEnable() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestGetFrontendLuCIProxyUpstream(t *testing.T) {
	tests := []struct {
		name     string
		setValue *string
		want     string
	}{
		{name: "returns configured value", setValue: strPtr("http://127.0.0.1:8000"), want: "http://127.0.0.1:8000"},
		{name: "trims surrounding whitespace", setValue: strPtr("  http://127.0.0.1:8000 "), want: "http://127.0.0.1:8000"},
		{name: "returns default when empty", setValue: strPtr(""), want: DefaultFrontendLuCIProxyUpstream},
		{name: "returns default when not set", setValue: nil, want: DefaultFrontendLuCIProxyUpstream},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			v := viper.New()
			if tt.setValue != nil {
				v.Set("frontend.luciProxy.upstream", *tt.setValue)
			}

			cfg := NewWithoutWatch(v)
			if got := cfg.GetFrontendLuCIProxyUpstream(); got != tt.want {
				t.Errorf("GetFrontendLuCIProxyUpstream() = %v, want %v", got, tt.want)
			}
		})
	}
}

// The keys must load from the nested YAML shape documented in
// example_config.yml, not only from v.Set with a dotted path.
func TestFrontendLuCIProxy_FromYAML(t *testing.T) {
	const doc = `
frontend:
  luciProxy:
    enable: true
    upstream: http://127.0.0.1:8080
`

	v := viper.New()
	v.SetConfigType("yaml")

	if err := v.ReadConfig(strings.NewReader(doc)); err != nil {
		t.Fatalf("ReadConfig: %v", err)
	}

	cfg := NewWithoutWatch(v)
	if !cfg.GetFrontendLuCIProxyEnable() {
		t.Error("GetFrontendLuCIProxyEnable() = false, want true")
	}

	if got := cfg.GetFrontendLuCIProxyUpstream(); got != "http://127.0.0.1:8080" {
		t.Errorf("GetFrontendLuCIProxyUpstream() = %q, want %q", got, "http://127.0.0.1:8080")
	}
}

func TestDefaultFrontendLuCIProxy(t *testing.T) {
	if DefaultFrontendLuCIProxyEnable {
		t.Error("DefaultFrontendLuCIProxyEnable must be false so the proxy is opt-in")
	}

	if DefaultFrontendLuCIProxyUpstream != "http://127.0.0.1:80" {
		t.Errorf("DefaultFrontendLuCIProxyUpstream = %q, want http://127.0.0.1:80", DefaultFrontendLuCIProxyUpstream)
	}
}
