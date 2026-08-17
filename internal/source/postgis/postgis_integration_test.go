package postgis

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
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

func roDSNWith(dsn, user, pass string) (string, error) {
	u, err := url.Parse(dsn)
	if err != nil {
		return "", err
	}
	u.User = url.UserPassword(user, pass)
	return u.String(), nil
}

// TestPostGISExcessPrivileges proves ExcessPrivileges tells a properly
// scoped role apart from an over-privileged one, against a real database —
// not just that the query parses.
func TestPostGISExcessPrivileges(t *testing.T) {
	dsn := os.Getenv("GEOVERSE_TEST_POSTGIS_DSN")
	if dsn == "" {
		t.Skip("GEOVERSE_TEST_POSTGIS_DSN is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	src, err := New(ctx, config.Source{
		Name: "live-features", Type: "postgis", DSN: dsn,
		Table: "geo.feature", GeometryColumn: "geom", IDColumn: "id", SRID: 4326,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer src.Close()

	// The DSN account this whole suite connects with (and that
	// examples/config.multisource.yaml uses in production-shaped configs)
	// is the table owner's own role, which always holds more than SELECT.
	// Asserting that here documents the gap this feature exists to catch,
	// rather than assuming an idealized already-scoped account.
	excess, err := src.ExcessPrivileges(ctx)
	if err != nil {
		t.Fatalf("ExcessPrivileges: %v", err)
	}
	if len(excess) == 0 {
		t.Fatal("expected the table-owner DSN account to report excess privileges")
	}

	// A role granted only USAGE+SELECT must report zero excess — this half
	// of the test is self-contained (creates and tears down its own role)
	// so it does not depend on any pre-existing role in the test database.
	admin, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	roRole := fmt.Sprintf("gv_test_ro_%d", time.Now().UnixNano())
	if _, err := admin.Exec(ctx, fmt.Sprintf(`CREATE ROLE %s LOGIN PASSWORD 'gv_test_ro'`, roRole)); err != nil {
		t.Fatalf("create test role: %v", err)
	}
	defer admin.Exec(ctx, fmt.Sprintf(`DROP ROLE %s`, roRole))
	if _, err := admin.Exec(ctx, "GRANT USAGE ON SCHEMA geo TO "+roRole); err != nil {
		t.Fatalf("grant usage: %v", err)
	}
	defer admin.Exec(ctx, "REVOKE USAGE ON SCHEMA geo FROM "+roRole)
	if _, err := admin.Exec(ctx, "GRANT SELECT ON geo.feature TO "+roRole); err != nil {
		t.Fatalf("grant select: %v", err)
	}
	defer admin.Exec(ctx, "REVOKE SELECT ON geo.feature FROM "+roRole)

	roDSN, err := roDSNWith(dsn, roRole, "gv_test_ro")
	if err != nil {
		t.Fatal(err)
	}
	roSrc, err := New(ctx, config.Source{
		Name: "ro-check", Type: "postgis", DSN: roDSN,
		Table: "geo.feature", GeometryColumn: "geom", IDColumn: "id", SRID: 4326,
	})
	if err != nil {
		t.Fatalf("connect as read-only role: %v", err)
	}
	defer roSrc.Close()
	excess, err = roSrc.ExcessPrivileges(ctx)
	if err != nil {
		t.Fatalf("ExcessPrivileges (read-only role): %v", err)
	}
	if len(excess) != 0 {
		t.Fatalf("read-only role must report zero excess privileges, got %v", excess)
	}
}
