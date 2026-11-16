package tiles

import (
	"math"
)

// maxMercatorLat is the latitude where a Web Mercator map ends.
const maxMercatorLat = 85.05112878

// kmPerDegreeLat is the length of one degree of latitude.
const kmPerDegreeLat = 111.32

// tileRange is the inclusive x and y tile range covering an area at one
// zoom.
type tileRange struct {
	z, minX, maxX, minY, maxY int
}

func (r tileRange) count() int64 {
	return int64(r.maxX-r.minX+1) * int64(r.maxY-r.minY+1)
}

// tileX returns the column holding lon at zoom z.
func tileX(lon float64, z int) int {
	n := 1 << z
	x := int(math.Floor((lon + 180) / 360 * float64(n)))

	return clampTile(x, n)
}

// tileY returns the row holding lat at zoom z (row 0 is the north edge).
func tileY(lat float64, z int) int {
	n := 1 << z
	lat = math.Max(-maxMercatorLat, math.Min(maxMercatorLat, lat))
	rad := lat * math.Pi / 180
	y := int(math.Floor((1 - math.Log(math.Tan(rad)+1/math.Cos(rad))/math.Pi) / 2 * float64(n)))

	return clampTile(y, n)
}

func clampTile(v, n int) int {
	return max(0, min(v, n-1))
}

// areaRanges returns the tile range for each zoom in [minZ, maxZ] that
// covers a circle's bounding box. The box is clamped at the map edges
// rather than wrapped across the antimeridian.
func areaRanges(lat, lon, radiusKm float64, minZ, maxZ int) []tileRange {
	dLat := radiusKm / kmPerDegreeLat
	dLon := radiusKm / (kmPerDegreeLat * math.Max(math.Cos(lat*math.Pi/180), 0.01))

	north := math.Min(lat+dLat, maxMercatorLat)
	south := math.Max(lat-dLat, -maxMercatorLat)
	west := math.Max(lon-dLon, -180)
	east := math.Min(lon+dLon, 180-1e-9)

	out := make([]tileRange, 0, max(maxZ-minZ+1, 0))

	for z := minZ; z <= maxZ; z++ {
		out = append(out, tileRange{
			z:    z,
			minX: tileX(west, z),
			maxX: tileX(east, z),
			minY: tileY(north, z),
			maxY: tileY(south, z),
		})
	}

	return out
}
