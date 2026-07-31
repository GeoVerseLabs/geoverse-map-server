package postgis

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

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
		IDColumn:       "project_id",
		SRID:           4326,
		Fields:         []string{"project_id", "layer_id", "feature_type"},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer src.Close()
	if err := src.Ping(ctx); err != nil {
		t.Fatal(err)
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
	// The Live table contains sub-metre polygons. At low zoom
	// ST_AsMVTGeom correctly drops geometry smaller than one tile unit, so
	// verify at the example profile's maximum zoom.
	tile := maptile.At(first.Geometry.Bound().Center(), 18)
	payload, err := src.Tile(ctx, uint32(tile.Z), tile.X, tile.Y)
	if err != nil {
		t.Fatal(err)
	}
	if len(payload) == 0 {
		t.Fatal("PostGIS returned an empty MVT")
	}
}
