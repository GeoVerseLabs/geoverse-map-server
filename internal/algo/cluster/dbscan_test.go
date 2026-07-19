package cluster

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/paulmach/orb"
	"github.com/paulmach/orb/geojson"

	"github.com/GeoVerseLabs/geoverse-map-server/internal/algo"
	"github.com/GeoVerseLabs/geoverse-map-server/internal/source"
	"github.com/GeoVerseLabs/geoverse-map-server/internal/source/memengine"
)

func testEnv(t *testing.T) *algo.Env {
	t.Helper()
	fc := geojson.NewFeatureCollection()
	add := func(x, y float64, name string) {
		f := geojson.NewFeature(orb.Point{x, y})
		f.Properties = geojson.Properties{"name": name}
		fc.Append(f)
	}
	// Cluster A: 4 points within ~20m around (0, 0).
	add(0, 0, "a1")
	add(0.0001, 0, "a2")
	add(0, 0.0001, "a3")
	add(0.0001, 0.0001, "a4")
	// Cluster B: 3 points around (0.01, 0.01) (~1.1km away).
	add(0.01, 0.01, "b1")
	add(0.0101, 0.01, "b2")
	add(0.01, 0.0101, "b3")
	// Noise: isolated point.
	add(0.005, -0.005, "noise")

	eng, err := memengine.New(memengine.Options{Name: "pts"}, fc.Features)
	if err != nil {
		t.Fatal(err)
	}
	return &algo.Env{Features: func(name string) (source.FeatureSource, bool) {
		if name == "pts" {
			return eng, true
		}
		return nil, false
	}}
}

func TestDBSCANFindsTwoClusters(t *testing.T) {
	env := testEnv(t)
	out, err := (DBSCAN{}).Run(context.Background(), env,
		json.RawMessage(`{"collection":"pts","eps_m":50,"min_points":3}`))
	if err != nil {
		t.Fatal(err)
	}
	res := out.(map[string]interface{})
	if res["cluster_count"].(int) != 2 {
		t.Errorf("cluster_count = %v, want 2", res["cluster_count"])
	}
	if res["noise_count"].(int) != 1 {
		t.Errorf("noise_count = %v, want 1", res["noise_count"])
	}
	clusters := res["clusters"].([]map[string]interface{})
	sizes := map[int]int{}
	for _, c := range clusters {
		sizes[c["cluster"].(int)] = c["count"].(int)
	}
	if sizes[0]+sizes[1] != 7 {
		t.Errorf("cluster sizes = %v, want total 7", sizes)
	}
	// Labeled points included by default.
	pts := res["points"].(*geojson.FeatureCollection)
	if len(pts.Features) != 8 {
		t.Errorf("labeled points = %d, want 8", len(pts.Features))
	}
}

func TestDBSCANParamsValidation(t *testing.T) {
	env := testEnv(t)
	cases := []string{
		`{"collection":"pts","eps_m":0,"min_points":3}`,
		`{"collection":"pts","eps_m":50,"min_points":0}`,
		`{"collection":"nope","eps_m":50,"min_points":3}`,
	}
	for _, c := range cases {
		if _, err := (DBSCAN{}).Run(context.Background(), env, json.RawMessage(c)); err == nil {
			t.Errorf("params %s: expected error", c)
		}
	}
}

func TestDBSCANSummariesOnly(t *testing.T) {
	env := testEnv(t)
	out, err := (DBSCAN{}).Run(context.Background(), env,
		json.RawMessage(`{"collection":"pts","eps_m":50,"min_points":3,"include_points":false}`))
	if err != nil {
		t.Fatal(err)
	}
	if _, has := out.(map[string]interface{})["points"]; has {
		t.Error("points must be omitted when include_points=false")
	}
}
