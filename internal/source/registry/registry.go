// Package registry builds the configured data sources and exposes them by
// name to the HTTP layer.
package registry

import (
	"context"
	"fmt"
	"sort"

	"github.com/GeoVerseLabs/geoverse-map-server/internal/config"
	"github.com/GeoVerseLabs/geoverse-map-server/internal/source"
	"github.com/GeoVerseLabs/geoverse-map-server/internal/source/geojsonsrc"
	"github.com/GeoVerseLabs/geoverse-map-server/internal/source/geopackage"
	"github.com/GeoVerseLabs/geoverse-map-server/internal/source/mbtiles"
	"github.com/GeoVerseLabs/geoverse-map-server/internal/source/postgis"
)

// Registry holds all configured sources keyed by name.
type Registry struct {
	sources map[string]source.Source
	order   []string
}

// Build constructs every source in cfg, failing fast on the first error.
func Build(ctx context.Context, cfg *config.Config) (*Registry, error) {
	r := &Registry{sources: map[string]source.Source{}}
	for _, sc := range cfg.Sources {
		var (
			s   source.Source
			err error
		)
		switch sc.Type {
		case "postgis":
			s, err = postgis.New(ctx, sc)
		case "mbtiles":
			s, err = mbtiles.New(sc)
		case "geojson":
			s, err = geojsonsrc.New(sc)
		case "geopackage":
			s, err = geopackage.New(sc)
		default:
			err = fmt.Errorf("unknown source type %q", sc.Type)
		}
		if err != nil {
			r.Close()
			return nil, err
		}
		r.sources[sc.Name] = s
		r.order = append(r.order, sc.Name)
	}
	return r, nil
}

// Get returns the source with the given name.
func (r *Registry) Get(name string) (source.Source, bool) {
	s, ok := r.sources[name]
	return s, ok
}

// TileSource returns the named source if it serves tiles.
func (r *Registry) TileSource(name string) (source.TileSource, bool) {
	if s, ok := r.sources[name]; ok {
		ts, ok := s.(source.TileSource)
		return ts, ok
	}
	return nil, false
}

// FeatureSource returns the named source if it serves features.
func (r *Registry) FeatureSource(name string) (source.FeatureSource, bool) {
	if s, ok := r.sources[name]; ok {
		fs, ok := s.(source.FeatureSource)
		return fs, ok
	}
	return nil, false
}

// Names returns all source names in configuration order.
func (r *Registry) Names() []string {
	out := make([]string, len(r.order))
	copy(out, r.order)
	return out
}

// TileSources returns every tile-serving source in configuration order.
func (r *Registry) TileSources() []source.TileSource {
	var out []source.TileSource
	for _, n := range r.order {
		if ts, ok := r.sources[n].(source.TileSource); ok {
			out = append(out, ts)
		}
	}
	return out
}

// FeatureSources returns every feature-serving source in configuration order.
func (r *Registry) FeatureSources() []source.FeatureSource {
	var out []source.FeatureSource
	for _, n := range r.order {
		if fs, ok := r.sources[n].(source.FeatureSource); ok {
			out = append(out, fs)
		}
	}
	return out
}

// Ping checks every source and returns a map of name -> error (nil = ok).
func (r *Registry) Ping(ctx context.Context) map[string]error {
	out := map[string]error{}
	for n, s := range r.sources {
		out[n] = s.Ping(ctx)
	}
	return out
}

// Close shuts down all sources.
func (r *Registry) Close() {
	names := make([]string, 0, len(r.sources))
	for n := range r.sources {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		_ = r.sources[n].Close()
	}
}
