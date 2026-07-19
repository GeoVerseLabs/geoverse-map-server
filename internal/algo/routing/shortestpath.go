// Package routing implements the network-based algorithms: shortest
// path (indoor/outdoor), isochrones and HMM map matching.
package routing

import (
	"context"
	"encoding/json"
	"math"

	"github.com/paulmach/orb"
	"github.com/paulmach/orb/geojson"

	"github.com/GeoVerseLabs/geoverse-map-server/internal/algo"
	"github.com/GeoVerseLabs/geoverse-map-server/internal/algo/network"
)

const defaultSnapRadiusM = 500

// ShortestPath is the shortest-path algorithm. It runs A* with an
// admissible great-circle heuristic over a configured network; indoor
// networks route across levels through connector edges.
type ShortestPath struct{}

// Describe implements algo.Algorithm.
func (ShortestPath) Describe() algo.Descriptor {
	return algo.Descriptor{
		Name:        "shortest_path",
		Title:       "最短路径（室内/室外）",
		Description: "Least-cost route between two points over a configured network, by travel time or distance. Uses A* with a great-circle heuristic. Multi-level (indoor) networks route through stairs/elevator connector edges; pass from_level/to_level for indoor floors.",
		InputSchema: map[string]interface{}{
			"type":     "object",
			"required": []string{"network", "from", "to"},
			"properties": map[string]interface{}{
				"network":       map[string]interface{}{"type": "string", "description": "Configured network name"},
				"from":          map[string]interface{}{"type": "array", "items": map[string]interface{}{"type": "number"}, "minItems": 2, "maxItems": 2, "description": "[lon, lat] origin"},
				"to":            map[string]interface{}{"type": "array", "items": map[string]interface{}{"type": "number"}, "minItems": 2, "maxItems": 2, "description": "[lon, lat] destination"},
				"cost":          map[string]interface{}{"type": "string", "enum": []string{"time", "distance"}, "description": "Optimize travel time (default) or distance"},
				"from_level":    map[string]interface{}{"type": "integer", "description": "Origin floor for indoor networks (default 0)"},
				"to_level":      map[string]interface{}{"type": "integer", "description": "Destination floor for indoor networks (default 0)"},
				"snap_radius_m": map[string]interface{}{"type": "number", "description": "Max distance to snap endpoints onto the network (default 500m)"},
			},
		},
	}
}

type shortestPathParams struct {
	Network     string     `json:"network"`
	From        *[]float64 `json:"from"`
	To          *[]float64 `json:"to"`
	Cost        string     `json:"cost"`
	FromLevel   int        `json:"from_level"`
	ToLevel     int        `json:"to_level"`
	SnapRadiusM float64    `json:"snap_radius_m"`
}

func parsePoint(v *[]float64, name string) (orb.Point, error) {
	if v == nil || len(*v) != 2 {
		return orb.Point{}, algo.Errorf("%s must be [lon, lat]", name)
	}
	p := orb.Point{(*v)[0], (*v)[1]}
	if p[0] < -180 || p[0] > 180 || p[1] < -90 || p[1] > 90 {
		return orb.Point{}, algo.Errorf("%s out of WGS84 range: %v", name, p)
	}
	return p, nil
}

func costFunc(name string) (network.CostFunc, bool, error) {
	switch name {
	case "", "time":
		return network.TimeCost, true, nil
	case "distance":
		return network.DistanceCost, false, nil
	}
	return nil, false, algo.Errorf("cost must be \"time\" or \"distance\", got %q", name)
}

// Run implements algo.Algorithm.
func (ShortestPath) Run(ctx context.Context, env *algo.Env, raw json.RawMessage) (interface{}, error) {
	var p shortestPathParams
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, algo.Errorf("invalid params: %v", err)
	}
	from, err := parsePoint(p.From, "from")
	if err != nil {
		return nil, err
	}
	to, err := parsePoint(p.To, "to")
	if err != nil {
		return nil, err
	}
	cost, timeBased, err := costFunc(p.Cost)
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
	src, srcD, ok := g.NearestNode(from, int16(p.FromLevel), snap)
	if !ok {
		return nil, algo.Errorf("no network node within %.0fm of origin on level %d", snap, p.FromLevel)
	}
	dst, dstD, ok := g.NearestNode(to, int16(p.ToLevel), snap)
	if !ok {
		return nil, algo.Errorf("no network node within %.0fm of destination on level %d", snap, p.ToLevel)
	}

	res := g.AStar(src, dst, cost, timeBased)
	path := res.PathTo(g, dst)
	if path == nil && src != dst {
		return nil, algo.Errorf("origin and destination are not connected in network %q", p.Network)
	}

	line := orb.LineString{g.Nodes[src].Pt}
	lengthM, timeS := 0.0, 0.0
	levels := map[int]bool{int(g.Nodes[src].Level): true}
	for _, ei := range path {
		e := &g.Edges[ei]
		line = append(line, g.Nodes[e.To].Pt)
		lengthM += e.LengthM
		timeS += e.TravelSeconds()
		levels[int(g.Nodes[e.To].Level)] = true
	}
	levelList := make([]int, 0, len(levels))
	for l := range levels {
		levelList = append(levelList, l)
	}

	f := geojson.NewFeature(line)
	f.Properties = geojson.Properties{
		"length_m":       math.Round(lengthM*10) / 10,
		"duration_s":     math.Round(timeS*10) / 10,
		"cost":           costName(p.Cost),
		"snap_from_m":    math.Round(srcD*10) / 10,
		"snap_to_m":      math.Round(dstD*10) / 10,
		"levels_visited": levelList,
	}
	fc := geojson.NewFeatureCollection()
	fc.Append(f)
	return fc, nil
}
