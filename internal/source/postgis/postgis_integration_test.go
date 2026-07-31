package postgis

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/paulmach/orb"
	"github.com/paulmach/orb/encoding/mvt"
	"github.com/paulmach/orb/maptile"

	"github.com/GeoVerseLabs/geoverse-map-server/internal/config"
	"github.com/GeoVerseLabs/geoverse-map-server/internal/source"
)

func TestPostGISIntegration(t *testing.T) {
	dsn := os.Getenv("GEOVERSE_TEST_POSTGIS_DSN")
	if dsn == "" {
		t.Skip("GEOVERSE_TEST_POSTGIS_DSN is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	src, err := New(ctx, config.Source{
		Name:           "live-features",
		Type:           "postgis",
		DSN:            dsn,
		Table:          "geo.feature",
		GeometryColumn: "geom",
		IDColumn:       "id",
		SRID:           4326,
		Fields:         []string{"id", "layer_id", "feature_type"},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer src.Close()
	if err := src.Ping(ctx); err != nil {
		t.Fatal(err)
	}
	if src.mvtIDCol != "" {
		t.Fatalf("UUID id must not be used as an MVT integer feature id: %q", src.mvtIDCol)
	}
	result, err := src.Features(ctx, source.FeatureQuery{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if result.NumberMatched == 0 || len(result.Features) == 0 {
		t.Fatalf("features = %d/%d", len(result.Features), result.NumberMatched)
	}
	first := result.Features[0]
	if first.ID == nil {
		t.Fatal("first feature has no configured id")
	}
	if _, err := src.Feature(ctx, fmt.Sprint(first.ID)); err != nil {
		t.Fatalf("feature %v: %v", first.ID, err)
	}
	// z0 covers geometries that reach latitude -90. The source must clip
	// them to the Web Mercator tile envelope before ST_Transform. z18 also
	// protects the existing small-geometry path.
	tiles := []maptile.Tile{
		maptile.New(0, 0, 0),
		maptile.At(first.Geometry.Bound().Center(), 18),
	}
	for _, zoom := range []maptile.Zoom{1, 2} {
		tiles = append(tiles, maptile.At(orb.Point{118.089, 24.479}, zoom))
	}
	for _, tile := range tiles {
		payload, err := src.Tile(ctx, uint32(tile.Z), tile.X, tile.Y)
		if err != nil {
			t.Fatalf("tile %v: %v", tile, err)
		}
		if len(payload) == 0 {
			t.Fatalf("PostGIS returned an empty MVT for %v", tile)
		}
		layers, err := mvt.Unmarshal(payload)
		if err != nil || len(layers) != 1 || len(layers[0].Features) == 0 {
			t.Fatalf("tile %v decode = %d layers, %v", tile, len(layers), err)
		}
	}
}
