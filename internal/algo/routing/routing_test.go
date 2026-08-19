package routing

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
	"testing"

	"github.com/paulmach/orb"
	"github.com/paulmach/orb/geojson"
	"github.com/paulmach/orb/planar"

	"github.com/GeoVerseLabs/geoverse-map-server/internal/algo"
	"github.com/GeoVerseLabs/geoverse-map-server/internal/algo/network"
	"github.com/GeoVerseLabs/geoverse-map-server/internal/source"
	"github.com/GeoVerseLabs/geoverse-map-server/internal/source/memengine"
)

// testEnv builds an Env over a 3x3 street grid (lons/lats 0, 0.001,
// 0.002) with a two-level indoor annex at x=0.003 joined by a ramp and
// an elevator.
func testEnv(t *testing.T) *algo.Env {
	t.Helper()
	fc := geojson.NewFeatureCollection()
	add := func(props geojson.Properties, coords ...orb.Point) {
		f := geojson.NewFeature(orb.LineString(coords))
		f.Properties = props
		fc.Append(f)
	}
	vals := []float64{0, 0.001, 0.002}
	for _, y := range vals {
		add(geojson.Properties{"name": "h"}, orb.Point{0, y}, orb.Point{0.001, y}, orb.Point{0.002, y})
	}
	for _, x := range vals {
		add(geojson.Properties{"name": "v"}, orb.Point{x, 0}, orb.Point{x, 0.001}, orb.Point{x, 0.002})
	}
	// Indoor annex: ramp L0->L1, corridors on L1/L2, elevator L1->L2.
	add(geojson.Properties{"level_from": 0.0, "level_to": 1.0, "duration_s": 20.0},
		orb.Point{0.002, 0}, orb.Point{0.003, 0})
	add(geojson.Properties{"level": 1.0}, orb.Point{0.003, 0}, orb.Point{0.003, 0.001})
	add(geojson.Properties{"level": 2.0}, orb.Point{0.003, 0}, orb.Point{0.003, 0.001})
	add(geojson.Properties{"level_from": 1.0, "level_to": 2.0, "duration_s": 25.0},
		orb.Point{0.003, 0}, orb.Point{0.003, 0})

	eng, err := memengine.New(memengine.Options{Name: "paths"}, fc.Features)
	if err != nil {
		t.Fatal(err)
	}
	lookup := func(name string) (source.FeatureSource, bool) {
		if name == "paths" {
			return eng, true
		}
		return nil, false
	}
	return &algo.Env{
		Features: lookup,
		Networks: network.NewManager([]network.NetworkConfig{
			{Name: "walk", Source: "paths", DefaultSpeedKMH: 5},
		}, lookup),
	}
}

func runAlgo(t *testing.T, a algo.Algorithm, env *algo.Env, params string) interface{} {
	t.Helper()
	out, err := a.Run(context.Background(), env, json.RawMessage(params))
	if err != nil {
		t.Fatalf("%s: %v", a.Describe().Name, err)
	}
	return out
}

func TestShortestPathOutdoor(t *testing.T) {
	env := testEnv(t)
	out := runAlgo(t, ShortestPath{}, env,
		`{"network":"walk","from":[0,0],"to":[0.002,0.002],"cost":"distance"}`)
	fc := out.(*geojson.FeatureCollection)
	if len(fc.Features) != 1 {
		t.Fatalf("features = %d", len(fc.Features))
	}
	f := fc.Features[0]
	length := f.Properties["length_m"].(float64)
	// Manhattan distance on the grid ≈ 443m (2×0.002° ≈ 2×222m/111m per 0.001°).
	if length < 400 || length > 480 {
		t.Errorf("length_m = %v, want ≈443", length)
	}
	if n := len(f.Geometry.(orb.LineString)); n != 5 {
		t.Errorf("route vertices = %d, want 5", n)
	}
}

func TestShortestPathIndoor(t *testing.T) {
	env := testEnv(t)
	// From outdoor grid origin to the level-2 corridor end.
	out := runAlgo(t, ShortestPath{}, env,
		`{"network":"walk","from":[0,0],"to":[0.003,0.001],"to_level":2}`)
	fc := out.(*geojson.FeatureCollection)
	levels := fc.Features[0].Properties["levels_visited"].([]int)
	seen := map[int]bool{}
	for _, l := range levels {
		seen[l] = true
	}
	if !seen[0] || !seen[1] || !seen[2] {
		t.Errorf("route should traverse levels 0,1,2; got %v", levels)
	}
	dur := fc.Features[0].Properties["duration_s"].(float64)
	if dur < 45 { // must include ramp 20s + elevator 25s
		t.Errorf("duration_s = %v, want >= 45", dur)
	}
}

func TestShortestPathErrors(t *testing.T) {
	env := testEnv(t)
	cases := []string{
		`{"network":"nope","from":[0,0],"to":[0.001,0]}`,
		`{"network":"walk","from":[10,10],"to":[0.001,0]}`, // out of snap range
		`{"network":"walk","from":[0,0],"to":[0.001,0],"cost":"beauty"}`,
	}
	for _, c := range cases {
		if _, err := (ShortestPath{}).Run(context.Background(), env, json.RawMessage(c)); err == nil {
			t.Errorf("params %s: expected error", c)
		}
	}
}

func TestIsochroneContainsNearExcludesFar(t *testing.T) {
	env := testEnv(t)
	// 60s at 5km/h ≈ 83m reach from the grid center.
	out := runAlgo(t, Isochrone{}, env,
		`{"network":"walk","origin":[0.001,0.001],"cutoffs":[60]}`)
	fc := out.(*geojson.FeatureCollection)
	if len(fc.Features) != 1 {
		t.Fatalf("features = %d", len(fc.Features))
	}
	mp := fc.Features[0].Geometry.(orb.MultiPolygon)
	if len(mp) == 0 {
		t.Fatal("empty multipolygon")
	}
	if !planar.MultiPolygonContains(mp, orb.Point{0.001, 0.001}) {
		t.Error("isochrone must contain its origin")
	}
	if planar.MultiPolygonContains(mp, orb.Point{0.002, 0.002}) {
		t.Error("far grid corner (~222m) must be outside a 60s walk")
	}
}

func TestIsochroneNested(t *testing.T) {
	env := testEnv(t)
	out := runAlgo(t, Isochrone{}, env,
		`{"network":"walk","origin":[0.001,0.001],"cutoffs":[60,300]}`)
	fc := out.(*geojson.FeatureCollection)
	if len(fc.Features) != 2 {
		t.Fatalf("features = %d, want 2", len(fc.Features))
	}
	// Largest cutoff first.
	if fc.Features[0].Properties["cutoff"].(float64) != 300 {
		t.Errorf("first cutoff = %v, want 300", fc.Features[0].Properties["cutoff"])
	}
}

// TestIsochroneRejectsUnboundedRaster covers the resource-exhaustion path
// cell_size_m used to open: it is caller-supplied and divides into the
// reachable extent, so a small value asked for a grid of billions of
// cells (measured: cell_size_m=0.01 over an 800m walk => 79986 x 80020,
// ~12.8 GB once dilate() doubles it) and took the process down with it.
// The request must now be refused as a client error, not attempted.
func TestIsochroneRejectsUnboundedRaster(t *testing.T) {
	env := testEnv(t)
	for _, params := range []string{
		`{"network":"walk","origin":[0.001,0.001],"cutoffs":[300],"cell_size_m":0.01}`,
		`{"network":"walk","origin":[0.001,0.001],"cutoffs":[300],"cell_size_m":0.0001}`,
	} {
		_, err := (Isochrone{}).Run(context.Background(), env, json.RawMessage(params))
		if err == nil {
			t.Fatalf("params %s: expected a refusal, got success", params)
		}
		var ue *algo.UserError
		if !errors.As(err, &ue) {
			t.Errorf("params %s: err = %#v, want algo.UserError (400, not 500)", params, err)
		}
	}

	// A sane explicit cell size still works, so the guard is a cap and not
	// a removal of the parameter.
	out := runAlgo(t, Isochrone{}, env,
		`{"network":"walk","origin":[0.001,0.001],"cutoffs":[300],"cell_size_m":5}`)
	if len(out.(*geojson.FeatureCollection).Features) == 0 {
		t.Error("cell_size_m=5 should still produce contours")
	}
}

// TestContourGridSizing pins the arithmetic directly: dimensions scale as
// 1/cell, the cap rejects before allocating, and the error names a cell
// size that would actually be accepted.
func TestContourGridSizing(t *testing.T) {
	minP := orb.Point{121.5, 31.20}
	maxP := orb.Point{121.5084, 31.2072} // ~800m box

	auto, err := contourGrid(minP, maxP, 0)
	if err != nil {
		t.Fatalf("automatic sizing must succeed: %v", err)
	}
	if auto.nx*auto.ny > maxContourCells {
		t.Errorf("automatic grid %dx%d exceeds the cap", auto.nx, auto.ny)
	}

	if _, err := contourGrid(minP, maxP, 0.01); err == nil {
		t.Fatal("cell_size_m=0.01 must be refused")
	}
	for _, bad := range []float64{math.NaN(), math.Inf(1)} {
		if _, err := contourGrid(minP, maxP, bad); err == nil {
			t.Errorf("cell_size_m=%v must be refused", bad)
		}
	}

	// The remedy the error suggests has to actually pass the guard,
	// otherwise the message sends the caller in circles.
	_, err = contourGrid(minP, maxP, 0.01)
	if err == nil {
		t.Fatal("expected refusal")
	}
	var suggested float64
	if _, scanErr := fmt.Sscanf(err.Error()[strings.LastIndex(err.Error(), ">= ")+3:], "%g", &suggested); scanErr != nil {
		t.Fatalf("could not read the suggested cell size out of %q: %v", err.Error(), scanErr)
	}
	if _, err := contourGrid(minP, maxP, suggested); err != nil {
		t.Errorf("suggested cell_size_m %g was itself refused: %v", suggested, err)
	}
}

func TestMapMatchFollowsGrid(t *testing.T) {
	env := testEnv(t)
	// Noisy trace along the L route: east along y=0 then north along
	// x=0.002. Offsets ≈ 5-8m off the streets.
	trace := `[
		[0.00005, 0.00006], [0.0005, -0.00005], [0.001, 0.00007],
		[0.0015, -0.00006], [0.002, 0.00005], [0.00206, 0.0005],
		[0.00195, 0.001], [0.00205, 0.0015], [0.002, 0.00195]
	]`
	out := runAlgo(t, MapMatch{}, env,
		`{"network":"walk","trace":`+trace+`,"search_radius_m":40}`)
	fc := out.(*geojson.FeatureCollection)
	route := fc.Features[0]
	if route.Properties["points_matched"].(int) != 9 {
		t.Errorf("points_matched = %v, want 9", route.Properties["points_matched"])
	}
	// Matched length ≈ 222m + 222m = 444m; generous bounds.
	length := route.Properties["length_m"].(float64)
	if length < 380 || length > 500 {
		t.Errorf("length_m = %v, want ≈444", length)
	}
	// Every snapped point must be very close to a street.
	pts := fc.ExtraMembers["points"].([]map[string]interface{})
	for _, p := range pts {
		if d := p["distance_m"].(float64); d > 10 {
			t.Errorf("snap distance %vm too large", d)
		}
	}
}

func TestMapMatchRejectsFarTrace(t *testing.T) {
	env := testEnv(t)
	_, err := (MapMatch{}).Run(context.Background(), env,
		json.RawMessage(`{"network":"walk","trace":[[10,10],[10.001,10]]}`))
	if err == nil {
		t.Fatal("expected error for trace far from network")
	}
}
