package network

import (
	"math"
	"testing"

	"github.com/paulmach/orb"
	"github.com/paulmach/orb/geojson"
)

func line(props geojson.Properties, coords ...[2]float64) *geojson.Feature {
	ls := make(orb.LineString, len(coords))
	for i, c := range coords {
		ls[i] = orb.Point{c[0], c[1]}
	}
	f := geojson.NewFeature(ls)
	f.Properties = props
	return f
}

// grid3 builds a 3x3 street grid: lons 0, 0.001, 0.002; lats same.
func grid3(t *testing.T) *Graph {
	t.Helper()
	var feats []*geojson.Feature
	vals := []float64{0, 0.001, 0.002}
	for _, y := range vals {
		feats = append(feats, line(geojson.Properties{"name": "h"},
			[2]float64{0, y}, [2]float64{0.001, y}, [2]float64{0.002, y}))
	}
	for _, x := range vals {
		feats = append(feats, line(geojson.Properties{"name": "v"},
			[2]float64{x, 0}, [2]float64{x, 0.001}, [2]float64{x, 0.002}))
	}
	g, err := Build(feats, BuildOptions{DefaultSpeedKMH: 5})
	if err != nil {
		t.Fatal(err)
	}
	return g
}

func TestBuildSharedNodes(t *testing.T) {
	g := grid3(t)
	if len(g.Nodes) != 9 {
		t.Errorf("nodes = %d, want 9 (shared intersections)", len(g.Nodes))
	}
	// 12 undirected segments -> 24 directed edges.
	if len(g.Edges) != 24 {
		t.Errorf("edges = %d, want 24", len(g.Edges))
	}
}

func TestAStarMatchesManhattan(t *testing.T) {
	g := grid3(t)
	src, _, ok := g.NearestNode(orb.Point{0, 0}, 0, 50)
	dst, _, ok2 := g.NearestNode(orb.Point{0.002, 0.002}, 0, 50)
	if !ok || !ok2 {
		t.Fatal("snap failed")
	}
	res := g.AStar(src, dst, DistanceCost, false)
	path := res.PathTo(g, dst)
	if len(path) != 4 {
		t.Fatalf("path edges = %d, want 4", len(path))
	}
	want := Haversine(orb.Point{0, 0}, orb.Point{0.002, 0}) +
		Haversine(orb.Point{0, 0}, orb.Point{0, 0.002})
	if math.Abs(res.Cost[dst]-want) > 1 {
		t.Errorf("cost = %.1f, want %.1f", res.Cost[dst], want)
	}
	// A* and Dijkstra must agree.
	dres := g.Dijkstra(src, DistanceCost, 0, dst)
	if math.Abs(res.Cost[dst]-dres.Cost[dst]) > 1e-9 {
		t.Errorf("A* %.3f != Dijkstra %.3f", res.Cost[dst], dres.Cost[dst])
	}
}

func TestOneway(t *testing.T) {
	feats := []*geojson.Feature{
		line(geojson.Properties{"oneway": "yes"}, [2]float64{0, 0}, [2]float64{0.001, 0}),
	}
	g, err := Build(feats, BuildOptions{DefaultSpeedKMH: 50, OnewayField: "oneway"})
	if err != nil {
		t.Fatal(err)
	}
	if len(g.Edges) != 1 {
		t.Fatalf("edges = %d, want 1 for oneway", len(g.Edges))
	}
	a, _, _ := g.NearestNode(orb.Point{0, 0}, 0, 10)
	b, _, _ := g.NearestNode(orb.Point{0.001, 0}, 0, 10)
	if res := g.Dijkstra(a, DistanceCost, 0, b); math.IsInf(res.Cost[b], 1) {
		t.Error("forward direction should be routable")
	}
	if res := g.Dijkstra(b, DistanceCost, 0, a); !math.IsInf(res.Cost[a], 1) {
		t.Error("reverse direction must be blocked")
	}
}

func TestLevelsAndConnector(t *testing.T) {
	feats := []*geojson.Feature{
		line(geojson.Properties{"level": 1.0}, [2]float64{0, 0}, [2]float64{0.001, 0}),
		line(geojson.Properties{"level": 2.0}, [2]float64{0, 0}, [2]float64{0.001, 0}),
		// Elevator at (0,0) between levels, fixed 30s.
		line(geojson.Properties{"level_from": 1.0, "level_to": 2.0, "duration_s": 30.0},
			[2]float64{0, 0}, [2]float64{0, 0}),
	}
	g, err := Build(feats, BuildOptions{DefaultSpeedKMH: 5})
	if err != nil {
		t.Fatal(err)
	}
	// Same coordinates on different levels are distinct nodes.
	n1, _, ok1 := g.NearestNode(orb.Point{0.001, 0}, 1, 10)
	n2, _, ok2 := g.NearestNode(orb.Point{0.001, 0}, 2, 10)
	if !ok1 || !ok2 || n1 == n2 {
		t.Fatalf("level nodes: %v/%v ok=%v/%v", n1, n2, ok1, ok2)
	}
	res := g.Dijkstra(n1, TimeCost, 0, n2)
	if math.IsInf(res.Cost[n2], 1) {
		t.Fatal("levels must connect through the elevator")
	}
	// Route: walk to elevator (~85m at 5km/h ≈ 61s) + 30s + walk back.
	walk := Haversine(orb.Point{0, 0}, orb.Point{0.001, 0}) / (5.0 / 3.6)
	want := 2*walk + 30
	if math.Abs(res.Cost[n2]-want) > 1 {
		t.Errorf("cross-level cost = %.1fs, want %.1fs", res.Cost[n2], want)
	}
}

func TestNearbyEdges(t *testing.T) {
	g := grid3(t)
	// Query point slightly north of the southern street's midpoint.
	cands := g.NearbyEdges(orb.Point{0.0005, 0.00002}, 0, 50, 3)
	if len(cands) == 0 {
		t.Fatal("no candidates")
	}
	c := cands[0]
	if c.DistM > 5 {
		t.Errorf("closest candidate %.1fm away", c.DistM)
	}
	if c.Frac <= 0 || c.Frac >= 1 {
		t.Errorf("projection frac = %v, want interior", c.Frac)
	}
}
