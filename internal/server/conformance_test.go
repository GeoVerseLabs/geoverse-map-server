package server

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// conformanceEvidence maps every conformance class URI this server may
// declare to a check that exercises the behaviour it promises against a
// live testServer. TestConformanceClassesHaveEvidence enforces the
// invariant in both directions: a class in declaredConformance (see
// conformance.go) with no entry here means the server is over-claiming; an
// entry here for a class no longer in declaredConformance means the
// evidence went dead without anyone noticing the declaration shrank.
var conformanceEvidence = map[string]func(t *testing.T, ts *httptest.Server){
	confCommonCore:   verifyCommonCore,
	confFeaturesCore: verifyFeaturesCore,
	confGeoJSON:      verifyGeoJSON,
	confTilesCore:    verifyTilesCore,
}

func TestConformanceClassesHaveEvidence(t *testing.T) {
	ts := testServer(t)
	doc := getJSON(t, ts.URL+"/conformance", http.StatusOK)
	classes, _ := doc["conformsTo"].([]interface{})
	if len(classes) == 0 {
		t.Fatal("conformance declaration is empty")
	}
	declared := make(map[string]bool, len(classes))
	for _, c := range classes {
		uri, _ := c.(string)
		declared[uri] = true
		verify, ok := conformanceEvidence[uri]
		if !ok {
			t.Errorf("conformance class %q is declared without a registered evidence check", uri)
			continue
		}
		t.Run(uri, func(t *testing.T) { verify(t, ts) })
	}
	for uri := range conformanceEvidence {
		if !declared[uri] {
			t.Errorf("evidence check registered for %q but the class is no longer declared", uri)
		}
	}
}

func verifyCommonCore(t *testing.T, ts *httptest.Server) {
	t.Helper()
	landing := getJSON(t, ts.URL+"/", http.StatusOK)
	rels := map[string]bool{}
	for _, l := range landing["links"].([]interface{}) {
		rels[l.(map[string]interface{})["rel"].(string)] = true
	}
	for _, want := range []string{"self", "service-desc", "conformance", "data"} {
		if !rels[want] {
			t.Errorf("landing missing required rel=%s link", want)
		}
	}
	if resp, err := http.Get(ts.URL + "/api?f=json"); err != nil {
		t.Fatal(err)
	} else {
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Errorf("/api status = %d", resp.StatusCode)
		}
	}
	getJSON(t, ts.URL+"/conformance", http.StatusOK)
}

func verifyFeaturesCore(t *testing.T, ts *httptest.Server) {
	t.Helper()
	cols := getJSON(t, ts.URL+"/collections", http.StatusOK)
	if len(cols["collections"].([]interface{})) == 0 {
		t.Fatal("no collections available to exercise Features Core against")
	}
	items := getJSON(t, ts.URL+"/collections/cities/items", http.StatusOK)
	if items["type"] != "FeatureCollection" {
		t.Errorf("items type = %v, want FeatureCollection", items["type"])
	}
	feat := getJSON(t, ts.URL+"/collections/cities/items/1", http.StatusOK)
	if feat["type"] != "Feature" {
		t.Errorf("single feature type = %v, want Feature", feat["type"])
	}
}

func verifyGeoJSON(t *testing.T, ts *httptest.Server) {
	t.Helper()
	resp, err := http.Get(ts.URL + "/collections/cities/items")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if ct := resp.Header.Get("Content-Type"); ct != geoJSONType {
		t.Errorf("content-type = %q, want %q", ct, geoJSONType)
	}
}

func verifyTilesCore(t *testing.T, ts *httptest.Server) {
	t.Helper()
	getJSON(t, ts.URL+"/tileMatrixSets", http.StatusOK)
	getJSON(t, ts.URL+"/collections/cities/tiles", http.StatusOK)
	resp, err := http.Get(ts.URL + "/collections/cities/tiles/" + webMercatorQuadID + "/6/24/52")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("standard tile route status = %d, want 200", resp.StatusCode)
	}
}
