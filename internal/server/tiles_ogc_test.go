package server

import (
	"bytes"
	"io"
	"net/http"
	"testing"
)

func TestTileMatrixSets(t *testing.T) {
	ts := testServer(t)

	list := getJSON(t, ts.URL+"/tileMatrixSets", http.StatusOK)
	sets := list["tileMatrixSets"].([]interface{})
	if len(sets) != 1 || sets[0].(map[string]interface{})["id"] != webMercatorQuadID {
		t.Fatalf("tileMatrixSets = %v", sets)
	}

	def := getJSON(t, ts.URL+"/tileMatrixSets/"+webMercatorQuadID, http.StatusOK)
	matrices := def["tileMatrices"].([]interface{})
	if len(matrices) != webMercatorQuadMaxZoom+1 {
		t.Fatalf("tileMatrices count = %d, want %d", len(matrices), webMercatorQuadMaxZoom+1)
	}
	z0 := matrices[0].(map[string]interface{})
	if z0["matrixWidth"].(float64) != 1 || z0["matrixHeight"].(float64) != 1 {
		t.Errorf("z0 matrix dims = %v/%v", z0["matrixWidth"], z0["matrixHeight"])
	}
	z6 := matrices[6].(map[string]interface{})
	if z6["matrixWidth"].(float64) != 64 {
		t.Errorf("z6 matrixWidth = %v, want 64", z6["matrixWidth"])
	}

	getJSON(t, ts.URL+"/tileMatrixSets/NotARealTMS", http.StatusNotFound)
}

func TestCollectionTileset(t *testing.T) {
	ts := testServer(t)
	doc := getJSON(t, ts.URL+"/collections/cities/tiles", http.StatusOK)
	if doc["tileMatrixSetURI"] == nil {
		t.Fatal("tileset missing tileMatrixSetURI")
	}
	limits := doc["tileMatrixSetLimits"].([]interface{})
	if len(limits) == 0 {
		t.Fatal("tileset missing tileMatrixSetLimits")
	}
	var hasTileLink bool
	for _, l := range doc["links"].([]interface{}) {
		m := l.(map[string]interface{})
		if m["rel"] == "item" {
			hasTileLink = true
			href := m["href"].(string)
			for _, want := range []string{"{tileMatrix}", "{tileRow}", "{tileCol}"} {
				if !bytes.Contains([]byte(href), []byte(want)) {
					t.Errorf("tile template %q missing %q", href, want)
				}
			}
		}
	}
	if !hasTileLink {
		t.Error("tileset missing rel=item tile template link")
	}

	getJSON(t, ts.URL+"/collections/does-not-exist/tiles", http.StatusNotFound)

	// The collection document should now advertise the tileset back-link.
	col := getJSON(t, ts.URL+"/collections/cities", http.StatusOK)
	var hasTilesRel bool
	for _, l := range col["links"].([]interface{}) {
		if l.(map[string]interface{})["rel"] == "tiles" {
			hasTilesRel = true
		}
	}
	if !hasTilesRel {
		t.Error("collection document missing rel=tiles link")
	}
}

// TestStandardTileMatchesXYZBytes proves the OGC API - Tiles route is a
// second door onto the same tile pipeline, not a parallel implementation:
// same z/x/y must return byte-identical payloads on both routes.
func TestStandardTileMatchesXYZBytes(t *testing.T) {
	ts := testServer(t)

	fetch := func(url string) (int, []byte, string) {
		req, _ := http.NewRequest("GET", url, nil)
		req.Header.Set("Accept-Encoding", "identity")
		resp, err := http.DefaultTransport.RoundTrip(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, body, resp.Header.Get("Content-Type")
	}

	xyzStatus, xyzBody, xyzType := fetch(ts.URL + "/tiles/cities/6/52/24.pbf")
	// tileMatrix=z(6), tileRow=y(24), tileCol=x(52).
	ogcStatus, ogcBody, ogcType := fetch(ts.URL + "/collections/cities/tiles/" + webMercatorQuadID + "/6/24/52")

	if xyzStatus != http.StatusOK || ogcStatus != http.StatusOK {
		t.Fatalf("status: xyz=%d ogc=%d", xyzStatus, ogcStatus)
	}
	if xyzType != ogcType {
		t.Errorf("content-type: xyz=%q ogc=%q", xyzType, ogcType)
	}
	if !bytes.Equal(xyzBody, ogcBody) {
		t.Errorf("tile bytes differ between XYZ and OGC Tiles routes (%d vs %d bytes)", len(xyzBody), len(ogcBody))
	}

	// Out-of-zoom-range behaves the same way on both routes (204, not an
	// error): z=23 is a syntactically valid tile (tilemath.MaxZoom=24) but
	// above this in-memory source's default MaxZoom=22.
	_, _, _ = fetch(ts.URL + "/tiles/cities/23/0/0.pbf")
	status, _, _ := fetch(ts.URL + "/collections/cities/tiles/" + webMercatorQuadID + "/23/0/0")
	if status != http.StatusNoContent {
		t.Errorf("out-of-range OGC tile status = %d, want 204", status)
	}

	getJSON(t, ts.URL+"/collections/cities/tiles/NotATMS/6/24/52", http.StatusNotFound)
	getJSON(t, ts.URL+"/collections/nope/tiles/"+webMercatorQuadID+"/6/24/52", http.StatusNotFound)
}
