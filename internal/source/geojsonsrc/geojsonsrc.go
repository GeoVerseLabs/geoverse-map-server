// Package geojsonsrc exposes a static GeoJSON file as both a vector tile
// source and an OGC API - Features collection, via the in-memory engine.
package geojsonsrc

import (
	"context"
	"fmt"
	"os"

	"github.com/paulmach/orb/geojson"

	"github.com/GeoVerseLabs/geoverse-map-server/internal/config"
	"github.com/GeoVerseLabs/geoverse-map-server/internal/source"
	"github.com/GeoVerseLabs/geoverse-map-server/internal/source/memengine"
)

// Source is a GeoJSON-file-backed source.
type Source struct {
	*memengine.Engine
	name string
	path string
}

var (
	_ source.Source        = (*Source)(nil)
	_ source.TileSource    = (*Source)(nil)
	_ source.FeatureSource = (*Source)(nil)
)

// New loads the GeoJSON file referenced by cfg. The file may contain a
// FeatureCollection, a single Feature, or a bare geometry.
func New(cfg config.Source) (*Source, error) {
	raw, err := os.ReadFile(cfg.Path)
	if err != nil {
		return nil, fmt.Errorf("source %q: %w", cfg.Name, err)
	}
	fc, err := parse(raw)
	if err != nil {
		return nil, fmt.Errorf("source %q: parse %s: %w", cfg.Name, cfg.Path, err)
	}
	opts := memengine.Options{
		Name:        cfg.Name,
		Title:       cfg.Title,
		Description: cfg.Description,
		Simplify:    cfg.Simplify == nil || *cfg.Simplify,
	}
	if cfg.MinZoom != nil {
		opts.MinZoom = *cfg.MinZoom
	}
	if cfg.MaxZoom != nil {
		opts.MaxZoom = *cfg.MaxZoom
	}
	eng, err := memengine.New(opts, fc.Features)
	if err != nil {
		return nil, err
	}
	return &Source{Engine: eng, name: cfg.Name, path: cfg.Path}, nil
}

func parse(raw []byte) (*geojson.FeatureCollection, error) {
	if fc, err := geojson.UnmarshalFeatureCollection(raw); err == nil {
		return fc, nil
	}
	if f, err := geojson.UnmarshalFeature(raw); err == nil {
		fc := geojson.NewFeatureCollection()
		fc.Append(f)
		return fc, nil
	}
	g, err := geojson.UnmarshalGeometry(raw)
	if err != nil {
		return nil, fmt.Errorf("not a FeatureCollection, Feature or Geometry")
	}
	fc := geojson.NewFeatureCollection()
	fc.Append(geojson.NewFeature(g.Geometry()))
	return fc, nil
}

// Name implements source.Source.
func (s *Source) Name() string { return s.name }

// Ping implements source.Source; the data is in memory, so always healthy.
func (s *Source) Ping(context.Context) error { return nil }

// Close implements source.Source.
func (s *Source) Close() error { return nil }
