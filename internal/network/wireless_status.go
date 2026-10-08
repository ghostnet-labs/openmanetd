package network

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"strings"

	"github.com/openmanet/openmanetd/internal/iwinfo"
)

// WirelessRadioStatus holds the runtime state for a single UCI radio device.
type WirelessRadioStatus struct {
	Interfaces []WirelessRadioInterface `json:"interfaces"`
	Up         bool                     `json:"up"`
	Disabled   bool                     `json:"disabled"`
}

// WirelessRadioInterface represents one interface running on a radio.
type WirelessRadioInterface struct {
	Section string                    `json:"section"`
	Ifname  string                    `json:"ifname"`
	Config  WirelessIfaceStatusConfig `json:"config"`
}

// ResolveWirelessRadioHardwareName correlates a UCI radio section with its
// runtime interfaces and returns the hardware name reported by iwinfo.
//
// iwinfo only names a chip when its PCI subsystem ID is in iwinfo's
// devices.txt, so a board that ships an MT7915/MT7916 under a vendor
// subsystem ID shows up as a generic radio. When iwinfo's name does not
// satisfy SupportsSecondaryMeshLink, the radio's kernel driver and PCI ID
// are read from /sys as a fallback; see detectSecondaryMeshChip.
func ResolveWirelessRadioHardwareName(
	radioName string,
	status map[string]*WirelessRadioStatus,
	iwinfoData map[string]*iwinfo.InterfaceInfo,
) string {
	return resolveWirelessRadioHardwareName(os.DirFS("/sys"), radioName, status, iwinfoData)
}

// resolveWirelessRadioHardwareName is ResolveWirelessRadioHardwareName
// with the sysfs root injected so tests can supply an fstest.MapFS.
func resolveWirelessRadioHardwareName(
	sysfs fs.FS,
	radioName string,
	status map[string]*WirelessRadioStatus,
	iwinfoData map[string]*iwinfo.InterfaceInfo,
) string {
	radio, ok := status[radioName]
	if !ok || radio == nil {
		return ""
	}

	var reported string

	for _, iface := range radio.Interfaces {
		info := iwinfoData[iface.Ifname]
		if info == nil {
			continue
		}

		if hardwareName := info.GetHardwareName(); hardwareName != "" {
			reported = hardwareName

			break
		}
	}

	if SupportsSecondaryMeshLink(reported) {
		return reported
	}

	for _, iface := range radio.Interfaces {
		var phy string
		if info := iwinfoData[iface.Ifname]; info != nil {
			phy = info.GetPHY()
		}

		if name := detectSecondaryMeshChip(sysfs, phy, iface.Ifname); name != "" {
			return name
		}
	}

	return reported
}

// mt7915DriverName is the PCI driver name the mt76 mt7915e module
// registers for both MT7915 and MT7916 parts.
const mt7915DriverName = "mt7915e"

// Hardware names reported when sysfs, not iwinfo, identifies the chip.
// Each must satisfy SupportsSecondaryMeshLink.
const (
	hwNameMT7915       = "MediaTek MT7915"
	hwNameMT7916       = "MediaTek MT7916"
	hwNameMT7915Family = "MediaTek MT7915/MT7916"
)

// detectSecondaryMeshChip identifies an MT7915/MT7916 radio from its sysfs
// device uevent, which carries the bound driver (DRIVER=) and the PCI
// vendor:device pair (PCI_ID=). The phy path is tried first, then the
// netdev path. It returns a hardware name SupportsSecondaryMeshLink
// accepts, or "" when the radio is something else or sysfs is unreadable.
//
// Only the primary PCIe functions are matched: the mt7915e PCI table lists
// 14c3:7915 (MT7915) and 14c3:7906 (MT7916). The second-HIF functions
// 14c3:7916 and 14c3:790a bind to a separate HIF driver and never own a
// wiphy, so they cannot appear here.
func detectSecondaryMeshChip(sysfs fs.FS, phy, ifname string) string {
	candidates := [...]string{
		sysfsDeviceUevent("class/ieee80211", phy),
		sysfsDeviceUevent("class/net", ifname),
	}

	for _, p := range candidates {
		if p == "" {
			continue
		}

		data, err := fs.ReadFile(sysfs, p)
		if err != nil {
			continue
		}

		if name := secondaryMeshChipFromUevent(string(data)); name != "" {
			return name
		}
	}

	return ""
}

// sysfsDeviceUevent returns "<class>/<name>/device/uevent", or "" when
// name is empty or is not a single path element.
func sysfsDeviceUevent(class, name string) string {
	if name == "" || name == "." || name == ".." || strings.ContainsRune(name, '/') {
		return ""
	}

	return class + "/" + name + "/device/uevent"
}

// secondaryMeshChipFromUevent maps a PCI device uevent body to an
// MT7915/MT7916 hardware name, or "" when it describes another device.
func secondaryMeshChipFromUevent(uevent string) string {
	var driver, pciID string

	for line := range strings.SplitSeq(uevent, "\n") {
		key, value, ok := strings.Cut(strings.TrimSpace(line), "=")
		if !ok {
			continue
		}

		switch key {
		case "DRIVER":
			driver = value
		case "PCI_ID":
			pciID = strings.ToLower(value)
		}
	}

	switch pciID {
	case "14c3:7915":
		return hwNameMT7915
	case "14c3:7906":
		return hwNameMT7916
	}

	if driver == mt7915DriverName {
		return hwNameMT7915Family
	}

	return ""
}

// WirelessIfaceStatusConfig is the nested config block inside the ubus response.
type WirelessIfaceStatusConfig struct {
	Mode string `json:"mode"`
}

// WirelessStatusProvider retrieves the runtime mapping from UCI radio names
// to Linux interface names via ubus.
type WirelessStatusProvider interface {
	GetWirelessStatus(ctx context.Context) (map[string]*WirelessRadioStatus, error)
}

// UbusWirelessStatusProvider implements WirelessStatusProvider by calling
// `ubus call network.wireless status`.
type UbusWirelessStatusProvider struct {
	exec iwinfo.UbusExecutor
}

// NewUbusWirelessStatusProvider returns a provider backed by the given executor.
func NewUbusWirelessStatusProvider(exec iwinfo.UbusExecutor) *UbusWirelessStatusProvider {
	return &UbusWirelessStatusProvider{exec: exec}
}

// NewDefaultWirelessStatusProvider returns a provider using the real ubus binary.
func NewDefaultWirelessStatusProvider() *UbusWirelessStatusProvider {
	return &UbusWirelessStatusProvider{exec: &iwinfo.DefaultUbusExecutor{}}
}

// GetWirelessStatus calls `ubus call network.wireless status` and returns the
// result keyed by UCI radio device name (e.g. "radio0", "radio2").
func (p *UbusWirelessStatusProvider) GetWirelessStatus(ctx context.Context) (map[string]*WirelessRadioStatus, error) {
	out, err := p.exec.Execute(ctx, "call", "network.wireless", "status")
	if err != nil {
		return nil, fmt.Errorf("network.wireless status: %w", err)
	}

	var result map[string]*WirelessRadioStatus
	if err := json.Unmarshal(out, &result); err != nil {
		return nil, fmt.Errorf("parse network.wireless status: %w", err)
	}

	return result, nil
}

// SupportsSecondaryMeshLink reports whether an iwinfo hardware name
// names a 2.4 GHz chipset the daemon runs a secondary batman-adv mesh
// link on. The daemon's boot-time fallback and the wizard's capability
// flag must agree, so both call this.
func SupportsSecondaryMeshLink(hardwareName string) bool {
	return strings.Contains(hardwareName, "MT7915") || strings.Contains(hardwareName, "MT7916")
}
