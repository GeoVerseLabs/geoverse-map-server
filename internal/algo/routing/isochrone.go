package routing

import (
	"context"
	"encoding/json"
	"math"
	"sort"

	"github.com/paulmach/orb"
	"github.com/paulmach/orb/geojson"

	"github.com/GeoVerseLabs/geoverse-map-server/internal/algo"
	"github.com/GeoVerseLabs/geoverse-map-server/internal/algo/network"
)

// Isochrone computes reachable-area contours around an origin. Instead of
// a convex hull over reached nodes (which badly overestimates along
// sparse networks), it interpolates exact cutoff points on frontier
// edges, rasterizes every traversed segment onto a grid, and extracts
// contour polygons with marching squares — the approach popularized by
// Valhalla-style gridded isochrones. Holes (unreachable enclaves) are
// preserved.
type Isochrone struct{}

// Describe implements algo.Algorithm.
func (Isochrone) Describe() algo.Descriptor {
	return algo.Descriptor{
		Name:        "isochrone",
		Title:       "等时圈",
		Description: "Reachable-area polygons around an origin for one or more time (or distance) budgets over a configured network. Returns one MultiPolygon feature per cutoff, largest first.",
		InputSchema: map[string]interface{}{
			"type":     "object",
			"required": []string{"network", "origin", "cutoffs"},
			"properties": map[string]interface{}{
				"network": map[string]interface{}{"type": "string", "description": "Configured network name"},
				"origin":  map[string]interface{}{"type": "array", "items": map[string]interface{}{"type": "number"}, "minItems": 2, "maxItems": 2, "description": "[lon, lat] origin"},
				"cutoffs": map[string]interface{}{"type": "array", "items": map[string]interface{}{"type": "number"}, "minItems": 1, "maxItems": 8, "description": "Budgets in seconds (cost=time) or meters (cost=distance), e.g. [300, 600, 900]"},
				"cost":    map[string]interface{}{"type": "string", "enum": []string{"time", "distance"}, "description": "Budget unit: time (default) or distance"},
				"level":   map[string]interface{}{"type": "integer", "description": "Origin floor for indoor networks (default 0)"},
				"cell_size_m": map[string]interface{}{"type": "number",
					"description": "Raster cell size in meters; smaller = finer contours (default: auto, reach/128)"},
				"snap_radius_m": map[string]interface{}{"type": "number", "description": "Max distance to snap the origin onto the network (default 500m)"},
			},
		},
	}
}

type isochroneParams struct {
	Network     string     `json:"network"`
	Origin      *[]float64 `json:"origin"`
	Cutoffs     []float64  `json:"cutoffs"`
	Cost        string     `json:"cost"`
	Level       int        `json:"level"`
	CellSizeM   float64    `json:"cell_size_m"`
	SnapRadiusM float64    `json:"snap_radius_m"`
}

// Run implements algo.Algorithm.
func (Isochrone) Run(ctx context.Context, env *algo.Env, raw json.RawMessage) (interface{}, error) {
	var p isochroneParams
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, algo.Errorf("invalid params: %v", err)
	}
	origin, err := parsePoint(p.Origin, "origin")
	if err != nil {
		return nil, err
	}
	if len(p.Cutoffs) == 0 || len(p.Cutoffs) > 8 {
		return nil, algo.Errorf("cutoffs must contain 1..8 positive numbers")
	}
	for _, c := range p.Cutoffs {
		if c <= 0 {
			return nil, algo.Errorf("cutoffs must be positive, got %v", c)
		}
	}
	cost, _, err := costFunc(p.Cost)
	if err != nil {
		return nil, err
	}
	g, err := env.Networks.Graph(ctx, p.Network)
	if err != nil {
		return nil, algo.Errorf("%v", err)
	}
	snap := p.SnapRadiusM
	if snap <= 0 {
		snap = defaultSnapRadiusM
	}
	src, _, ok := g.NearestNode(origin, int16(p.Level), snap)
	if !ok {
		return nil, algo.Errorf("no network node within %.0fm of origin on level %d", snap, p.Level)
	}

	cutoffs := append([]float64(nil), p.Cutoffs...)
	sort.Float64s(cutoffs)
	maxCut := cutoffs[len(cutoffs)-1]
	res := g.Dijkstra(src, cost, maxCut, -1)

	fc := geojson.NewFeatureCollection()
	// Emit largest cutoff first so smaller rings draw on top.
	for i := len(cutoffs) - 1; i >= 0; i-- {
		cut := cutoffs[i]
		mp, reached := contourForCutoff(g, res, cut, cost, p.CellSizeM)
		if len(mp) == 0 {
			continue
		}
		f := geojson.NewFeature(mp)
		f.Properties = geojson.Properties{
			"cutoff":        cut,
			"cost":          costName(p.Cost),
			"reached_nodes": reached,
		}
		fc.Append(f)
	}
	if len(fc.Features) == 0 {
		return nil, algo.Errorf("nothing reachable within the given cutoffs")
	}
	return fc, nil
}

func costName(c string) string {
	if c == "" {
		return "time"
	}
	return c
}

// contourForCutoff rasterizes the sub-cutoff portion of the search tree
// and extracts contour polygons.
func contourForCutoff(g *network.Graph, res *network.SearchResult, cutoff float64, cost network.CostFunc, cellSizeM float64) (orb.MultiPolygon, int) {
	// Collect reached geometry: full segments where both ends are within
	// budget, partial segments interpolated exactly at the cutoff.
	type seg struct{ a, b orb.Point }
	var segs []seg
	minP := orb.Point{math.Inf(1), math.Inf(1)}
	maxP := orb.Point{math.Inf(-1), math.Inf(-1)}
	grow := func(p orb.Point) {
		minP[0] = math.Min(minP[0], p[0])
		minP[1] = math.Min(minP[1], p[1])
		maxP[0] = math.Max(maxP[0], p[0])
		maxP[1] = math.Max(maxP[1], p[1])
	}
	reached := 0
	for u := range g.Nodes {
		cu := res.Cost[u]
		if math.IsInf(cu, 1) || cu > cutoff {
			continue
		}
		reached++
		pu := g.Nodes[u].Pt
		grow(pu)
		for _, ei := range g.Adj[u] {
			e := &g.Edges[ei]
			c := cost(e)
			pv := g.Nodes[e.To].Pt
			if cu+c <= cutoff && res.Cost[e.To] <= cutoff {
				segs = append(segs, seg{pu, pv})
				grow(pv)
			} else if c > 0 {
				f := (cutoff - cu) / c
				if f > 0 {
					mid := orb.Point{pu[0] + (pv[0]-pu[0])*f, pu[1] + (pv[1]-pu[1])*f}
					segs = append(segs, seg{pu, mid})
					grow(mid)
				}
			}
		}
	}
	if reached == 0 || len(segs) == 0 {
		return nil, reached
	}

	lonM, latM := 111320*math.Cos((minP[1]+maxP[1])/2*math.Pi/180), 111132.0
	if lonM < 1 {
		lonM = 1
	}
	extentM := math.Max((maxP[0]-minP[0])*lonM, (maxP[1]-minP[1])*latM)
	cell := cellSizeM
	if cell <= 0 {
		cell = math.Max(extentM/128, 5)
	}
	cw, ch := cell/lonM, cell/latM
	// Pad 2 cells so dilation and contours never touch the border.
	pad := 2.0
	nx := int((maxP[0]-minP[0])/cw) + int(2*pad) + 1
	ny := int((maxP[1]-minP[1])/ch) + int(2*pad) + 1
	grid := newBinaryGrid(nx, ny, minP[0]-pad*cw, minP[1]-pad*ch, cw, ch)
	for _, s := range segs {
		grid.markSegment(s.a, s.b)
	}
	grid.dilate()
	rings := grid.contours()
	return assemblePolygons(rings, cw/2), reached
}
