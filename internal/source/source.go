// Package source defines the data-source abstraction shared by all
// backends (PostGIS, MBTiles, GeoJSON, GeoPackage) and the registry that
// builds them from configuration.
package source

import (
	"context"
	"errors"

	"github.com/paulmach/orb/geojson"
)

// ErrTileNotFound is returned when a tile does not exist in the source
// (as opposed to an I/O failure). Handlers translate it to 204/404.
var ErrTileNotFound = errors.New("tile not found")

// ErrFeatureNotFound is returned for unknown feature ids.
var ErrFeatureNotFound = errors.New("feature not found")

// TileFormat identifies the payload format of a tile source.
type TileFormat string

const (
	FormatMVT  TileFormat = "pbf"
	FormatPNG  TileFormat = "png"
	FormatJPG  TileFormat = "jpg"
	FormatWebP TileFormat = "webp"
)

// ContentType returns the MIME type for the format.
func (f TileFormat) ContentType() string {
	switch f {
	case FormatMVT:
		return "application/vnd.mapbox-vector-tile"
	case FormatPNG:
		return "image/png"
	case FormatJPG:
		return "image/jpeg"
	case FormatWebP:
		return "image/webp"
	}
	return "application/octet-stream"
}

// TileInfo describes a tile layer for TileJSON / WMTS documents.
type TileInfo struct {
	Name        string
	Title       string
	Description string
	Format      TileFormat
	MinZoom     int
	MaxZoom     int
	// Bounds in WGS84: west, south, east, north.
	Bounds [4]float64
	// Center: lon, lat, zoom.
	Center [3]float64
	// VectorLayers describes MVT layers (TileJSON vector_layers).
	VectorLayers []VectorLayer
	// Gzipped reports whether Tile() payloads are already gzip-compressed.
	Gzipped bool
	// Cacheable hints whether the process-level tile cache helps
	// (false for sources that are already fast local lookups).
	Cacheable bool
}

// VectorLayer is one named layer inside an MVT tile.
type VectorLayer struct {
	ID     string            `json:"id"`
	Fields map[string]string `json:"fields"`
}

// TileSource serves z/x/y tiles in XYZ (y down) addressing.
type TileSource interface {
	Tile(ctx context.Context, z, x, y uint32) ([]byte, error)
	TileInfo() TileInfo
}

// FeatureQuery holds OGC API - Features query parameters.
type FeatureQuery struct {
	// BBox in WGS84 (west, south, east, north); nil = no filter.
	BBox   *[4]float64
	Limit  int
	Offset int
}

// FeatureResult is a page of features plus the total match count.
type FeatureResult struct {
	Features []*geojson.Feature
	// NumberMatched is the total number of features matching the query
	// ignoring paging; -1 if unknown.
	NumberMatched int
}

// CollectionInfo describes a feature collection.
type CollectionInfo struct {
	Name        string
	Title       string
	Description string
	// Bounds in WGS84: west, south, east, north.
	Bounds [4]float64
}

// FeatureSource serves OGC API - Features queries.
type FeatureSource interface {
	Features(ctx context.Context, q FeatureQuery) (*FeatureResult, error)
	Feature(ctx context.Context, id string) (*geojson.Feature, error)
	CollectionInfo() CollectionInfo
}

// Source is a configured backend; it may implement TileSource,
// FeatureSource, or both.
type Source interface {
	Name() string
	// Ping verifies the source is reachable/healthy.
	Ping(ctx context.Context) error
	Close() error
}
