package network

import (
	"testing"

	"github.com/digineo/go-uci/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// meshFixture builds a wireless config from (section, type, options)
// rows in order, so GetSections returns them in that order.
func meshFixture(t *testing.T, rows ...meshFixtureRow) *mockConfigReader {
	t.Helper()

	reader := &mockConfigReader{
		data:         map[string]map[string]map[string][]string{},
		sectionTypes: map[string]map[string]string{"wireless": {}},
	}

	for _, row := range rows {
		require.NoError(t, reader.AddSection("wireless", row.section, row.secType))

		for opt, val := range row.opts {
			require.NoError(t, reader.SetType("wireless", row.section, opt, uci.TypeOption, val))
		}
	}

	return reader
}

type meshFixtureRow struct {
	section string
	secType string
	opts    map[string]string
}

func TestPrimaryMeshCredentials(t *testing.T) {
	halowDev := meshFixtureRow{"radio0", "wifi-device", map[string]string{"type": "morse", "band": "s1g"}}
	wifiDev := meshFixtureRow{"radio1", "wifi-device", map[string]string{"type": "mac80211", "band": "2g"}}
	halowMesh := meshFixtureRow{"default_radio0", "wifi-iface", map[string]string{
		"device": "radio0", "mode": "mesh", "mesh_id": "halow-mesh", "key": "halow-key",
	}}
	wifiMesh := meshFixtureRow{"batmesh1_radio1", "wifi-iface", map[string]string{
		"device": "radio1", "mode": "mesh", "mesh_id": "wifi-mesh", "key": "wifi-key",
	}}
	wifiAP := meshFixtureRow{"default_radio1", "wifi-iface", map[string]string{
		"device": "radio1", "mode": "ap", "ssid": "openmanet", "key": "ap-key",
	}}
	emptyMesh := meshFixtureRow{"blank", "wifi-iface", map[string]string{"device": "radio1", "mode": "mesh"}}

	tests := []struct {
		name    string
		rows    []meshFixtureRow
		skip    string
		wantID  string
		wantKey string
	}{
		{"HaLow link wins over an earlier Wi-Fi mesh", []meshFixtureRow{halowDev, wifiDev, wifiMesh, halowMesh}, "", "halow-mesh", "halow-key"},
		{"no HaLow link falls back to the first mesh", []meshFixtureRow{wifiDev, wifiAP, wifiMesh}, "", "wifi-mesh", "wifi-key"},
		{"skip ignores the named section", []meshFixtureRow{halowDev, wifiDev, halowMesh, wifiMesh}, "default_radio0", "wifi-mesh", "wifi-key"},
		{"mesh without mesh_id is ignored", []meshFixtureRow{wifiDev, emptyMesh, wifiMesh}, "", "wifi-mesh", "wifi-key"},
		{"AP sections never count", []meshFixtureRow{wifiDev, wifiAP}, "", "", ""},
		{"empty config", nil, "", "", ""},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			reader := meshFixture(t, tc.rows...)

			id, key, err := PrimaryMeshCredentials(reader, tc.skip)
			require.NoError(t, err)
			assert.Equal(t, tc.wantID, id)
			assert.Equal(t, tc.wantKey, key)
		})
	}
}
