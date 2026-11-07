package openmanet

import (
	"context"
	"fmt"
	"net"
	"os"
	"strings"

	"github.com/openmanet/go-alfred"
	netv1 "github.com/openmanet/openmanetd/internal/api/openmanet/network/v1"
	"github.com/openmanet/openmanetd/internal/config"
	"github.com/openmanet/openmanetd/internal/gpsd"
	"github.com/openmanet/openmanetd/internal/jamdetect"
	"github.com/openmanet/openmanetd/internal/mgmt"
	"github.com/openmanet/openmanetd/internal/openmanet/server/handlers"
	"github.com/openmanet/openmanetd/internal/util/logger"
	"github.com/rs/zerolog"
)

// alfred record identity for jam reports. The data type comes from
// openmanet.network.v1.DataType (DATA_TYPE_JAM_REPORT = 105).
const (
	jamAlfredDataType    = uint8(netv1.DataType_DATA_TYPE_JAM_REPORT)
	jamAlfredDataVersion = uint8(1)
)

// jamGossip binds an alfred client to the jam report data type.
type jamGossip struct {
	client alfred.ReadWriteClient
}

func (g jamGossip) Publish(payload []byte) error {
	if err := g.client.Set(jamAlfredDataType, jamAlfredDataVersion, payload); err != nil {
		return fmt.Errorf("alfred set: %w", err)
	}

	return nil
}

// Fetch returns every jam report alfred holds. Records of another version
// are skipped so a mixed-firmware mesh degrades to "node not heard".
func (g jamGossip) Fetch() ([]jamdetect.GossipRecord, error) {
	recs, err := g.client.Request(jamAlfredDataType)
	if err != nil {
		return nil, fmt.Errorf("alfred request: %w", err)
	}

	out := make([]jamdetect.GossipRecord, 0, len(recs))

	for _, r := range recs {
		if r.Version != jamAlfredDataVersion {
			continue
		}

		out = append(out, jamdetect.GossipRecord{Source: r.Source, Data: r.Data})
	}

	return out, nil
}

// startJamdetect builds the jamming detector and runs it until ctx is
// canceled. It returns nil, starting nothing, when jamdetect.enable is
// false. Without alfred the detector still runs and alerts locally; it
// just does not share reports. gps may be nil (GNSS disabled).
func startJamdetect(ctx context.Context, cfg *config.Config, manager *mgmt.ManagementConfig, gps *gpsd.GPSService, log zerolog.Logger) *jamdetect.Detector {
	jc := cfg.GetJamdetect()
	if !jc.Enable {
		log.Info().Msg("jamdetect: disabled")

		return nil
	}

	batIface := cfg.GetAlfredBatInterface()

	opts := jamdetectOptions(jc)
	opts.Log = logger.GetLogger("jamdetect")
	opts.Source = &jamdetect.IWSource{}
	opts.Identity = func() (string, string) { return nodeIdentity(batIface) }

	if gps != nil {
		opts.Position = func() (jamdetect.Position, bool) {
			p := gps.GetPosition()
			if !p.Valid {
				return jamdetect.Position{}, false
			}

			return jamdetect.Position{Latitude: p.Latitude, Longitude: p.Longitude, AltitudeM: p.Altitude}, true
		}
	}

	switch {
	case !jc.Gossip:
	case manager == nil || manager.Client() == nil:
		log.Info().Msg("jamdetect: alfred is disabled; detections stay on this node")
	default:
		opts.Gossip = jamGossip{client: manager.Client()}
	}

	det := jamdetect.New(opts)

	go det.Run(ctx)

	return det
}

// jamDetectorAPI converts the detector for the API server, keeping a
// disabled detector a nil interface rather than a typed nil pointer.
func jamDetectorAPI(det *jamdetect.Detector) handlers.JamDetector {
	if det == nil {
		return nil
	}

	return det
}

// jamdetectOptions maps the config section onto detector options.
func jamdetectOptions(jc config.JamdetectConfig) jamdetect.Options {
	return jamdetect.Options{
		Interval:       jc.Interval,
		GossipInterval: jc.GossipInterval,
		GossipExpiry:   jc.GossipExpiry,
		Thresholds: jamdetect.Thresholds{
			NoiseRiseDB:     jc.NoiseRiseDB,
			NoiseCeilingDBm: jc.NoiseCeilingDBm,
			BusyRisePct:     jc.BusyRisePct,
			BusyFloorPct:    jc.BusyFloorPct,
			BusyCeilingPct:  jc.BusyCeilingPct,
			WarmupSamples:   jc.WarmupSamples,
			SuspectSamples:  jc.SuspectSamples,
			JamSamples:      jc.JamSamples,
			ClearSamples:    jc.ClearSamples,
		},
	}
}

// nodeIdentity returns the batman interface MAC and the hostname that
// identify this node in published reports. Either may be empty when
// unavailable; the detector then falls back to the alfred source MAC on
// receivers.
func nodeIdentity(batIface string) (primaryMAC, hostname string) {
	if ifc, err := net.InterfaceByName(batIface); err == nil {
		primaryMAC = strings.ToLower(ifc.HardwareAddr.String())
	}

	hostname, _ = os.Hostname() //nolint:errcheck // an unknown hostname publishes empty

	return primaryMAC, hostname
}
