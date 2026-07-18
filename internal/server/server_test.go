package server

import (
	"compress/gzip"
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
	"github.com/paulmach/orb/encoding/mvt"
)

const testGeoJSON = `{
  "type": "FeatureCollection",
  "features": [
    {"type": "Feature", "id": 1, "properties": {"name": "beijing"},
     "geometry": {"type": "Point", "coordinates": [116.39, 39.91]}},
    {"type": "Feature", "id": 2, "properties": {"name": "shanghai"},
     "geometry": {"type": "Point", "coordinates": [121.47, 31.23]}}
  ]
}`

func testServer(t *testing.T) *httptest.Server {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "cities.geojson")
	if err := os.WriteFile(path, []byte(testGeoJSON), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg := config.Default()
	cfg.Sources = []config.Source{{Name: "cities", Type: "geojson", Path: path}}
	reg, err := registry.Build(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(reg.Close)
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	ts := httptest.NewServer(New(cfg, reg, log).Handler())
	t.Cleanup(ts.Close)
	return ts
}

func getJSON(t *testing.T, url string, wantStatus int) map[string]interface{} {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != wantStatus {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("GET %s: status %d, want %d; body: %s", url, resp.StatusCode, wantStatus, body)
	}
	var out map[string]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("GET %s: bad json: %v", url, err)
	}
	return out
}

func TestLandingAndConformance(t *testing.T) {
	ts := testServer(t)
	doc := getJSON(t, ts.URL+"/", http.StatusOK)
	if doc["title"] != "GeoVerse Map Server" {
		t.Errorf("landing title = %v", doc["title"])
	}
	conf := getJSON(t, ts.URL+"/conformance", http.StatusOK)
	if len(conf["conformsTo"].([]interface{})) == 0 {
		t.Error("conformance empty")
	}
}

func TestHealth(t *testing.T) {
	ts := testServer(t)
	doc := getJSON(t, ts.URL+"/health", http.StatusOK)
	if doc["status"] != "ok" {
		t.Errorf("health = %v", doc)
	}
}

func TestTileJSON(t *testing.T) {
	ts := testServer(t)
	doc := getJSON(t, ts.URL+"/tiles/cities.json", http.StatusOK)
	if doc["tilejson"] != "3.0.0" || doc["format"] != "pbf" {
		t.Errorf("tilejson doc: %v", doc)
	}
	tiles := doc["tiles"].([]interface{})[0].(string)
	if !strings.Contains(tiles, "/tiles/cities/{z}/{x}/{y}.pbf") {
		t.Errorf("tiles url = %s", tiles)
	}
	getJSON(t, ts.URL+"/tiles/nope.json", http.StatusNotFound)
}

func TestVectorTile(t *testing.T) {
	ts := testServer(t)
	req, _ := http.NewRequest("GET", ts.URL+"/tiles/cities/6/52/24.pbf", nil)
	req.Header.Set("Accept-Encoding", "identity") // force server-side gunzip
	resp, err := http.DefaultTransport.RoundTrip(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "application/vnd.mapbox-vector-tile" {
		t.Errorf("content-type = %s", ct)
	}
	body, _ := io.ReadAll(resp.Body)
	layers, err := mvt.Unmarshal(body)
	if err != nil {
		t.Fatalf("decode mvt: %v", err)
	}
	if len(layers) != 1 || len(layers[0].Features) != 1 {
		t.Fatalf("unexpected tile content: %d layers", len(layers))
	}

	// Same tile with gzip accepted must arrive gzip-encoded.
	req2, _ := http.NewRequest("GET", ts.URL+"/tiles/cities/6/52/24.pbf", nil)
	req2.Header.Set("Accept-Encoding", "gzip")
	resp2, err := http.DefaultTransport.RoundTrip(req2)
	if err != nil {
		t.Fatal(err)
	}
	defer resp2.Body.Close()
	if resp2.Header.Get("Content-Encoding") != "gzip" {
		t.Errorf("expected gzip passthrough")
	}
	if _, err := gzip.NewReader(resp2.Body); err != nil {
		t.Errorf("body not gzip: %v", err)
	}

	// ETag round trip.
	etag := resp.Header.Get("ETag")
	req3, _ := http.NewRequest("GET", ts.URL+"/tiles/cities/6/52/24.pbf", nil)
	req3.Header.Set("If-None-Match", etag)
	resp3, err := http.DefaultClient.Do(req3)
	if err != nil {
		t.Fatal(err)
	}
	resp3.Body.Close()
	if resp3.StatusCode != http.StatusNotModified {
		t.Errorf("etag: status = %d, want 304", resp3.StatusCode)
	}
}

func TestEmptyAndInvalidTiles(t *testing.T) {
	ts := testServer(t)
	resp, err := http.Get(ts.URL + "/tiles/cities/6/0/0.pbf") // mid-Pacific
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Errorf("empty tile: status = %d, want 204", resp.StatusCode)
	}
	getJSON(t, ts.URL+"/tiles/cities/2/9/1.pbf", http.StatusBadRequest)   // x out of range
	getJSON(t, ts.URL+"/tiles/cities/6/52/24.png", http.StatusBadRequest) // wrong ext
	getJSON(t, ts.URL+"/tiles/nope/0/0/0.pbf", http.StatusNotFound)
}

func TestCollectionsAndItems(t *testing.T) {
	ts := testServer(t)
	cols := getJSON(t, ts.URL+"/collections", http.StatusOK)
	if len(cols["collections"].([]interface{})) != 1 {
		t.Fatalf("collections: %v", cols)
	}
	col := getJSON(t, ts.URL+"/collections/cities", http.StatusOK)
	if col["id"] != "cities" {
		t.Errorf("collection id = %v", col["id"])
	}

	items := getJSON(t, ts.URL+"/collections/cities/items", http.StatusOK)
	if items["numberMatched"].(float64) != 2 {
		t.Errorf("numberMatched = %v", items["numberMatched"])
	}

	// bbox filter narrows to Beijing only.
	items = getJSON(t, ts.URL+"/collections/cities/items?bbox=115,39,118,41", http.StatusOK)
	if items["numberReturned"].(float64) != 1 {
		t.Errorf("bbox query returned %v", items["numberReturned"])
	}

	// Paging produces a next link.
	items = getJSON(t, ts.URL+"/collections/cities/items?limit=1", http.StatusOK)
	var hasNext bool
	for _, l := range items["links"].([]interface{}) {
		if l.(map[string]interface{})["rel"] == "next" {
			hasNext = true
		}
	}
	if !hasNext {
		t.Error("missing next link")
	}

	feat := getJSON(t, ts.URL+"/collections/cities/items/1", http.StatusOK)
	if feat["type"] != "Feature" {
		t.Errorf("single feature: %v", feat)
	}
	getJSON(t, ts.URL+"/collections/cities/items/999", http.StatusNotFound)
	getJSON(t, ts.URL+"/collections/cities/items?bbox=1,2,3", http.StatusBadRequest)
	getJSON(t, ts.URL+"/collections/cities/items?limit=-2", http.StatusBadRequest)
}

func TestWMTS(t *testing.T) {
	ts := testServer(t)
	resp, err := http.Get(ts.URL + "/wmts/1.0.0/WMTSCapabilities.xml")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	s := string(body)
	for _, want := range []string{
		"<ows:Identifier>cities</ows:Identifier>",
		"GoogleMapsCompatible",
		"{TileMatrix}/{TileRow}/{TileCol}",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("capabilities missing %q", want)
		}
	}

	// WMTS RESTful tile: row before col.
	tileResp, err := http.Get(ts.URL + "/wmts/1.0.0/cities/default/GoogleMapsCompatible/6/24/52.pbf")
	if err != nil {
		t.Fatal(err)
	}
	tileResp.Body.Close()
	if tileResp.StatusCode != http.StatusOK {
		t.Errorf("wmts tile status = %d", tileResp.StatusCode)
	}
}

func TestCatalog(t *testing.T) {
	ts := testServer(t)
	doc := getJSON(t, ts.URL+"/catalog", http.StatusOK)
	layers := doc["layers"].([]interface{})
	if len(layers) != 1 {
		t.Fatalf("catalog layers = %d", len(layers))
	}
	l := layers[0].(map[string]interface{})
	if l["name"] != "cities" || !strings.Contains(l["tiles"].(string), "{z}/{x}/{y}.pbf") {
		t.Errorf("catalog entry: %v", l)
	}
}
