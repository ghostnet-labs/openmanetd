package openmanet

import (
	"context"
	"os"

	"github.com/rs/zerolog"

	"github.com/openmanet/openmanetd/internal/config"
	"github.com/openmanet/openmanetd/internal/cotmcast"
	"github.com/openmanet/openmanetd/internal/database/models"
	"github.com/openmanet/openmanetd/internal/gpsd"
	"github.com/openmanet/openmanetd/internal/openmanet/server"
	"github.com/openmanet/openmanetd/internal/scanner"
	"github.com/openmanet/openmanetd/internal/takfeed"
	"github.com/openmanet/openmanetd/internal/util/board"
)

// gpsPosition adapts the GNSS service to takfeed.PositionSource. A nil
// service (GNSS disabled) reports no fix.
type gpsPosition struct {
	gps *gpsd.GPSService
}

func (g gpsPosition) Position() (takfeed.Position, bool) {
	if g.gps == nil {
		return takfeed.Position{}, false
	}

	p := g.gps.GetPosition()
	if !p.Valid {
		return takfeed.Position{}, false
	}

	return takfeed.Position{
		Time:   p.Timestamp,
		Lat:    p.Latitude,
		Lon:    p.Longitude,
		HAE:    p.Altitude + p.GeoidSeparation,
		CE:     p.EPH,
		LE:     p.EPV,
		Speed:  p.Speed,
		Course: p.Track,
	}, true
}

// startTAKFeed starts the ATAK feed goroutine (GHO-94). It always runs:
// tak.enable is read every tick, so turning the feed on or off in
// config.yml applies without a restart. The returned channel closes once
// the goroutine has exited after ctx is canceled; wait on it before the
// database closes.
func startTAKFeed(
	ctx context.Context,
	cfg *config.Config,
	gps *gpsd.GPSService,
	db *models.Queries,
	api *server.APIServer,
	scannerMgr *scanner.Manager,
	log zerolog.Logger,
) <-chan struct{} {
	opts := takFeedOptions(cfg, gps, db, api, scannerMgr, log)

	if problems := cfg.GetTAK().Problems; len(problems) > 0 {
		log.Warn().Strs("problems", problems).Msg("ATAK feed: rejected tak settings, using defaults for them")
	}

	done := make(chan struct{})

	go func() {
		defer close(done)

		takfeed.New(opts).Run(ctx)
	}()

	return done
}

func takFeedOptions(
	cfg *config.Config,
	gps *gpsd.GPSService,
	db *models.Queries,
	api *server.APIServer,
	scannerMgr *scanner.Manager,
	log zerolog.Logger,
) takfeed.Options {
	hostname, err := os.Hostname()
	if err != nil {
		hostname = ""
	}

	opts := takfeed.Options{
		Log:      log,
		Config:   cfg,
		Sender:   &cotmcast.Sender{MeshInterface: cfg.GetMeshNetInterface},
		Position: gpsPosition{gps: gps},
		Callsign: gpsd.NodeCallsign(),
		Hostname: hostname,
		// takfeed.AircraftSource and JamSource stay nil until wired below;
		// a nil interface (not a typed nil pointer) disables that layer.
	}

	if info, infoErr := board.NewBoardConfigInfo(); infoErr == nil && info != nil {
		opts.Platform = info.Model.GetName()
	}

	if gps != nil {
		opts.CameraOwnsMarker = gps.CameraPresent()
	}

	if db != nil {
		opts.Nodes = db
	}

	if api != nil {
		if api.MeshTopology != nil {
			opts.Topology = api.MeshTopology
		}

		if api.Battery != nil {
			opts.Battery = api.Battery
		}
	}

	if scannerMgr != nil {
		opts.Aircraft = scannerMgr
	}

	return opts
}
