package frontend

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"

	"github.com/openmanet/openmanetd/internal/config"
	"github.com/openmanet/openmanetd/internal/tiles"
	"github.com/openmanet/openmanetd/internal/util/logger"
)

// Map tile endpoints (GHO-95).
//
// Public (no login), because ATAK fetches tiles with no session:
//
//	GET /tiles/<layer>/<z>/<x>/<y>   one map tile
//
// Behind login:
//
//	GET    /api/tiles/status   cache size, sources, download progress
//	POST   /api/tiles/seed     download an area ahead of time
//	DELETE /api/tiles/seed     stop that download
const (
	tilesStatusPath = "/api/tiles/status"
	tilesSeedPath   = "/api/tiles/seed"
	maxSeedBody     = 4 << 10
)

// newTileCache opens and starts the tile cache, or returns nil when it is
// disabled or its directory cannot be created. The phone settings
// package then leaves map sources out (see servedMapSources).
func newTileCache(ctx context.Context, cfg *config.Config, s *Server) *tiles.Cache {
	tc := cfg.GetTiles()
	if len(tc.Problems) > 0 {
		s.log.Warn().Strs("problems", tc.Problems).Msg("tiles: rejected settings, using defaults for them")
	}

	if !tc.Enable {
		return nil
	}

	c, err := tiles.New(tiles.Options{Config: tc, Log: logger.GetLogger("tiles")})
	if err != nil {
		s.log.Warn().Err(err).Str("dir", tc.Dir).Msg("map tiles disabled")

		return nil
	}

	c.Start(ctx)

	return c
}

// servedMapSources returns the onboarding map sources this node actually
// serves. A source whose layer is not served, or has another tile type,
// is dropped, and zooms are clipped to the served range, so a phone is
// never handed a map that loads blank.
func (s *Server) servedMapSources() []config.OnboardMapSource {
	sources := s.cfg.GetOnboard().MapSources
	out := make([]config.OnboardMapSource, 0, len(sources))

	for _, src := range sources {
		if s.tiles == nil {
			continue
		}

		l, ok := s.tiles.Layer(src.Layer)
		if !ok || l.TileType != src.TileType {
			continue
		}

		src.MinZoom = max(src.MinZoom, l.MinZoom)
		src.MaxZoom = min(src.MaxZoom, l.MaxZoom)

		if src.MinZoom > src.MaxZoom {
			continue
		}

		out = append(out, src)
	}

	return out
}

// registerTilesPublic adds the tile route to root.
func (s *Server) registerTilesPublic(root *http.ServeMux) {
	if s.tiles == nil {
		return
	}

	root.Handle(tiles.PathPrefix, s.tiles)
}

// registerTilesAdmin adds the login-protected tile routes to mux. The
// status route is always there so the settings page can say the cache
// is off.
func (s *Server) registerTilesAdmin(mux *http.ServeMux) {
	mux.HandleFunc(tilesStatusPath, s.handleTilesStatus)
	mux.HandleFunc(tilesSeedPath, s.handleTilesSeed)
}

type tilesStatusResponse struct {
	tiles.Status

	Enabled bool `json:"enabled"`
}

func (s *Server) handleTilesStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		s.writeErrorStatus(w, http.StatusMethodNotAllowed, "method not allowed")

		return
	}

	resp := tilesStatusResponse{}
	if s.tiles != nil {
		resp.Enabled = true
		resp.Status = s.tiles.Status()
	}

	w.Header().Set("Cache-Control", "no-store")
	s.writeJSON(w, resp)
}

// seedRequest is the POST /api/tiles/seed body.
type seedRequest struct {
	Name     string   `json:"name"`
	Layers   []string `json:"layers"`
	Lat      float64  `json:"lat"`
	Lon      float64  `json:"lon"`
	RadiusKm float64  `json:"radiusKm"`
	MaxZoom  int      `json:"maxZoom"`
}

func (s *Server) handleTilesSeed(w http.ResponseWriter, r *http.Request) {
	if s.tiles == nil {
		s.writeErrorStatus(w, http.StatusServiceUnavailable, "map tiles are turned off on this node")

		return
	}

	switch r.Method {
	case http.MethodPost:
		s.handleTilesSeedStart(w, r)
	case http.MethodDelete:
		s.tiles.CancelSeed()
		w.WriteHeader(http.StatusNoContent)
	default:
		s.writeErrorStatus(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (s *Server) handleTilesSeedStart(w http.ResponseWriter, r *http.Request) {
	var req seedRequest

	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxSeedBody))
	dec.DisallowUnknownFields()

	if err := dec.Decode(&req); err != nil {
		s.writeErrorStatus(w, http.StatusBadRequest, "expected a JSON area: lat, lon, radiusKm, maxZoom")

		return
	}

	name := req.Name
	if name == "" {
		name = "manual area"
	}

	err := s.tiles.StartSeed(r.Context(), config.TileSeedArea{
		Name:     name,
		Layers:   req.Layers,
		Lat:      req.Lat,
		Lon:      req.Lon,
		RadiusKm: req.RadiusKm,
		MaxZoom:  req.MaxZoom,
	})

	switch {
	case err == nil:
		w.WriteHeader(http.StatusAccepted)
		s.writeJSON(w, s.tiles.Status().Seed)
	case errors.Is(err, tiles.ErrSeedRunning):
		s.writeErrorStatus(w, http.StatusConflict, err.Error())
	case errors.Is(err, tiles.ErrSeedTooLarge), errors.Is(err, tiles.ErrSeedBadArea):
		s.writeErrorStatus(w, http.StatusBadRequest, err.Error())
	default:
		s.writeErrorStatus(w, http.StatusServiceUnavailable, err.Error())
	}
}
