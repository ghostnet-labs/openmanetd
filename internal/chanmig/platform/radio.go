package platform

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/rs/zerolog"

	"github.com/openmanet/openmanetd/internal/chanmig"
	"github.com/openmanet/openmanetd/internal/network"
)

const (
	wirelessConfig     = "wireless"
	wifiDeviceSection  = "wifi-device"
	uciBandOption      = "band"
	reloadRadioTimeout = 30 * time.Second
)

// UCIBand returns the UCI wifi-device "band" value for b.
func UCIBand(b chanmig.Band) string {
	switch b {
	case chanmig.BandHaLow:
		return "s1g"
	case chanmig.Band2G4:
		return "2g"
	case chanmig.Band5G:
		return "5g"
	default:
		return ""
	}
}

// HTMode returns the UCI htmode for an assignment. S1G widths use LuCI's
// "N MHz" literals. Wi-Fi keeps the PHY family of current (HE, VHT, EHT
// or HT) and only changes the width; an HT40+/HT40- current value is kept
// for a 40 MHz assignment so the secondary-channel choice survives, and
// HT is promoted to VHT for widths HT cannot express.
func HTMode(a chanmig.Assignment, current string) string {
	w := strconv.Itoa(int(a.WidthMHz))

	if a.Band == chanmig.BandHaLow {
		return w + " MHz"
	}

	cur := strings.ToUpper(strings.TrimSpace(current))

	switch {
	case strings.HasPrefix(cur, "EHT"):
		return "EHT" + w
	case strings.HasPrefix(cur, "HE"):
		return "HE" + w
	case strings.HasPrefix(cur, "VHT"):
		return "VHT" + w
	case a.WidthMHz > 40:
		return "VHT" + w
	case a.WidthMHz == 40 && (cur == "HT40+" || cur == "HT40-"):
		return cur
	default:
		return "HT" + w
	}
}

// UCIRadio implements chanmig.RadioSetter by rewriting the channel and
// htmode options of the band's wifi-device section and reloading the
// wireless stack. A tune that matches the committed UCI is a no-op, so the
// Node's boot-time tune does not bounce a radio that is already right.
type UCIRadio struct { //nolint:govet // fieldalignment: mu stays above the fields it guards per .claude/rules/concurrency.md
	// Reader is the UCI tree. It is re-read before every tune so edits
	// made through other readers (Wi-Fi settings RPC, setup) are seen.
	Reader network.ConfigReader
	// Reload makes committed UCI take effect. nil runs reload_config.
	Reload func(ctx context.Context) error
	// Radios optionally pins a band to a wifi-device section name.
	// Bands without an entry use the first section whose band option
	// matches UCIBand.
	Radios map[chanmig.Band]string
	Log    zerolog.Logger

	// mu serializes tunes, including the reload, so two bands never
	// interleave their UCI writes. Nothing else takes it.
	mu sync.Mutex
}

// SetChannel implements chanmig.RadioSetter.
func (r *UCIRadio) SetChannel(ctx context.Context, a chanmig.Assignment) error {
	if err := a.Validate(); err != nil {
		return fmt.Errorf("tune radio: %w", err)
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	if err := r.Reader.ReloadConfig(); err != nil {
		return fmt.Errorf("re-read uci: %w", err)
	}

	section, err := r.section(a.Band)
	if err != nil {
		return err
	}

	dev, err := network.GetWirelessDeviceByNameWithReader(section, r.Reader)
	if err != nil {
		return fmt.Errorf("read %s: %w", section, err)
	}

	channel := strconv.Itoa(int(a.Channel))
	htmode := HTMode(a, dev.HTMode)

	if dev.Channel == channel && dev.HTMode == htmode {
		return nil
	}

	update := &network.UCIWirelessDevice{Channel: channel, HTMode: htmode}
	if err := network.SetWirelessDeviceConfigWithReader(section, update, r.Reader); err != nil {
		return fmt.Errorf("write %s: %w", section, err)
	}

	r.Log.Info().Str("radio", section).Str("channel", channel).Str("htmode", htmode).
		Msg("chanmig: retuning radio")

	return r.reload(ctx)
}

// section resolves the wifi-device section for band b.
func (r *UCIRadio) section(b chanmig.Band) (string, error) {
	if name, ok := r.Radios[b]; ok && name != "" {
		return name, nil
	}

	want := UCIBand(b)

	sections, err := r.Reader.GetSections(wirelessConfig, wifiDeviceSection)
	if err != nil {
		return "", fmt.Errorf("list wifi-device sections: %w", err)
	}

	for _, s := range sections {
		if v, ok := r.Reader.Get(wirelessConfig, s, uciBandOption); ok && len(v) > 0 && strings.EqualFold(v[0], want) {
			return s, nil
		}
	}

	return "", fmt.Errorf("chanmig/platform: no wifi-device with band %q for %s", want, b)
}

// reload runs on a context detached from ctx so daemon shutdown cannot
// kill reload_config halfway through a radio restart; the timeout bounds
// it instead.
func (r *UCIRadio) reload(ctx context.Context) error {
	rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), reloadRadioTimeout)
	defer cancel()

	reload := r.Reload
	if reload == nil {
		reload = network.ForceReloadConfig
	}

	if err := reload(rctx); err != nil {
		return fmt.Errorf("reload wireless: %w", err)
	}

	return nil
}
