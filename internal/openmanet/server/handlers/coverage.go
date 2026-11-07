package handlers

import (
	"context"

	"github.com/rs/zerolog"

	coveragev1 "github.com/openmanet/openmanetd/internal/api/openmanet/coverage/v1"
	"github.com/openmanet/openmanetd/internal/config"
)

// CoverageMap is the coverage surface CoverageService needs.
// *coverage.Sharer implements it.
type CoverageMap interface {
	Local() *coveragev1.CoverageReport
	Remote() []*coveragev1.CoverageReport
}

// CoverageService serves the mesh coverage map.
type CoverageService struct {
	Log zerolog.Logger
	Cfg *config.Config
	// Map is nil when the node has no database to keep a map in. It must
	// be a nil interface, not a typed nil pointer.
	Map CoverageMap
}

// GetCoverageMap returns this node's map and every other node's latest.
// With tak.coverage.enable false it reports enabled false and no cells.
func (s *CoverageService) GetCoverageMap(
	_ context.Context,
	_ *coveragev1.GetCoverageMapRequest,
) (*coveragev1.GetCoverageMapResponse, error) {
	if s.Map == nil || s.Cfg == nil || !s.Cfg.GetTAK().CoverageEnable {
		return &coveragev1.GetCoverageMapResponse{}, nil
	}

	return &coveragev1.GetCoverageMapResponse{
		Enabled: true,
		Local:   s.Map.Local(),
		Remote:  s.Map.Remote(),
	}, nil
}
