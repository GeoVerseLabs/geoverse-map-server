package server

import (
	"net/http"
	"strings"
	"testing"
)

func TestLandingLinksToServiceDesc(t *testing.T) {
	ts := testServer(t)
	doc := getJSON(t, ts.URL+"/", http.StatusOK)
	want := map[string]bool{"service-desc": false, "service-doc": false, "conformance": false, "data": false}
	for _, l := range doc["links"].([]interface{}) {
		rel, _ := l.(map[string]interface{})["rel"].(string)
		if _, tracked := want[rel]; tracked {
			want[rel] = true
		}
	}
	for rel, seen := range want {
		if !seen {
			t.Errorf("landing missing rel=%s link", rel)
		}
	}
}

func TestOpenAPIDocument(t *testing.T) {
	ts := testServer(t)

	// YAML is the default representation.
	resp, err := http.Get(ts.URL + "/api")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "text/vnd.yaml" {
		t.Errorf("content-type = %q, want text/vnd.yaml", ct)
	}

	// JSON on request, structurally decoded from the same source.
	doc := getJSON(t, ts.URL+"/api?f=json", http.StatusOK)
	if doc["openapi"] != "3.0.3" {
		t.Errorf("openapi version = %v", doc["openapi"])
	}
	paths, ok := doc["paths"].(map[string]interface{})
	if !ok || len(paths) == 0 {
		t.Fatal("openapi document has no paths")
	}
	if _, ok := paths["/collections/{collectionId}/tiles"]; !ok {
		t.Error("openapi document missing OGC API - Tiles tileset path")
	}

	// info.version reflects the running build, not a stale placeholder.
	info, ok := doc["info"].(map[string]interface{})
	if !ok || info["version"] != Version {
		t.Errorf("info.version = %v, want %v", info["version"], Version)
	}
}

// TestOpenAPIPathsAreWired asserts every path the OpenAPI document declares
// resolves to a real handler rather than the mux's default "page not
// found" — the document is allowed to be a subset of the server's actual
// routes (it excludes this server's non-OGC extension endpoints), so the
// check only runs path-declared -> route-exists, not the reverse.
func TestOpenAPIPathsAreWired(t *testing.T) {
	ts := testServer(t)
	paths, err := openapiPaths()
	if err != nil {
		t.Fatal(err)
	}
	if len(paths) == 0 {
		t.Fatal("no paths in openapi document")
	}
	substitute := strings.NewReplacer(
		"{collectionId}", "cities",
		"{featureId}", "1",
		"{tileMatrixSetId}", webMercatorQuadID,
		"{tileMatrix}", "6",
		"{tileRow}", "24",
		"{tileCol}", "52",
	)
	for _, p := range paths {
		reqPath := substitute.Replace(p)
		resp, err := http.Get(ts.URL + reqPath)
		if err != nil {
			t.Fatalf("GET %s: %v", reqPath, err)
		}
		resp.Body.Close()
		if resp.StatusCode == http.StatusNotFound && strings.HasPrefix(resp.Header.Get("Content-Type"), "text/plain") {
			t.Errorf("openapi path %s (-> %s) did not match any route (mux default 404)", p, reqPath)
		}
	}
}
