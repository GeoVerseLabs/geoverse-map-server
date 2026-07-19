package server

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/GeoVerseLabs/geoverse-map-server/internal/config"
	"github.com/GeoVerseLabs/geoverse-map-server/internal/source/registry"
)

const testNetworkGeoJSON = `{
  "type": "FeatureCollection",
  "features": [
    {"type": "Feature", "properties": {"name": "ew"},
     "geometry": {"type": "LineString", "coordinates": [[0,0],[0.001,0],[0.002,0]]}},
    {"type": "Feature", "properties": {"name": "ns"},
     "geometry": {"type": "LineString", "coordinates": [[0.002,0],[0.002,0.001],[0.002,0.002]]}}
  ]
}`

func algoServer(t *testing.T) *httptest.Server {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "paths.geojson")
	if err := os.WriteFile(path, []byte(testNetworkGeoJSON), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg := config.Default()
	cfg.Sources = []config.Source{{Name: "paths", Type: "geojson", Path: path}}
	cfg.Networks = []config.Network{{Name: "walk", Source: "paths", DefaultSpeedKMH: 5}}
	reg, err := registry.Build(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(reg.Close)
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	ts := httptest.NewServer(New(cfg, reg, nil, log).Handler())
	t.Cleanup(ts.Close)
	return ts
}

func TestAlgorithmsList(t *testing.T) {
	ts := algoServer(t)
	doc := getJSON(t, ts.URL+"/algorithms", http.StatusOK)
	algos := doc["algorithms"].([]interface{})
	names := map[string]bool{}
	for _, a := range algos {
		names[a.(map[string]interface{})["name"].(string)] = true
	}
	for _, want := range []string{"shortest_path", "isochrone", "map_match", "dbscan"} {
		if !names[want] {
			t.Errorf("missing algorithm %q", want)
		}
	}
	nets := doc["networks"].([]interface{})
	if len(nets) != 1 || nets[0] != "walk" {
		t.Errorf("networks = %v", nets)
	}
	desc := getJSON(t, ts.URL+"/algorithms/shortest_path", http.StatusOK)
	if desc["name"] != "shortest_path" {
		t.Errorf("descriptor: %v", desc)
	}
	getJSON(t, ts.URL+"/algorithms/nope", http.StatusNotFound)
}

func postJSON(t *testing.T, url, body string, wantStatus int) map[string]interface{} {
	t.Helper()
	resp, err := http.Post(url, "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != wantStatus {
		t.Fatalf("POST %s: status %d, want %d; body: %s", url, resp.StatusCode, wantStatus, raw)
	}
	out := map[string]interface{}{}
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("bad json: %v", err)
	}
	return out
}

func TestRunShortestPathHTTP(t *testing.T) {
	ts := algoServer(t)
	out := postJSON(t, ts.URL+"/algorithms/shortest_path",
		`{"network":"walk","from":[0,0],"to":[0.002,0.002]}`, http.StatusOK)
	feats := out["features"].([]interface{})
	if len(feats) != 1 {
		t.Fatalf("features = %d", len(feats))
	}
	props := feats[0].(map[string]interface{})["properties"].(map[string]interface{})
	if props["length_m"].(float64) < 400 {
		t.Errorf("length_m = %v", props["length_m"])
	}

	// User errors map to 400, unknown algorithms to 404.
	postJSON(t, ts.URL+"/algorithms/shortest_path",
		`{"network":"walk","from":[50,50],"to":[0,0]}`, http.StatusBadRequest)
	postJSON(t, ts.URL+"/algorithms/nope", `{}`, http.StatusNotFound)
}

func TestRunDBSCANHTTP(t *testing.T) {
	ts := algoServer(t)
	out := postJSON(t, ts.URL+"/algorithms/dbscan",
		`{"collection":"paths","eps_m":500,"min_points":1,"include_points":false}`, http.StatusOK)
	if out["cluster_count"] == nil {
		t.Errorf("result: %v", out)
	}
}
