// Package cluster implements clustering algorithms over feature
// collections.
package cluster

import (
	"context"
	"encoding/json"
	"math"

	"github.com/paulmach/orb"
	"github.com/paulmach/orb/geojson"

	"github.com/GeoVerseLabs/geoverse-map-server/internal/algo"
	"github.com/GeoVerseLabs/geoverse-map-server/internal/source"
)

// DBSCAN is density-based clustering over point features. The classic
// algorithm is O(n²) from pairwise distance checks; this implementation
// uses the standard grid-index acceleration — cells sized eps so each
// neighbourhood query only scans the 3×3 surrounding cells — giving
// near-linear expected time. Distances are geodesic-aware via a local
// equirectangular projection around the dataset centroid.
type DBSCAN struct{}

const (
	maxClusterFeatures = 200000
	noise              = -1
	unclassified       = -2
)

// Describe implements algo.Algorithm.
func (DBSCAN) Describe() algo.Descriptor {
	return algo.Descriptor{
		Name:        "dbscan",
		Title:       "DBSCAN 密度聚类",
		Description: "Density-based clustering of point features (grid-accelerated DBSCAN, metric distances in meters). Non-point geometries are represented by their centroid. Returns input points labeled with cluster ids plus per-cluster summaries; noise gets cluster -1.",
		InputSchema: map[string]interface{}{
			"type":     "object",
			"required": []string{"collection", "eps_m", "min_points"},
			"properties": map[string]interface{}{
				"collection": map[string]interface{}{"type": "string", "description": "Feature collection to cluster"},
				"eps_m":      map[string]interface{}{"type": "number", "description": "Neighbourhood radius in meters"},
				"min_points": map[string]interface{}{"type": "integer", "description": "Minimum neighbours (incl. self) for a core point"},
				"bbox": map[string]interface{}{"type": "array", "items": map[string]interface{}{"type": "number"}, "minItems": 4, "maxItems": 4,
					"description": "Optional [west, south, east, north] pre-filter"},
				"include_points": map[string]interface{}{"type": "boolean", "description": "Include labeled input points in the result (default true; set false for summaries only)"},
			},
		},
	}
}

type dbscanParams struct {
	Collection    string     `json:"collection"`
	EpsM          float64    `json:"eps_m"`
	MinPoints     int        `json:"min_points"`
	BBox          *[]float64 `json:"bbox"`
	IncludePoints *bool      `json:"include_points"`
}

// Run implements algo.Algorithm.
func (DBSCAN) Run(ctx context.Context, env *algo.Env, raw json.RawMessage) (interface{}, error) {
	var p dbscanParams
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, algo.Errorf("invalid params: %v", err)
	}
	if p.EpsM <= 0 {
		return nil, algo.Errorf("eps_m must be positive")
	}
	if p.MinPoints < 1 {
		return nil, algo.Errorf("min_points must be >= 1")
	}
	fs, ok := env.Features(p.Collection)
	if !ok {
		return nil, algo.Errorf("unknown collection %q", p.Collection)
	}
	q := source.FeatureQuery{Limit: maxClusterFeatures}
	if p.BBox != nil {
		if len(*p.BBox) != 4 {
			return nil, algo.Errorf("bbox must be [west, south, east, north]")
		}
		b := [4]float64{(*p.BBox)[0], (*p.BBox)[1], (*p.BBox)[2], (*p.BBox)[3]}
		q.BBox = &b
	}
	res, err := fs.Features(ctx, q)
	if err != nil {
		return nil, algo.Errorf("load features: %v", err)
	}

	// Represent each feature by a point (centroid for non-points),
	// projected to local meters for metric eps.
	var feats []*geojson.Feature
	var pts []orb.Point
	for _, f := range res.Features {
		if f == nil || f.Geometry == nil {
			continue
		}
		var pt orb.Point
		if gp, ok := f.Geometry.(orb.Point); ok {
			pt = gp
		} else {
			b := f.Geometry.Bound()
			pt = orb.Point{(b.Min[0] + b.Max[0]) / 2, (b.Min[1] + b.Max[1]) / 2}
		}
		feats = append(feats, f)
		pts = append(pts, pt)
	}
	if len(pts) == 0 {
		return nil, algo.Errorf("collection %q has no usable features", p.Collection)
	}

	labels := run(pts, p.EpsM, p.MinPoints)

	// Cluster summaries.
	type agg struct {
		count      int
		sumX, sumY float64
		bound      orb.Bound
	}
	sums := map[int]*agg{}
	noiseCount := 0
	for i, l := range labels {
		if l == noise {
			noiseCount++
			continue
		}
		a := sums[l]
		if a == nil {
			a = &agg{bound: orb.Bound{Min: pts[i], Max: pts[i]}}
			sums[l] = a
		}
		a.count++
		a.sumX += pts[i][0]
		a.sumY += pts[i][1]
		a.bound = a.bound.Union(orb.Bound{Min: pts[i], Max: pts[i]})
	}
	clusters := make([]map[string]interface{}, 0, len(sums))
	for id := 0; id < len(sums); id++ {
		a := sums[id]
		clusters = append(clusters, map[string]interface{}{
			"cluster":  id,
			"count":    a.count,
			"centroid": orb.Point{a.sumX / float64(a.count), a.sumY / float64(a.count)},
			"bbox":     [4]float64{a.bound.Min[0], a.bound.Min[1], a.bound.Max[0], a.bound.Max[1]},
		})
	}

	out := map[string]interface{}{
		"clusters":      clusters,
		"cluster_count": len(sums),
		"noise_count":   noiseCount,
		"feature_count": len(feats),
		"eps_m":         p.EpsM,
		"min_points":    p.MinPoints,
	}
	if p.IncludePoints == nil || *p.IncludePoints {
		fc := geojson.NewFeatureCollection()
		for i, f := range feats {
			lf := geojson.NewFeature(orb.Point(pts[i]))
			lf.ID = f.ID
			lf.Properties = geojson.Properties{"cluster": labels[i]}
			if name, ok := f.Properties["name"]; ok {
				lf.Properties["name"] = name
			}
			fc.Append(lf)
		}
		out["points"] = fc
	}
	return out, nil
}

// run executes grid-accelerated DBSCAN and returns a cluster label per
// point (>= 0) or noise (-1).
func run(pts []orb.Point, epsM float64, minPts int) []int {
	// Local equirectangular projection around the centroid keeps eps
	// metric without full geodesic math per pair.
	var cx, cy float64
	for _, p := range pts {
		cx += p[0]
		cy += p[1]
	}
	cx /= float64(len(pts))
	cy /= float64(len(pts))
	lonM := 111320 * math.Cos(cy*math.Pi/180)
	if lonM < 1 {
		lonM = 1
	}
	const latM = 111132.0
	xs := make([]float64, len(pts))
	ys := make([]float64, len(pts))
	for i, p := range pts {
		xs[i] = (p[0] - cx) * lonM
		ys[i] = (p[1] - cy) * latM
	}

	// Grid with cell size eps: all neighbours of a point live in the
	// 3×3 cells around it.
	type cellKey struct{ x, y int32 }
	grid := map[cellKey][]int32{}
	cellOf := func(i int) cellKey {
		return cellKey{int32(math.Floor(xs[i] / epsM)), int32(math.Floor(ys[i] / epsM))}
	}
	for i := range pts {
		k := cellOf(i)
		grid[k] = append(grid[k], int32(i))
	}
	eps2 := epsM * epsM
	neighbours := func(i int) []int32 {
		k := cellOf(i)
		var out []int32
		for dy := int32(-1); dy <= 1; dy++ {
			for dx := int32(-1); dx <= 1; dx++ {
				for _, j := range grid[cellKey{k.x + dx, k.y + dy}] {
					ddx := xs[j] - xs[i]
					ddy := ys[j] - ys[i]
					if ddx*ddx+ddy*ddy <= eps2 {
						out = append(out, j)
					}
				}
			}
		}
		return out
	}

	labels := make([]int, len(pts))
	for i := range labels {
		labels[i] = unclassified
	}
	cluster := 0
	for i := range pts {
		if labels[i] != unclassified {
			continue
		}
		nb := neighbours(i)
		if len(nb) < minPts {
			labels[i] = noise
			continue
		}
		labels[i] = cluster
		// Expand the cluster breadth-first over core points.
		queue := append([]int32(nil), nb...)
		for len(queue) > 0 {
			j := queue[0]
			queue = queue[1:]
			if labels[j] == noise {
				labels[j] = cluster // border point
			}
			if labels[j] != unclassified {
				continue
			}
			labels[j] = cluster
			nb2 := neighbours(int(j))
			if len(nb2) >= minPts {
				queue = append(queue, nb2...)
			}
		}
		cluster++
	}
	return labels
}
