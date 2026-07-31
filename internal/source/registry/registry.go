// Package registry builds the configured data sources and exposes them by
// name to the HTTP layer.
package registry

import (
	"context"
	"fmt"
	"sort"
	"sync"

	"github.com/GeoVerseLabs/geoverse-map-server/internal/config"
	"github.com/GeoVerseLabs/geoverse-map-server/internal/source"
	"github.com/GeoVerseLabs/geoverse-map-server/internal/source/geojsonsrc"
	"github.com/GeoVerseLabs/geoverse-map-server/internal/source/geopackage"
	"github.com/GeoVerseLabs/geoverse-map-server/internal/source/mbtiles"
	"github.com/GeoVerseLabs/geoverse-map-server/internal/source/mysql"
	"github.com/GeoVerseLabs/geoverse-map-server/internal/source/pmtiles"
	"github.com/GeoVerseLabs/geoverse-map-server/internal/source/postgis"
)

// Registry holds all configured sources keyed by name.
type Registry struct {
	mu      sync.RWMutex
	sources map[string]source.Source
	order   []string
	// retired sources stay open until shutdown. A handler may already hold a
	// source interface when the WebUI replaces it; deferred close avoids
	// racing that in-flight request while keeping mutations infrequent and
	// predictable.
	retired []source.Source
}

// Build constructs every source in cfg, failing fast on the first error.
func Build(ctx context.Context, cfg *config.Config) (*Registry, error) {
	r := &Registry{sources: map[string]source.Source{}}
	for _, sc := range cfg.Sources {
		s, err := Open(ctx, sc)
		if err != nil {
			r.Close()
			return nil, err
		}
		r.sources[sc.Name] = s
		r.order = append(r.order, sc.Name)
	}
	return r, nil
}

// Open constructs one source. Management endpoints use it to probe a
// candidate before it is persisted or made visible to live requests.
func Open(ctx context.Context, sc config.Source) (source.Source, error) {
	switch sc.Type {
	case "postgis":
		return postgis.New(ctx, sc)
	case "mysql":
		return mysql.New(ctx, sc)
	case "mbtiles":
		return mbtiles.New(sc)
	case "pmtiles":
		return pmtiles.New(sc)
	case "geojson":
		return geojsonsrc.New(sc)
	case "geopackage":
		return geopackage.New(sc)
	default:
		return nil, fmt.Errorf("unknown source type %q", sc.Type)
	}
}

// Replace publishes a pre-opened source under its configured name.
func (r *Registry) Replace(name string, replacement source.Source) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if old, ok := r.sources[name]; ok {
		r.retired = append(r.retired, old)
	} else {
		r.order = append(r.order, name)
	}
	r.sources[name] = replacement
}

// Remove hides a source from new requests. Its resources are released when
// the registry closes so in-flight requests cannot observe a closed backend.
func (r *Registry) Remove(name string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	old, ok := r.sources[name]
	if !ok {
		return false
	}
	delete(r.sources, name)
	r.retired = append(r.retired, old)
	for i, n := range r.order {
		if n == name {
			r.order = append(r.order[:i], r.order[i+1:]...)
			break
		}
	}
	return true
}

// Get returns the source with the given name.
func (r *Registry) Get(name string) (source.Source, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	s, ok := r.sources[name]
	return s, ok
}

// TileSource returns the named source if it serves tiles.
func (r *Registry) TileSource(name string) (source.TileSource, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if s, ok := r.sources[name]; ok {
		ts, ok := s.(source.TileSource)
		return ts, ok
	}
	return nil, false
}

// FeatureSource returns the named source if it serves features.
func (r *Registry) FeatureSource(name string) (source.FeatureSource, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if s, ok := r.sources[name]; ok {
		fs, ok := s.(source.FeatureSource)
		return fs, ok
	}
	return nil, false
}

// Names returns all source names in configuration order.
func (r *Registry) Names() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]string, len(r.order))
	copy(out, r.order)
	return out
}

// TileSources returns every tile-serving source in configuration order.
func (r *Registry) TileSources() []source.TileSource {
	r.mu.RLock()
	defer r.mu.RUnlock()
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
	r.mu.RLock()
	defer r.mu.RUnlock()
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
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := map[string]error{}
	for n, s := range r.sources {
		out[n] = s.Ping(ctx)
	}
	return out
}

// Close shuts down all sources.
func (r *Registry) Close() {
	r.mu.Lock()
	defer r.mu.Unlock()
	names := make([]string, 0, len(r.sources))
	for n := range r.sources {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		_ = r.sources[n].Close()
	}
	for _, s := range r.retired {
		_ = s.Close()
	}
	r.sources = map[string]source.Source{}
	r.retired = nil
	r.order = nil
}
