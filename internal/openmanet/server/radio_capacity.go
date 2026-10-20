package server

import (
	"github.com/openmanet/openmanetd/internal/config"
	"github.com/openmanet/openmanetd/internal/iwinfo"
	"github.com/openmanet/openmanetd/internal/network"
	"github.com/openmanet/openmanetd/internal/radiocap"
	"github.com/openmanet/openmanetd/internal/radiogate"
	"github.com/rs/zerolog"
)

// newRadioCapacityGate builds the radio capacity gate (GHO-59) from
// config, and returns the Provider it reads so RadioService can expose the
// same cached PHY inventory. Mode, qualification evidence and policy are read from cfg on
// every evaluation, so a config file edit applies without a restart. HaLow
// PHYs are identified from the same UCI tree, ubus status and iwinfo the
// wifi config service uses, plus hardware.halow.netdev.
func newRadioCapacityGate(
	cfg *config.Config,
	log zerolog.Logger,
	reader network.ConfigReader,
	status network.WirelessStatusProvider,
	iw iwinfo.IwinfoProvider,
) (*radiogate.Gate, *radiogate.Provider) {
	if rejected := cfg.GetRadio().CapacityGateRejected; rejected != "" {
		log.Warn().Str("configured", rejected).Str("fallback", string(radiogate.DefaultMode)).
			Msg("radio.capacityGate is not off, advisory or enforce; using default")
	}

	halow := &radiogate.HaLowResolver{
		Config: reader,
		Status: status,
		Iwinfo: iw,
		Netdevs: func() []string {
			return []string{cfg.GetHardware().HaLowNetdev}
		},
	}

	provider := &radiogate.Provider{
		HaLowPHYs: halow.PHYs,
		Settings: func() radiogate.Settings {
			r := cfg.GetRadio()

			return radiogate.Settings{
				QualifiedPHYs:        r.QualifiedPHYs,
				SharingQualifiedPHYs: r.SharingQualifiedPHYs,
				UnavailablePHYs:      r.UnavailablePHYs,
				AllowUnqualified:     r.AllowUnqualified,
			}
		},
	}

	gate := &radiogate.Gate{
		Log:    log,
		Source: provider,
		Mode: func() radiogate.Mode {
			m, _ := radiogate.ParseMode(cfg.GetRadio().CapacityGate)

			return m
		},
		Policy: func() radiocap.Policy {
			p := radiocap.DefaultPolicy()
			p.AllowUnqualified = cfg.GetRadio().AllowUnqualified

			return p
		},
	}

	return gate, provider
}
