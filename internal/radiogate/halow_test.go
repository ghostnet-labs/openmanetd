package radiogate_test

import (
	"context"
	"errors"
	"testing"

	"github.com/openmanet/openmanetd/internal/iwinfo"
	"github.com/openmanet/openmanetd/internal/network"
	"github.com/openmanet/openmanetd/internal/radiogate"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func halowReader() *fakeConfigReader {
	return &fakeConfigReader{
		data: map[string]map[string]string{
			"radio0": {"type": "mac80211", "band": "2g"},
			"radio1": {"type": "mac80211", "band": "5g"},
			"radio2": {"type": "Morse"},
		},
		types: map[string]string{
			"radio0":         "wifi-device",
			"radio1":         "wifi-device",
			"radio2":         "wifi-device",
			"default_radio2": "wifi-iface",
		},
	}
}

func halowStatus() *fakeWirelessStatus {
	return &fakeWirelessStatus{status: map[string]*network.WirelessRadioStatus{
		"radio0": {Up: true, Interfaces: []network.WirelessRadioInterface{{Section: "a", Ifname: "phy0-mesh0"}}},
		"radio2": {Up: true, Interfaces: []network.WirelessRadioInterface{{Section: "default_radio2", Ifname: "wlan0"}}},
	}}
}

func halowIwinfo() *fakeIwinfo {
	return &fakeIwinfo{info: map[string]*iwinfo.InterfaceInfo{
		"phy0-mesh0": {PHY: "phy0"},
		"wlan0":      {PHY: "phy2"},
		"halow1":     {PHY: "phy3"},
	}}
}

func TestHaLowResolver_PHYs(t *testing.T) {
	r := &radiogate.HaLowResolver{Config: halowReader(), Status: halowStatus(), Iwinfo: halowIwinfo()}

	phys, err := r.PHYs(context.Background())
	require.NoError(t, err)
	assert.Equal(t, []string{"phy2"}, phys, "only the type=morse device's wiphy")
}

func TestHaLowResolver_PHYs_netdevsFromConfig(t *testing.T) {
	r := &radiogate.HaLowResolver{
		Config:  halowReader(),
		Status:  halowStatus(),
		Iwinfo:  halowIwinfo(),
		Netdevs: func() []string { return []string{"halow1", "", "wlan0"} },
	}

	phys, err := r.PHYs(context.Background())
	require.NoError(t, err)
	assert.Equal(t, []string{"phy2", "phy3"}, phys, "sorted and de-duplicated")
}

func TestHaLowResolver_PHYs_unresolvedIsEmpty(t *testing.T) {
	tests := []struct {
		name string
		r    *radiogate.HaLowResolver
	}{
		{
			name: "morse interface down",
			r: &radiogate.HaLowResolver{
				Config: halowReader(),
				Status: &fakeWirelessStatus{status: map[string]*network.WirelessRadioStatus{}},
				Iwinfo: halowIwinfo(),
			},
		},
		{
			name: "no morse device",
			r: &radiogate.HaLowResolver{
				Config: &fakeConfigReader{types: map[string]string{"radio0": "wifi-device"}},
				Status: &fakeWirelessStatus{err: errors.New("must not be called")},
				Iwinfo: halowIwinfo(),
			},
		},
		{name: "nothing wired", r: &radiogate.HaLowResolver{}},
		{
			name: "iwinfo missing the ifname",
			r: &radiogate.HaLowResolver{
				Config: halowReader(), Status: halowStatus(),
				Iwinfo: &fakeIwinfo{info: map[string]*iwinfo.InterfaceInfo{}},
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			phys, err := tc.r.PHYs(context.Background())
			require.NoError(t, err)
			assert.Empty(t, phys)
		})
	}
}

func TestHaLowResolver_PHYs_errors(t *testing.T) {
	tests := []struct {
		name string
		r    *radiogate.HaLowResolver
		want string
	}{
		{
			name: "uci",
			r:    &radiogate.HaLowResolver{Config: &fakeConfigReader{sectsErr: errors.New("boom")}, Status: halowStatus()},
			want: "wifi-device",
		},
		{
			name: "ubus",
			r:    &radiogate.HaLowResolver{Config: halowReader(), Status: &fakeWirelessStatus{err: errors.New("boom")}},
			want: "wireless status",
		},
		{
			name: "iwinfo",
			r:    &radiogate.HaLowResolver{Config: halowReader(), Status: halowStatus(), Iwinfo: &fakeIwinfo{err: errors.New("boom")}},
			want: "iwinfo",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := tc.r.PHYs(context.Background())
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.want)
		})
	}
}

// TestHaLowResolver_feedsProvider checks the resolver end to end: the
// Morse wiphy from UCI type=morse becomes the S1G PHY of the model.
func TestHaLowResolver_feedsProvider(t *testing.T) {
	r := &radiogate.HaLowResolver{Config: halowReader(), Status: halowStatus(), Iwinfo: halowIwinfo()}
	p := &radiogate.Provider{
		Exec:      func(context.Context) ([]byte, error) { return fixture(t, nodeFixture), nil },
		HaLowPHYs: r.PHYs,
	}

	m, err := p.Model(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "s1g", string(capacityOf(t, m, "phy2").Bands[0]))
}
