package radiogate

import (
	"context"
	"fmt"
	"slices"

	"github.com/openmanet/openmanetd/internal/iwinfo"
	"github.com/openmanet/openmanetd/internal/network"
)

// HaLowResolver identifies the wiphys driven by the Morse Micro HaLow
// driver the same way the rest of the daemon recognizes HaLow: a UCI
// wifi-device with `option type 'morse'` (network.IsMorseDevice). Its
// runtime interfaces come from `ubus call network.wireless status` and
// each interface's wiphy from iwinfo. Netdevs adds interface names known
// to be HaLow from other configuration (hardware.halow.netdev).
//
// A morse device whose interfaces are down cannot be resolved; it is
// skipped rather than reported as an error, and the Gate then declines
// to evaluate HaLow demands (ErrHaLowUnidentified) instead of rejecting
// them.
type HaLowResolver struct {
	Config network.ConfigReader
	Status network.WirelessStatusProvider
	Iwinfo iwinfo.IwinfoProvider
	// Netdevs returns extra HaLow interface names. nil means none.
	Netdevs func() []string
}

// PHYs returns the sorted, de-duplicated HaLow wiphy names.
func (r *HaLowResolver) PHYs(ctx context.Context) ([]string, error) {
	ifnames, err := r.ifnames(ctx)
	if err != nil {
		return nil, err
	}

	if len(ifnames) == 0 || r.Iwinfo == nil {
		return nil, nil
	}

	info, err := r.Iwinfo.GetInfoForAll(ctx)
	if err != nil {
		return nil, fmt.Errorf("iwinfo: %w", err)
	}

	phys := make([]string, 0, 1)

	for _, ifname := range ifnames {
		if i := info[ifname]; i != nil && i.GetPHY() != "" && !slices.Contains(phys, i.GetPHY()) {
			phys = append(phys, i.GetPHY())
		}
	}

	slices.Sort(phys)

	return phys, nil
}

// ifnames collects the runtime interface names of every morse device plus
// the configured extras.
func (r *HaLowResolver) ifnames(ctx context.Context) ([]string, error) {
	var out []string

	if r.Netdevs != nil {
		for _, n := range r.Netdevs() {
			if n != "" {
				out = append(out, n)
			}
		}
	}

	if r.Config == nil || r.Status == nil {
		return out, nil
	}

	devices, err := r.Config.GetSections("wireless", "wifi-device")
	if err != nil {
		return nil, fmt.Errorf("list wifi-device sections: %w", err)
	}

	morse := make([]string, 0, 1)

	for _, dev := range devices {
		if network.IsMorseDevice(r.Config, dev) {
			morse = append(morse, dev)
		}
	}

	if len(morse) == 0 {
		return out, nil
	}

	status, err := r.Status.GetWirelessStatus(ctx)
	if err != nil {
		return nil, fmt.Errorf("wireless status: %w", err)
	}

	for _, dev := range morse {
		radio := status[dev]
		if radio == nil {
			continue
		}

		for _, iface := range radio.Interfaces {
			if iface.Ifname != "" {
				out = append(out, iface.Ifname)
			}
		}
	}

	return out, nil
}
