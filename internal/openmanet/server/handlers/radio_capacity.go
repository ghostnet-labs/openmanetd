package handlers

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"connectrpc.com/connect"
	wificonfigv1 "github.com/openmanet/openmanetd/internal/api/openmanet/wifi_config/v1"
	"github.com/openmanet/openmanetd/internal/network"
	"github.com/openmanet/openmanetd/internal/radiocap"
	"github.com/openmanet/openmanetd/internal/radiogate"
)

// RadioCapacityGate is the radio capacity admission check (GHO-59) radio
// writes consult before staging. Evaluate returns nil to proceed or a
// *radiogate.DeniedError to refuse; *radiogate.Gate implements it.
type RadioCapacityGate interface {
	Evaluate(ctx context.Context, req radiogate.Request) error
}

// Capacity check origins recorded in the gate's decision history.
const (
	capacityOriginUpdate = "wifi_config.update_radio_settings"
	capacityOriginBatch  = "wifi_config.apply_radio_settings_batch"
)

// Metadata keys carrying the machine-readable denial on the connect error.
const (
	capacityOutcomeHeader = "Radio-Capacity-Outcome"
	capacityReasonHeader  = "Radio-Capacity-Reason"
)

// capacityDeniedError maps a gate denial to CodeFailedPrecondition, with
// the outcome and reason both in the message and as error metadata.
func capacityDeniedError(d *radiogate.DeniedError) *connect.Error {
	cerr := connect.NewError(connect.CodeFailedPrecondition, d)
	cerr.Meta().Set(capacityOutcomeHeader, string(d.Decision.Outcome))
	cerr.Meta().Set(capacityReasonHeader, string(d.Decision.Reason))

	return cerr
}

// admitRadioCapacity checks updates against the node's radio capacity
// before any of them is staged. It returns a connect error only when the
// gate refuses the change; a nil gate, a UCI read failure while building
// the demands, or any non-denial gate error lets the change proceed (the
// gate itself logs and records). The caller holds s.mu so the demands are
// read from the same UCI state the staging will write over and two
// concurrent changes cannot each be admitted against the pre-change state.
// s.mu already spans the reload_config run; the gate's `iw list` probe is
// bounded by its own timeout and cached for a minute, so it adds at most
// one short fork per TTL to that critical section.
func (s *WifiConfigService) admitRadioCapacity(ctx context.Context, origin string, updates []RadioSettingsUpdate) error {
	if s.Capacity == nil {
		return nil
	}

	current, requested, err := radioDemands(s.ConfigReader, updates)
	if err != nil {
		s.Log.Warn().Err(err).Msg("Cannot build radio capacity demands; skipping capacity check")

		return nil
	}

	radios := make([]string, 0, len(updates))
	for _, u := range updates {
		radios = append(radios, u.RadioName)
	}

	err = s.Capacity.Evaluate(ctx, radiogate.Request{
		Origin:    origin,
		Radios:    radios,
		Current:   current,
		Requested: requested,
	})
	if err == nil {
		return nil
	}

	var denied *radiogate.DeniedError
	if errors.As(err, &denied) {
		return capacityDeniedError(denied)
	}

	s.Log.Warn().Err(err).Msg("Radio capacity check failed; proceeding")

	return nil
}

// radioDemands translates the enabled wifi-ifaces into radiocap demands
// in their post-update state. Interfaces on an updated wifi-device are
// requested (the update retunes the whole radio); the rest are current.
// It mirrors stageRadioSettings: the device takes the new channel and
// htmode, and only the radio's linked (first) iface takes the new mode
// and disabled flag. Interfaces whose mode has no capacity role (adhoc,
// monitor) or whose band or channel cannot be determined are skipped.
func radioDemands(reader network.ConfigReader, updates []RadioSettingsUpdate) (current, requested []radiocap.Demand, err error) {
	devices, err := reader.GetSections(wirelessConfig, "wifi-device")
	if err != nil {
		return nil, nil, fmt.Errorf("list wifi-device sections: %w", err)
	}

	ifaces, err := reader.GetSections(wirelessConfig, "wifi-iface")
	if err != nil {
		return nil, nil, fmt.Errorf("list wifi-iface sections: %w", err)
	}

	// Sorted copies give auto channels a stable slot per device and the
	// demands a stable order; findLinkedIface keeps the reader's order,
	// exactly as stageRadioSettings sees it.
	sortedDevices := slices.Sorted(slices.Values(devices))

	byRadio := make(map[string]*wificonfigv1.RadioSettings, len(updates))
	linked := make(map[string]string, len(updates))

	for _, u := range updates {
		byRadio[u.RadioName] = u.Settings
		linked[u.RadioName] = findLinkedIface(u.RadioName, ifaces, reader)
	}

	current = make([]radiocap.Demand, 0, len(ifaces))
	requested = make([]radiocap.Demand, 0, len(updates))

	for _, sec := range slices.Sorted(slices.Values(ifaces)) {
		iface, err := network.GetWirelessIfaceByNameWithReader(sec, reader)
		if err != nil {
			return nil, nil, fmt.Errorf("read iface %s: %w", sec, err)
		}

		if iface.Device == "" {
			continue
		}

		dev, err := network.GetWirelessDeviceByNameWithReader(iface.Device, reader)
		if err != nil {
			return nil, nil, fmt.Errorf("read device %s: %w", iface.Device, err)
		}

		settings, updated := byRadio[iface.Device]
		st := ifaceState{
			mode:     iface.Mode,
			channel:  dev.Channel,
			htmode:   dev.HTMode,
			disabled: iface.Disabled == "1" || dev.Disabled == "1",
		}

		if updated {
			st.apply(settings, linked[iface.Device] == sec, dev.Disabled == "1")
		}

		if st.disabled {
			continue
		}

		d, ok := st.demand(sec, dev, network.IsMorseDevice(reader, iface.Device), slices.Index(sortedDevices, iface.Device))
		if !ok {
			continue
		}

		if updated {
			requested = append(requested, d)
		} else {
			current = append(current, d)
		}
	}

	return current, requested, nil
}

// ifaceState is one wifi-iface's capacity-relevant options.
type ifaceState struct {
	mode     string
	channel  string
	htmode   string
	disabled bool
}

// apply overlays settings the way stageRadioSettings writes them. Empty
// fields are left alone, as the UCI setters skip them.
func (st *ifaceState) apply(settings *wificonfigv1.RadioSettings, isLinked, devDisabled bool) {
	if settings == nil {
		return
	}

	if settings.GetChannel() != "" {
		st.channel = settings.GetChannel()
	}

	if h := ProtoToWifiHTMode(settings.GetBandwidth()); h != "" {
		st.htmode = h
	}

	if !isLinked {
		return
	}

	if m := ProtoToWifiMode(settings.GetMode()); m != "" {
		st.mode = m
	}

	if settings.Disabled != nil {
		st.disabled = settings.GetDisabled() || devDisabled
	}
}

// demand builds the radiocap demand for the iface section sec.
func (st *ifaceState) demand(sec string, dev *network.UCIWirelessDevice, isMorse bool, autoSlot int) (radiocap.Demand, bool) {
	role, purpose, ok := capacityRole(st.mode)
	if !ok {
		return radiocap.Demand{}, false
	}

	band := radiogate.UCIBand(dev.Band, isMorse, st.channel)

	width, _ := network.HTModeBandwidthMHz(st.htmode)

	ch, ok := radiogate.UCIChannel(band, st.channel, width, autoSlot)
	if !ok {
		return radiocap.Demand{}, false
	}

	return radiocap.Demand{ID: sec, Purpose: purpose, Role: role, Channel: ch}, true
}

// capacityRole maps a UCI wifi-iface mode to a capacity role. A missing
// mode is an AP, as netifd defaults it.
func capacityRole(mode string) (radiocap.Role, radiocap.Purpose, bool) {
	switch mode {
	case uciModeMesh:
		return radiocap.RoleMesh, radiocap.PurposeMesh, true
	case uciModeAP, "":
		return radiocap.RoleAP, radiocap.PurposeEUD, true
	case uciModeSTA:
		return radiocap.RoleStation, radiocap.PurposeOther, true
	default:
		return "", "", false
	}
}
