package memengine

import (
	"bytes"
	"compress/gzip"
	"context"
	"io"
	"testing"

	"github.com/paulmach/orb"
	"github.com/paulmach/orb/encoding/mvt"
	"github.com/paulmach/orb/geojson"

	"github.com/GeoVerseLabs/geoverse-map-server/internal/source"
)

func testEngine(t *testing.T) *Engine {
	t.Helper()
	fc := geojson.NewFeatureCollection()
	pt := geojson.NewFeature(orb.Point{116.39, 39.91}) // Beijing
	pt.Properties["name"] = "beijing"
	pt.Properties["pop"] = 21893095.0
	fc.Append(pt)
	ln := geojson.NewFeature(orb.LineString{{116.0, 39.5}, {117.0, 40.5}})
	ln.Properties["name"] = "line"
	fc.Append(ln)
	far := geojson.NewFeature(orb.Point{-74.0, 40.7}) // New York
	far.Properties["name"] = "nyc"
	fc.Append(far)

	e, err := New(Options{Name: "test", MaxZoom: 14, Simplify: true}, fc.Features)
	if err != nil {
		t.Fatal(err)
	}
	return e
}

func TestTileEncodesNearbyFeatures(t *testing.T) {
	e := testEngine(t)
	// Zoom 6 tile containing Beijing: x = (116.39+180)/360 * 64 ≈ 52.
	data, err := e.Tile(context.Background(), 6, 52, 24)
	if err != nil {
		t.Fatal(err)
	}
	zr, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("tile is not gzipped: %v", err)
	}
	raw, err := io.ReadAll(zr)
	if err != nil {
		t.Fatal(err)
	}
	layers, err := mvt.Unmarshal(raw)
	if err != nil {
		t.Fatalf("decode mvt: %v", err)
	}
	if len(layers) != 1 || layers[0].Name != "test" {
		t.Fatalf("unexpected layers: %+v", layers)
	}
	found := false
	for _, f := range layers[0].Features {
		if f.Properties["name"] == "beijing" {
			found = true
			if f.Properties["pop"] != 21893095.0 {
				t.Errorf("pop property lost: %v", f.Properties["pop"])
			}
		}
		if f.Properties["name"] == "nyc" {
			t.Error("nyc must not appear in a Beijing tile")
		}
	}
	if !found {
		t.Error("beijing feature missing from tile")
	}
}

func TestTileEmptyRegion(t *testing.T) {
	e := testEngine(t)
	// South Atlantic: nothing there.
	if _, err := e.Tile(context.Background(), 6, 28, 36); err != source.ErrTileNotFound {
		t.Fatalf("want ErrTileNotFound, got %v", err)
	}
}

func TestFeaturesBBoxAndPaging(t *testing.T) {
	e := testEngine(t)
	bbox := [4]float64{115, 39, 118, 41}
	res, err := e.Features(context.Background(), source.FeatureQuery{BBox: &bbox, Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if res.NumberMatched != 2 || len(res.Features) != 2 {
		t.Fatalf("bbox query: matched=%d returned=%d, want 2/2", res.NumberMatched, len(res.Features))
	}

	res, err = e.Features(context.Background(), source.FeatureQuery{Limit: 1, Offset: 2})
	if err != nil {
		t.Fatal(err)
	}
	if res.NumberMatched != 3 || len(res.Features) != 1 {
		t.Fatalf("paged query: matched=%d returned=%d, want 3/1", res.NumberMatched, len(res.Features))
	}
}

func TestFeatureByID(t *testing.T) {
	e := testEngine(t)
	f, err := e.Feature(context.Background(), "0")
	if err != nil {
		t.Fatal(err)
	}
	if f.Properties["name"] != "beijing" {
		t.Errorf("feature 0 = %v", f.Properties["name"])
	}
	if _, err := e.Feature(context.Background(), "nope"); err != source.ErrFeatureNotFound {
		t.Errorf("want ErrFeatureNotFound, got %v", err)
	}
}

func TestTileInfo(t *testing.T) {
	e := testEngine(t)
	info := e.TileInfo()
	if info.Format != source.FormatMVT || !info.Gzipped || !info.Cacheable {
		t.Errorf("unexpected info: %+v", info)
	}
	if info.Bounds[0] != -74.0 || info.Bounds[2] != 117.0 {
		t.Errorf("bounds wrong: %v", info.Bounds)
	}
	if len(info.VectorLayers) != 1 || info.VectorLayers[0].Fields["name"] != "String" {
		t.Errorf("vector layers wrong: %+v", info.VectorLayers)
	}
}
