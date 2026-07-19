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

// MapMatch snaps a noisy GPS trace onto the network using the HMM
// formulation of Newson & Krumm (2009): candidate edges within a search
// radius are HMM states, a Gaussian over the snap distance gives emission
// probabilities, and an exponential over |great-circle - route| distance
// gives transition probabilities; Viterbi decoding picks the most likely
// edge sequence. This is the standard improvement over per-point nearest
// edge snapping, which jumps between parallel roads on noisy traces.
type MapMatch struct{}

const (
	defaultSearchRadiusM = 50.0
	defaultSigmaM        = 15.0 // GPS noise (Gaussian sigma)
	defaultBetaM         = 30.0 // tolerated route-vs-line detour scale
	defaultMaxCandidates = 5
	maxTracePoints       = 10000
)

// Describe implements algo.Algorithm.
func (MapMatch) Describe() algo.Descriptor {
	return algo.Descriptor{
		Name:        "map_match",
		Title:       "路径匹配（HMM）",
		Description: "Match a noisy GPS trace onto the network (Newson-Krumm HMM + Viterbi). Returns the matched route geometry plus per-point snap details.",
		InputSchema: map[string]interface{}{
			"type":     "object",
			"required": []string{"network", "trace"},
			"properties": map[string]interface{}{
				"network": map[string]interface{}{"type": "string", "description": "Configured network name"},
				"trace": map[string]interface{}{"type": "array", "minItems": 2, "maxItems": maxTracePoints,
					"items":       map[string]interface{}{"type": "array", "items": map[string]interface{}{"type": "number"}, "minItems": 2, "maxItems": 2},
					"description": "GPS points as [[lon, lat], ...] in recorded order"},
				"search_radius_m": map[string]interface{}{"type": "number", "description": "Candidate edge search radius (default 50m)"},
				"sigma_m":         map[string]interface{}{"type": "number", "description": "GPS noise sigma for emission probability (default 15m)"},
				"beta_m":          map[string]interface{}{"type": "number", "description": "Transition tolerance: route/straight-line detour scale (default 30m)"},
				"level":           map[string]interface{}{"type": "integer", "description": "Floor for indoor networks (default 0)"},
			},
		},
	}
}

type mapMatchParams struct {
	Network       string      `json:"network"`
	Trace         [][]float64 `json:"trace"`
	SearchRadiusM float64     `json:"search_radius_m"`
	SigmaM        float64     `json:"sigma_m"`
	BetaM         float64     `json:"beta_m"`
	Level         int         `json:"level"`
}

type mmCandidate struct {
	network.EdgeCandidate
	emission float64
}

// Run implements algo.Algorithm.
func (MapMatch) Run(ctx context.Context, env *algo.Env, raw json.RawMessage) (interface{}, error) {
	var p mapMatchParams
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, algo.Errorf("invalid params: %v", err)
	}
	if len(p.Trace) < 2 || len(p.Trace) > maxTracePoints {
		return nil, algo.Errorf("trace must contain 2..%d points", maxTracePoints)
	}
	trace := make([]orb.Point, len(p.Trace))
	for i, c := range p.Trace {
		pt, err := parsePoint(&c, "trace point")
		if err != nil {
			return nil, err
		}
		trace[i] = pt
	}
	radius := p.SearchRadiusM
	if radius <= 0 {
		radius = defaultSearchRadiusM
	}
	sigma := p.SigmaM
	if sigma <= 0 {
		sigma = defaultSigmaM
	}
	beta := p.BetaM
	if beta <= 0 {
		beta = defaultBetaM
	}
	g, err := env.Networks.Graph(ctx, p.Network)
	if err != nil {
		return nil, algo.Errorf("%v", err)
	}
	level := int16(p.Level)

	// Candidate states per trace point; points with no nearby edge are
	// dropped from the chain (reported as unmatched).
	var states [][]mmCandidate
	var pointIdx []int // original trace index per chain step
	for i, pt := range trace {
		cands := g.NearbyEdges(pt, level, radius, defaultMaxCandidates)
		if len(cands) == 0 {
			continue
		}
		row := make([]mmCandidate, len(cands))
		for k, c := range cands {
			row[k] = mmCandidate{
				EdgeCandidate: c,
				emission:      -0.5 * (c.DistM / sigma) * (c.DistM / sigma),
			}
		}
		states = append(states, row)
		pointIdx = append(pointIdx, i)
	}
	if len(states) < 2 {
		return nil, algo.Errorf("fewer than 2 trace points have network edges within %.0fm", radius)
	}

	// Viterbi over the candidate lattice.
	n := len(states)
	score := make([][]float64, n)
	back := make([][]int, n)
	score[0] = make([]float64, len(states[0]))
	back[0] = make([]int, len(states[0]))
	for k := range states[0] {
		score[0][k] = states[0][k].emission
		back[0][k] = -1
	}
	for i := 1; i < n; i++ {
		prevRow, curRow := states[i-1], states[i]
		gcDist := network.Haversine(trace[pointIdx[i-1]], trace[pointIdx[i]])
		cutoff := gcDist*10 + 2000 // bound each route search
		score[i] = make([]float64, len(curRow))
		back[i] = make([]int, len(curRow))
		// One seeded Dijkstra per predecessor candidate reaches every
		// current candidate in a single search.
		for j := range prevRow {
			res := g.DijkstraSeeded(exitSeeds(g, &prevRow[j].EdgeCandidate), network.DistanceCost, cutoff, -1)
			for k := range curRow {
				routeD, ok := entryDistance(g, res, &curRow[k].EdgeCandidate, &prevRow[j].EdgeCandidate)
				if !ok {
					continue
				}
				trans := -math.Abs(routeD-gcDist) / beta
				cand := score[i-1][j] + trans + curRow[k].emission
				if isUnset(score[i], back[i], k) || cand > score[i][k] {
					score[i][k] = cand
					back[i][k] = j + 1 // 1-based; 0 = unset
				}
			}
		}
		// Points whose every transition failed break the chain; fall
		// back to emission-only so matching continues after gaps.
		for k := range curRow {
			if isUnset(score[i], back[i], k) {
				score[i][k] = score[i-1][argmax(score[i-1])] + curRow[k].emission - 1e3
				back[i][k] = argmax(score[i-1]) + 1
			}
		}
	}

	// Backtrack the best chain.
	chosen := make([]int, n)
	chosen[n-1] = argmax(score[n-1])
	for i := n - 1; i > 0; i-- {
		chosen[i-1] = back[i][chosen[i]] - 1
	}

	// Assemble output: snapped points + stitched route geometry.
	fc := geojson.NewFeatureCollection()
	var matchedLine orb.LineString
	totalLen := 0.0
	var points []map[string]interface{}
	for i := 0; i < n; i++ {
		c := &states[i][chosen[i]].EdgeCandidate
		ref := g.Features[g.Edges[c.EdgeIdx].FeatureIdx]
		points = append(points, map[string]interface{}{
			"index":      pointIdx[i],
			"input":      trace[pointIdx[i]],
			"snapped":    c.Point,
			"distance_m": math.Round(c.DistM*10) / 10,
			"feature":    ref,
		})
		if i == 0 {
			matchedLine = append(matchedLine, c.Point)
			continue
		}
		segLine, segLen := routeGeometry(g, &states[i-1][chosen[i-1]].EdgeCandidate, c)
		totalLen += segLen
		matchedLine = append(matchedLine, segLine...)
	}
	route := geojson.NewFeature(matchedLine)
	route.Properties = geojson.Properties{
		"role":             "matched_route",
		"length_m":         math.Round(totalLen*10) / 10,
		"points_total":     len(trace),
		"points_matched":   n,
		"points_unmatched": len(trace) - n,
	}
	fc.Append(route)
	fc.ExtraMembers = map[string]interface{}{"points": points}
	return fc, nil
}

func isUnset(score []float64, back []int, k int) bool { return back[k] == 0 }

func argmax(v []float64) int {
	best := 0
	for i := 1; i < len(v); i++ {
		if v[i] > v[best] {
			best = i
		}
	}
	return best
}

// exitSeeds seeds a search at both endpoints of the candidate's edge with
// the partial distances from the projection point.
func exitSeeds(g *network.Graph, c *network.EdgeCandidate) []network.Seed {
	e := &g.Edges[c.EdgeIdx]
	return []network.Seed{
		{Node: e.From, Cost: c.Frac * e.LengthM},
		{Node: e.To, Cost: (1 - c.Frac) * e.LengthM},
	}
}

// entryDistance reads the route distance from a seeded search to the
// projection point of candidate c. Same-edge transitions short-circuit to
// the along-edge distance.
func entryDistance(g *network.Graph, res *network.SearchResult, c, prev *network.EdgeCandidate) (float64, bool) {
	e := &g.Edges[c.EdgeIdx]
	if c.EdgeIdx == prev.EdgeIdx {
		return math.Abs(c.Frac-prev.Frac) * e.LengthM, true
	}
	dFrom := res.Cost[e.From] + c.Frac*e.LengthM
	dTo := res.Cost[e.To] + (1-c.Frac)*e.LengthM
	d := math.Min(dFrom, dTo)
	if math.IsInf(d, 1) {
		return 0, false
	}
	return d, true
}

// routeGeometry stitches the on-network geometry between two consecutive
// matched candidates (projection point → nodes → projection point).
func routeGeometry(g *network.Graph, a, b *network.EdgeCandidate) (orb.LineString, float64) {
	if a.EdgeIdx == b.EdgeIdx {
		return orb.LineString{b.Point}, network.Haversine(a.Point, b.Point)
	}
	res := g.DijkstraSeeded(exitSeeds(g, a), network.DistanceCost, 0, -1)
	eb := &g.Edges[b.EdgeIdx]
	entryNode := eb.From
	entryOffset := b.Frac * eb.LengthM
	if res.Cost[eb.To]+(1-b.Frac)*eb.LengthM < res.Cost[eb.From]+entryOffset {
		entryNode = eb.To
		entryOffset = (1 - b.Frac) * eb.LengthM
	}
	if math.IsInf(res.Cost[entryNode], 1) {
		// Disconnected: draw a straight gap segment.
		return orb.LineString{b.Point}, network.Haversine(a.Point, b.Point)
	}
	var line orb.LineString
	path := res.PathTo(g, entryNode)
	if len(path) > 0 {
		line = append(line, g.Nodes[g.Edges[path[0]].From].Pt)
		for _, ei := range path {
			line = append(line, g.Nodes[g.Edges[ei].To].Pt)
		}
	} else {
		line = append(line, g.Nodes[entryNode].Pt)
	}
	line = append(line, b.Point)
	return line, res.Cost[entryNode] + entryOffset
}
