// Package network builds routable graphs from LineString feature
// collections and runs shortest-path style searches over them.
//
// The graph model is deliberately simple but multi-level aware, so one
// code path serves both outdoor road networks and indoor walkways:
//
//   - every polyline is exploded into vertex-to-vertex segment edges,
//     making frontier interpolation (isochrones) and projection
//     (map matching) exact on straight segments;
//   - nodes are keyed by quantized coordinate + level, so features that
//     share endpoints join automatically;
//   - features carrying a level property live on that level (default 0);
//   - "connector" features (stairs, elevators, building entrances)
//     declare level_from/level_to and bridge levels, optionally with a
//     fixed traversal duration (duration_s) instead of length/speed.
package network

import (
	"fmt"
	"math"

	"github.com/paulmach/orb"
	"github.com/paulmach/orb/geojson"
)

const earthRadiusM = 6371008.8

// Haversine returns the great-circle distance between two WGS84 points
// in meters.
func Haversine(a, b orb.Point) float64 {
	lat1 := a[1] * math.Pi / 180
	lat2 := b[1] * math.Pi / 180
	dlat := lat2 - lat1
	dlon := (b[0] - a[0]) * math.Pi / 180
	h := math.Sin(dlat/2)*math.Sin(dlat/2) +
		math.Cos(lat1)*math.Cos(lat2)*math.Sin(dlon/2)*math.Sin(dlon/2)
	return 2 * earthRadiusM * math.Asin(math.Min(1, math.Sqrt(h)))
}

// NodeID indexes Graph.Nodes.
type NodeID = int32

// Node is a graph vertex at a coordinate on a level.
type Node struct {
	Pt    orb.Point
	Level int16
}

// Edge is a directed straight segment.
type Edge struct {
	From, To   NodeID
	LengthM    float64
	SpeedMS    float64
	DurationS  float64 // fixed traversal time (connectors); 0 = length/speed
	FeatureIdx int32   // index into Graph.Features
}

// TravelSeconds returns the time cost of the edge.
func (e *Edge) TravelSeconds() float64 {
	if e.DurationS > 0 {
		return e.DurationS
	}
	return e.LengthM / e.SpeedMS
}

// Graph is an immutable routable network, safe for concurrent use.
type Graph struct {
	Nodes []Node
	Edges []Edge
	Adj   [][]int32 // node -> outgoing edge indices
	// Features keeps a reference id/name per source feature for
	// attributing matched edges back to data.
	Features []FeatureRef
	// MaxSpeedMS bounds edge speeds; used for admissible A* heuristics.
	MaxSpeedMS float64

	nodeGrid *pointGrid
	edgeGrid *segGrid
}

// FeatureRef identifies a source feature an edge came from.
type FeatureRef struct {
	ID   string `json:"id,omitempty"`
	Name string `json:"name,omitempty"`
}

// BuildOptions control graph construction.
type BuildOptions struct {
	DefaultSpeedKMH float64 // fallback edge speed, default 5 (walking)
	SpeedField      string  // numeric property overriding speed (km/h)
	OnewayField     string  // property marking one-way lines ("yes"/1/true)
	LevelField      string  // numeric property assigning a level, default "level"
	// Connector detection: features with both properties present.
	LevelFromField string // default "level_from"
	LevelToField   string // default "level_to"
	DurationField  string // fixed traversal seconds, default "duration_s"
	NameField      string // property used for FeatureRef.Name, default "name"
}

func (o *BuildOptions) fill() {
	if o.DefaultSpeedKMH <= 0 {
		o.DefaultSpeedKMH = 5
	}
	if o.LevelField == "" {
		o.LevelField = "level"
	}
	if o.LevelFromField == "" {
		o.LevelFromField = "level_from"
	}
	if o.LevelToField == "" {
		o.LevelToField = "level_to"
	}
	if o.DurationField == "" {
		o.DurationField = "duration_s"
	}
	if o.NameField == "" {
		o.NameField = "name"
	}
}

type nodeKey struct {
	x, y  int64
	level int16
}

// quantize snaps coordinates to ~1e-6 deg (≈0.1m) so shared endpoints
// coalesce into one node despite float noise.
func quantize(p orb.Point) (int64, int64) {
	return int64(math.Round(p[0] * 1e6)), int64(math.Round(p[1] * 1e6))
}

type builder struct {
	opts  BuildOptions
	g     *Graph
	index map[nodeKey]NodeID
}

// Build constructs a Graph from LineString / MultiLineString features.
func Build(feats []*geojson.Feature, opts BuildOptions) (*Graph, error) {
	opts.fill()
	b := &builder{
		opts:  opts,
		g:     &Graph{},
		index: map[nodeKey]NodeID{},
	}
	for _, f := range feats {
		if f == nil || f.Geometry == nil {
			continue
		}
		var lines []orb.LineString
		switch geom := f.Geometry.(type) {
		case orb.LineString:
			lines = []orb.LineString{geom}
		case orb.MultiLineString:
			lines = geom
		default:
			continue // points/polygons are not network edges
		}
		ref := FeatureRef{Name: propString(f.Properties, opts.NameField)}
		if f.ID != nil {
			ref.ID = fmt.Sprint(f.ID)
		}
		fIdx := int32(len(b.g.Features))
		b.g.Features = append(b.g.Features, ref)

		speed := opts.DefaultSpeedKMH
		if opts.SpeedField != "" {
			if v, ok := propFloat(f.Properties, opts.SpeedField); ok && v > 0 {
				speed = v
			}
		}
		speedMS := speed / 3.6
		oneway := false
		if opts.OnewayField != "" {
			oneway = propBool(f.Properties, opts.OnewayField)
		}

		from, fromOK := propFloat(f.Properties, opts.LevelFromField)
		to, toOK := propFloat(f.Properties, opts.LevelToField)
		if fromOK && toOK {
			// Connector: single edge bridging two levels.
			dur, _ := propFloat(f.Properties, opts.DurationField)
			for _, ln := range lines {
				if len(ln) < 2 {
					continue
				}
				b.addConnector(ln, int16(from), int16(to), speedMS, dur, fIdx, oneway)
			}
			continue
		}

		level := int16(0)
		if v, ok := propFloat(f.Properties, opts.LevelField); ok {
			level = int16(v)
		}
		for _, ln := range lines {
			for i := 0; i+1 < len(ln); i++ {
				b.addSegment(ln[i], ln[i+1], level, speedMS, 0, fIdx, oneway)
			}
		}
	}
	if len(b.g.Edges) == 0 {
		return nil, fmt.Errorf("network: no LineString features to build a graph from")
	}
	b.g.buildIndexes()
	return b.g, nil
}

func (b *builder) node(p orb.Point, level int16) NodeID {
	x, y := quantize(p)
	key := nodeKey{x, y, level}
	if id, ok := b.index[key]; ok {
		return id
	}
	id := NodeID(len(b.g.Nodes))
	b.g.Nodes = append(b.g.Nodes, Node{Pt: p, Level: level})
	b.index[key] = id
	return id
}

func (b *builder) addEdge(from, to NodeID, lengthM, speedMS, durationS float64, fIdx int32) {
	b.g.Edges = append(b.g.Edges, Edge{
		From: from, To: to,
		LengthM: lengthM, SpeedMS: speedMS, DurationS: durationS,
		FeatureIdx: fIdx,
	})
	if speedMS > b.g.MaxSpeedMS {
		b.g.MaxSpeedMS = speedMS
	}
}

func (b *builder) addSegment(p1, p2 orb.Point, level int16, speedMS, durationS float64, fIdx int32, oneway bool) {
	n1, n2 := b.node(p1, level), b.node(p2, level)
	if n1 == n2 {
		return
	}
	length := Haversine(p1, p2)
	b.addEdge(n1, n2, length, speedMS, durationS, fIdx)
	if !oneway {
		b.addEdge(n2, n1, length, speedMS, durationS, fIdx)
	}
}

func (b *builder) addConnector(ln orb.LineString, from, to int16, speedMS, durationS float64, fIdx int32, oneway bool) {
	n1 := b.node(ln[0], from)
	n2 := b.node(ln[len(ln)-1], to)
	if n1 == n2 {
		return
	}
	length := 0.0
	for i := 0; i+1 < len(ln); i++ {
		length += Haversine(ln[i], ln[i+1])
	}
	// Zero-length vertical connectors (elevators) need a duration.
	if length == 0 && durationS == 0 {
		durationS = 30
	}
	b.addEdge(n1, n2, length, speedMS, durationS, fIdx)
	if !oneway {
		b.addEdge(n2, n1, length, speedMS, durationS, fIdx)
	}
}

func (g *Graph) buildIndexes() {
	g.Adj = make([][]int32, len(g.Nodes))
	for i := range g.Edges {
		e := &g.Edges[i]
		g.Adj[e.From] = append(g.Adj[e.From], int32(i))
	}
	g.nodeGrid = newPointGrid(g)
	g.edgeGrid = newSegGrid(g)
}

// NearestNode finds the closest node to p on the given level within
// maxDistM. Returns false if none is in range.
func (g *Graph) NearestNode(p orb.Point, level int16, maxDistM float64) (NodeID, float64, bool) {
	return g.nodeGrid.nearest(p, level, maxDistM)
}

// EdgeCandidate is an edge with the projection of a query point onto it.
type EdgeCandidate struct {
	EdgeIdx int32
	Point   orb.Point // projection of the query point onto the edge
	Frac    float64   // position along the edge, 0..1
	DistM   float64   // distance from query point to projection
}

// NearbyEdges returns up to k edges within radiusM of p on the given
// level, closest first.
func (g *Graph) NearbyEdges(p orb.Point, level int16, radiusM float64, k int) []EdgeCandidate {
	return g.edgeGrid.nearby(p, level, radiusM, k)
}

func propString(props geojson.Properties, key string) string {
	if v, ok := props[key]; ok {
		return fmt.Sprint(v)
	}
	return ""
}

func propFloat(props geojson.Properties, key string) (float64, bool) {
	switch v := props[key].(type) {
	case float64:
		return v, true
	case float32:
		return float64(v), true
	case int:
		return float64(v), true
	case int64:
		return float64(v), true
	case uint64:
		return float64(v), true
	}
	return 0, false
}

func propBool(props geojson.Properties, key string) bool {
	switch v := props[key].(type) {
	case bool:
		return v
	case string:
		return v == "yes" || v == "true" || v == "1" || v == "-1"
	case float64:
		return v != 0
	case int:
		return v != 0
	}
	return false
}
