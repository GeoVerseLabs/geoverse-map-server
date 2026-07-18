// Package memengine implements an in-memory feature store that can encode
// Mapbox Vector Tiles on the fly and answer OGC API - Features queries.
// It backs the GeoJSON and GeoPackage sources, which only differ in how
// features are loaded.
package memengine

import (
	"context"
	"fmt"
	"sort"

	"github.com/paulmach/orb"
	"github.com/paulmach/orb/encoding/mvt"
	"github.com/paulmach/orb/geojson"
	"github.com/paulmach/orb/maptile"
	"github.com/paulmach/orb/simplify"

	"github.com/GeoVerseLabs/geoverse-map-server/internal/source"
)

const gridDim = 64 // grid index resolution per axis

// Options tune tile generation.
type Options struct {
	Name        string
	Title       string
	Description string
	MinZoom     int
	MaxZoom     int
	Simplify    bool
}

// Engine is an immutable in-memory feature store. It is safe for
// concurrent use after Load.
type Engine struct {
	opts     Options
	features []*geojson.Feature
	bounds   []orb.Bound // per-feature bounds, parallel to features
	ids      []string    // stringified feature ids, parallel to features
	byID     map[string]int
	extent   orb.Bound // dataset bounds in WGS84
	fields   map[string]string
	grid     [][]int32 // gridDim*gridDim cells of feature indices
}

// New builds an engine over the given WGS84 features. Feature ids are
// taken from the GeoJSON id member when present, otherwise assigned
// sequentially.
func New(opts Options, feats []*geojson.Feature) (*Engine, error) {
	if opts.MaxZoom == 0 {
		opts.MaxZoom = 22
	}
	e := &Engine{
		opts:   opts,
		byID:   map[string]int{},
		fields: map[string]string{},
		grid:   make([][]int32, gridDim*gridDim),
	}
	for _, f := range feats {
		if f == nil || f.Geometry == nil {
			continue
		}
		i := len(e.features)
		id := fmt.Sprint(i)
		if f.ID != nil {
			id = fmt.Sprint(f.ID)
		} else {
			f.ID = i
		}
		e.features = append(e.features, f)
		b := f.Geometry.Bound()
		e.bounds = append(e.bounds, b)
		e.ids = append(e.ids, id)
		if _, dup := e.byID[id]; !dup {
			e.byID[id] = i
		}
		if i == 0 {
			e.extent = b
		} else {
			e.extent = e.extent.Union(b)
		}
		for k, v := range f.Properties {
			if _, ok := e.fields[k]; !ok {
				e.fields[k] = jsonType(v)
			}
		}
	}
	if len(e.features) == 0 {
		return nil, fmt.Errorf("source %q: no usable features", opts.Name)
	}
	e.buildGrid()
	return e, nil
}

func jsonType(v interface{}) string {
	switch v.(type) {
	case float64, float32, int, int64, uint64:
		return "Number"
	case bool:
		return "Boolean"
	default:
		return "String"
	}
}

func (e *Engine) buildGrid() {
	for i, b := range e.bounds {
		x0, y0 := e.cellOf(b.Min)
		x1, y1 := e.cellOf(b.Max)
		for y := y0; y <= y1; y++ {
			for x := x0; x <= x1; x++ {
				c := y*gridDim + x
				e.grid[c] = append(e.grid[c], int32(i))
			}
		}
	}
}

func (e *Engine) cellOf(p orb.Point) (int, int) {
	w := e.extent.Max[0] - e.extent.Min[0]
	h := e.extent.Max[1] - e.extent.Min[1]
	x, y := 0, 0
	if w > 0 {
		x = int((p[0] - e.extent.Min[0]) / w * gridDim)
	}
	if h > 0 {
		y = int((p[1] - e.extent.Min[1]) / h * gridDim)
	}
	return clampi(x, 0, gridDim-1), clampi(y, 0, gridDim-1)
}

func clampi(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

// query returns indices of features whose bounds intersect b, in id order.
func (e *Engine) query(b orb.Bound) []int {
	if !b.Intersects(e.extent) {
		return nil
	}
	x0, y0 := e.cellOf(b.Min)
	x1, y1 := e.cellOf(b.Max)
	seen := map[int32]struct{}{}
	var out []int
	for y := y0; y <= y1; y++ {
		for x := x0; x <= x1; x++ {
			for _, i := range e.grid[y*gridDim+x] {
				if _, ok := seen[i]; ok {
					continue
				}
				seen[i] = struct{}{}
				if e.bounds[i].Intersects(b) {
					out = append(out, int(i))
				}
			}
		}
	}
	sort.Ints(out)
	return out
}

// Tile encodes the features intersecting tile z/x/y as a gzipped MVT.
func (e *Engine) Tile(_ context.Context, z, x, y uint32) ([]byte, error) {
	t := maptile.New(x, y, maptile.Zoom(z))
	// Select with a ~10% buffer so clipped geometries keep context.
	sel := e.query(t.Bound(0.1))
	if len(sel) == 0 {
		return nil, source.ErrTileNotFound
	}
	fc := geojson.NewFeatureCollection()
	for _, i := range sel {
		src := e.features[i]
		f := geojson.NewFeature(orb.Clone(src.Geometry))
		f.ID = src.ID
		f.Properties = src.Properties
		fc.Append(f)
	}
	layer := mvt.NewLayer(e.opts.Name, fc)
	layer.Version = 2
	layer.ProjectToTile(t)
	layer.Clip(mvt.MapboxGLDefaultExtentBound)
	if e.opts.Simplify {
		layer.Simplify(simplify.DouglasPeucker(1.0))
		layer.RemoveEmpty(1.0, 2.0)
	}
	if len(layer.Features) == 0 {
		return nil, source.ErrTileNotFound
	}
	data, err := mvt.MarshalGzipped(mvt.Layers{layer})
	if err != nil {
		return nil, fmt.Errorf("encode mvt: %w", err)
	}
	return data, nil
}

// TileInfo implements source.TileSource.
func (e *Engine) TileInfo() source.TileInfo {
	b := e.extent
	cx := (b.Min[0] + b.Max[0]) / 2
	cy := (b.Min[1] + b.Max[1]) / 2
	return source.TileInfo{
		Name:        e.opts.Name,
		Title:       e.opts.Title,
		Description: e.opts.Description,
		Format:      source.FormatMVT,
		MinZoom:     e.opts.MinZoom,
		MaxZoom:     e.opts.MaxZoom,
		Bounds:      [4]float64{b.Min[0], b.Min[1], b.Max[0], b.Max[1]},
		Center:      [3]float64{cx, cy, float64(clampi((e.opts.MinZoom+e.opts.MaxZoom)/2, 0, 14))},
		VectorLayers: []source.VectorLayer{{
			ID:     e.opts.Name,
			Fields: e.fields,
		}},
		Gzipped:   true,
		Cacheable: true,
	}
}

// Features implements source.FeatureSource.
func (e *Engine) Features(_ context.Context, q source.FeatureQuery) (*source.FeatureResult, error) {
	var idx []int
	if q.BBox != nil {
		b := orb.Bound{
			Min: orb.Point{q.BBox[0], q.BBox[1]},
			Max: orb.Point{q.BBox[2], q.BBox[3]},
		}
		idx = e.query(b)
	} else {
		idx = make([]int, len(e.features))
		for i := range idx {
			idx[i] = i
		}
	}
	total := len(idx)
	if q.Offset > 0 {
		if q.Offset >= len(idx) {
			idx = nil
		} else {
			idx = idx[q.Offset:]
		}
	}
	if q.Limit > 0 && q.Limit < len(idx) {
		idx = idx[:q.Limit]
	}
	res := &source.FeatureResult{NumberMatched: total}
	for _, i := range idx {
		res.Features = append(res.Features, e.features[i])
	}
	return res, nil
}

// Feature implements source.FeatureSource.
func (e *Engine) Feature(_ context.Context, id string) (*geojson.Feature, error) {
	i, ok := e.byID[id]
	if !ok {
		return nil, source.ErrFeatureNotFound
	}
	return e.features[i], nil
}

// CollectionInfo implements source.FeatureSource.
func (e *Engine) CollectionInfo() source.CollectionInfo {
	b := e.extent
	return source.CollectionInfo{
		Name:        e.opts.Name,
		Title:       e.opts.Title,
		Description: e.opts.Description,
		Bounds:      [4]float64{b.Min[0], b.Min[1], b.Max[0], b.Max[1]},
	}
}

// Count returns the number of loaded features.
func (e *Engine) Count() int { return len(e.features) }
