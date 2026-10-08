package network

import (
	"context"
	"errors"
	"testing"
	"testing/fstest"

	"github.com/openmanet/openmanetd/internal/iwinfo"
	"github.com/stretchr/testify/assert"
)

type wsUbusExecutor struct {
	output []byte
	err    error
}

func (m *wsUbusExecutor) Execute(_ context.Context, _ ...string) ([]byte, error) {
	return m.output, m.err
}

func TestGetWirelessStatus_MultipleRadios(t *testing.T) {
	fixture := `{
  "radio2": {
    "up": true,
    "disabled": false,
    "interfaces": [
      {
        "section": "default_radio2",
        "ifname": "phy0-ap0",
        "config": {
          "mode": "ap"
        }
      }
    ]
  },
  "radio3": {
    "up": true,
    "disabled": false,
    "interfaces": [
      {
        "section": "default_radio3",
        "ifname": "phy1-mesh0",
        "config": {
          "mode": "mesh"
        }
      }
    ]
  }
}`
	provider := NewUbusWirelessStatusProvider(&wsUbusExecutor{output: []byte(fixture)})

	result, err := provider.GetWirelessStatus(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(result) != 2 {
		t.Fatalf("expected 2 radios, got %d", len(result))
	}

	r2 := result["radio2"]
	if r2 == nil {
		t.Fatal("expected radio2 in result")
	}

	if !r2.Up {
		t.Error("expected radio2 to be up")
	}

	if len(r2.Interfaces) != 1 {
		t.Fatalf("expected 1 interface for radio2, got %d", len(r2.Interfaces))
	}

	if r2.Interfaces[0].Ifname != "phy0-ap0" {
		t.Errorf("expected ifname phy0-ap0, got %s", r2.Interfaces[0].Ifname)
	}

	if r2.Interfaces[0].Section != "default_radio2" {
		t.Errorf("expected section default_radio2, got %s", r2.Interfaces[0].Section)
	}

	if r2.Interfaces[0].Config.Mode != "ap" {
		t.Errorf("expected mode ap, got %s", r2.Interfaces[0].Config.Mode)
	}

	r3 := result["radio3"]
	if r3 == nil {
		t.Fatal("expected radio3 in result")
	}

	if r3.Interfaces[0].Ifname != "phy1-mesh0" {
		t.Errorf("expected ifname phy1-mesh0, got %s", r3.Interfaces[0].Ifname)
	}

	if r3.Interfaces[0].Config.Mode != "mesh" {
		t.Errorf("expected mode mesh, got %s", r3.Interfaces[0].Config.Mode)
	}
}

func TestResolveWirelessRadioHardwareName_interfaces(t *testing.T) {
	t.Parallel()

	status := map[string]*WirelessRadioStatus{
		"radio2": {Interfaces: []WirelessRadioInterface{
			{Ifname: "missing"},
			{Ifname: "phy1-mesh0"},
		}},
	}
	info := map[string]*iwinfo.InterfaceInfo{
		"phy1-mesh0": {Hardware: iwinfo.HardwareInfo{Name: "MediaTek MT7915AN"}},
	}

	sysfs := fstest.MapFS{}

	assert.Equal(t, "MediaTek MT7915AN", resolveWirelessRadioHardwareName(sysfs, "radio2", status, info))
	assert.Empty(t, resolveWirelessRadioHardwareName(sysfs, "missing", status, info))
	assert.Empty(t, resolveWirelessRadioHardwareName(sysfs, "radio2", status, nil))
}

// pciUevent builds a sysfs PCI device uevent body.
func pciUevent(driver, pciID, subsys string) *fstest.MapFile {
	return &fstest.MapFile{Data: []byte("DRIVER=" + driver + "\nPCI_CLASS=28000\nPCI_ID=" + pciID +
		"\nPCI_SUBSYS_ID=" + subsys + "\nPCI_SLOT_NAME=0001:01:00.0\nMODALIAS=pci:v000014C3\n")}
}

func TestResolveWirelessRadioHardwareName_sysfsFallback(t *testing.T) {
	t.Parallel()

	status := map[string]*WirelessRadioStatus{
		"radio0": {Interfaces: []WirelessRadioInterface{{Ifname: "phy0-mesh0"}}},
	}

	cases := []struct {
		name  string
		hw    string // iwinfo hardware name; "" means iwinfo did not name it
		phy   string // iwinfo phy; "" forces the netdev path
		sysfs fstest.MapFS
		want  string
	}{
		{
			name:  "iwinfo names MT7916, sysfs not consulted",
			hw:    "MediaTek MT7916AN",
			phy:   "phy0",
			sysfs: fstest.MapFS{},
			want:  "MediaTek MT7916AN",
		},
		{
			name: "generic iwinfo name, MT7916 PCI ID with vendor subsystem",
			hw:   "Generic MAC80211",
			phy:  "phy0",
			sysfs: fstest.MapFS{
				"class/ieee80211/phy0/device/uevent": pciUevent("mt7915e", "14C3:7906", "1A3B:5458"),
			},
			want: "MediaTek MT7916",
		},
		{
			name: "no iwinfo name, MT7915 PCI ID",
			phy:  "phy0",
			sysfs: fstest.MapFS{
				"class/ieee80211/phy0/device/uevent": pciUevent("mt7915e", "14c3:7915", "14C3:7915"),
			},
			want: "MediaTek MT7915",
		},
		{
			name: "unknown PCI ID but mt7915e driver",
			hw:   "Generic MAC80211",
			phy:  "phy0",
			sysfs: fstest.MapFS{
				"class/ieee80211/phy0/device/uevent": pciUevent("mt7915e", "14C3:0000", "0000:0000"),
			},
			want: "MediaTek MT7915/MT7916",
		},
		{
			name: "no phy reported, netdev path used",
			hw:   "Generic MAC80211",
			sysfs: fstest.MapFS{
				"class/net/phy0-mesh0/device/uevent": pciUevent("mt7915e", "14C3:7906", "1A3B:5458"),
			},
			want: "MediaTek MT7916",
		},
		{
			name: "other driver keeps iwinfo name",
			hw:   "Generic MAC80211",
			phy:  "phy0",
			sysfs: fstest.MapFS{
				"class/ieee80211/phy0/device/uevent": pciUevent("mt7921e", "14C3:7961", "14C3:7961"),
			},
			want: "Generic MAC80211",
		},
		{
			name:  "sysfs missing keeps iwinfo name",
			hw:    "Generic MAC80211",
			phy:   "phy0",
			sysfs: fstest.MapFS{},
			want:  "Generic MAC80211",
		},
		{
			name: "malformed uevent ignored",
			hw:   "Generic MAC80211",
			phy:  "phy0",
			sysfs: fstest.MapFS{
				"class/ieee80211/phy0/device/uevent": &fstest.MapFile{Data: []byte("garbage\n\nDRIVER\n")},
			},
			want: "Generic MAC80211",
		},
		{
			name: "path traversal in phy rejected",
			hw:   "Generic MAC80211",
			phy:  "../phy0",
			sysfs: fstest.MapFS{
				"class/phy0/device/uevent": pciUevent("mt7915e", "14C3:7906", "1A3B:5458"),
			},
			want: "Generic MAC80211",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			info := map[string]*iwinfo.InterfaceInfo{
				"phy0-mesh0": {PHY: tc.phy, Hardware: iwinfo.HardwareInfo{Name: tc.hw}},
			}

			got := resolveWirelessRadioHardwareName(tc.sysfs, "radio0", status, info)
			assert.Equal(t, tc.want, got)
			assert.Equal(t, SupportsSecondaryMeshLink(tc.want), SupportsSecondaryMeshLink(got))
		})
	}
}

func TestResolveWirelessRadioHardwareName_noIwinfoEntry(t *testing.T) {
	t.Parallel()

	status := map[string]*WirelessRadioStatus{
		"radio0": {Interfaces: []WirelessRadioInterface{{Ifname: "phy0-ap0"}}},
	}
	sysfs := fstest.MapFS{
		"class/net/phy0-ap0/device/uevent": pciUevent("mt7915e", "14C3:7906", "1A3B:5458"),
	}

	assert.Equal(t, "MediaTek MT7916", resolveWirelessRadioHardwareName(sysfs, "radio0", status, nil))
}

func TestGetWirelessStatus_DisabledRadio(t *testing.T) {
	fixture := `{
  "radio1": {
    "up": false,
    "disabled": true,
    "interfaces": []
  }
}`
	provider := NewUbusWirelessStatusProvider(&wsUbusExecutor{output: []byte(fixture)})

	result, err := provider.GetWirelessStatus(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	r1 := result["radio1"]
	if r1 == nil {
		t.Fatal("expected radio1 in result")
	}

	if r1.Up {
		t.Error("expected radio1 to be down")
	}

	if !r1.Disabled {
		t.Error("expected radio1 to be disabled")
	}

	if len(r1.Interfaces) != 0 {
		t.Errorf("expected 0 interfaces, got %d", len(r1.Interfaces))
	}
}

func TestGetWirelessStatus_EmptyResponse(t *testing.T) {
	provider := NewUbusWirelessStatusProvider(&wsUbusExecutor{output: []byte(`{}`)})

	result, err := provider.GetWirelessStatus(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(result) != 0 {
		t.Errorf("expected empty result, got %d entries", len(result))
	}
}

func TestGetWirelessStatus_UbusError(t *testing.T) {
	provider := NewUbusWirelessStatusProvider(&wsUbusExecutor{err: errors.New("ubus timeout")})

	_, err := provider.GetWirelessStatus(context.Background())
	if err == nil {
		t.Fatal("expected error when ubus fails")
	}
}

func TestGetWirelessStatus_MalformedJSON(t *testing.T) {
	provider := NewUbusWirelessStatusProvider(&wsUbusExecutor{output: []byte(`{invalid}`)})

	_, err := provider.GetWirelessStatus(context.Background())
	if err == nil {
		t.Fatal("expected error for malformed JSON")
	}
}

func TestGetWirelessStatus_MultipleInterfacesPerRadio(t *testing.T) {
	fixture := `{
  "radio0": {
    "up": true,
    "disabled": false,
    "interfaces": [
      {
        "section": "default_radio0",
        "ifname": "phy0-ap0",
        "config": {
          "mode": "ap"
        }
      },
      {
        "section": "mesh_radio0",
        "ifname": "phy0-mesh0",
        "config": {
          "mode": "mesh"
        }
      }
    ]
  }
}`
	provider := NewUbusWirelessStatusProvider(&wsUbusExecutor{output: []byte(fixture)})

	result, err := provider.GetWirelessStatus(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	r0 := result["radio0"]
	if r0 == nil {
		t.Fatal("expected radio0 in result")
	}

	if len(r0.Interfaces) != 2 {
		t.Fatalf("expected 2 interfaces for radio0, got %d", len(r0.Interfaces))
	}

	if r0.Interfaces[0].Section != "default_radio0" {
		t.Errorf("first interface section: got %s, want default_radio0", r0.Interfaces[0].Section)
	}

	if r0.Interfaces[1].Ifname != "phy0-mesh0" {
		t.Errorf("second interface ifname: got %s, want phy0-mesh0", r0.Interfaces[1].Ifname)
	}
}

func TestSupportsSecondaryMeshLink(t *testing.T) {
	cases := []struct {
		name string
		want bool
	}{
		{"MediaTek MT7915AN", true},
		{"MediaTek MT7916AN", true},
		{"MT7916", true},
		{"MediaTek MT7921", false},
		{"Broadcom BCM43430", false},
		{"Morse Micro MM6108", false},
		{"", false},
	}

	for _, tc := range cases {
		assert.Equal(t, tc.want, SupportsSecondaryMeshLink(tc.name), "hardware %q", tc.name)
	}
}
