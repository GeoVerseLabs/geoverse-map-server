package mysql

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/paulmach/orb"
	"github.com/paulmach/orb/encoding/mvt"
	"github.com/paulmach/orb/maptile"

	"github.com/GeoVerseLabs/geoverse-map-server/internal/config"
	"github.com/GeoVerseLabs/geoverse-map-server/internal/source"
)

func TestParseURLDSN(t *testing.T) {
	cfg, err := parseURLDSN("mysql://reader:p%40ss@127.0.0.1:3307/geoverse?tls=true&timeout=3s")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.User != "reader" || cfg.Passwd != "p@ss" || cfg.Addr != "127.0.0.1:3307" {
		t.Fatalf("credentials/address = %#v", cfg)
	}
	if cfg.DBName != "geoverse" || cfg.TLSConfig != "true" || cfg.Timeout != 3*time.Second {
		t.Fatalf("database/options = %#v", cfg)
	}
}

func TestParseURLDSNDefaultsPortAndRejectsInvalid(t *testing.T) {
	cfg, err := parseURLDSN("mysql://reader@db.internal/geoverse")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Addr != "db.internal:3306" {
		t.Fatalf("addr = %q", cfg.Addr)
	}
	for _, raw := range []string{
		"postgres://reader@localhost/geoverse",
		"mysql://reader@/geoverse",
		"mysql://reader@localhost/",
		"mysql://reader@localhost/geoverse?timeout=soon",
	} {
		if _, err := parseURLDSN(raw); err == nil {
			t.Errorf("parseURLDSN(%q) succeeded", raw)
		}
	}
}

func TestSQLHelpers(t *testing.T) {
	schema, table := splitTable("transport.roads", "geoverse")
	if schema != "transport" || table != "roads" {
		t.Fatalf("split qualified = %q %q", schema, table)
	}
	schema, table = splitTable("roads", "geoverse")
	if schema != "geoverse" || table != "roads" {
		t.Fatalf("split default = %q %q", schema, table)
	}
	if got := quoteIdent("odd`name"); got != "`odd``name`" {
		t.Fatalf("quoted identifier = %q", got)
	}
	wkt := boundWKT(orb.Bound{Min: orb.Point{120.1, 30.2}, Max: orb.Point{121.3, 31.4}})
	if !strings.HasPrefix(wkt, "POLYGON((120.1 30.2,121.3 30.2") ||
		!strings.HasSuffix(wkt, "120.1 30.2))") {
		t.Fatalf("wkt = %q", wkt)
	}
	got := clampWGS84Bound(orb.Bound{
		Min: orb.Point{-216, -90},
		Max: orb.Point{216, 90},
	})
	want := orb.Bound{
		Min: orb.Point{-180 + 1e-9, -85.05112877980659},
		Max: orb.Point{180 - 1e-9, 85.05112877980659},
	}
	if got != want {
		t.Fatalf("clampWGS84Bound() = %v, want %v", got, want)
	}
}

func TestGeometryExpressionsAndTypes(t *testing.T) {
	cases := []struct {
		srid int
		want string
	}{
		{0, "ST_SRID(t.`geom`, 4326)"},
		{4326, "t.`geom`"},
		{3857, "ST_Transform(t.`geom`, 4326)"},
	}
	for _, test := range cases {
		s := &Source{geomCol: "geom", srid: test.srid}
		if got := s.geometry4326(); got != test.want {
			t.Errorf("srid %d expression = %q", test.srid, got)
		}
	}
	maria := &Source{geomCol: "geom", srid: 4326, mariaDB: true}
	if got := maria.bboxGeometry(); got != "ST_GeomFromText(?, 4326)" {
		t.Fatalf("MariaDB bbox expression = %q", got)
	}
	mysql := &Source{geomCol: "geom", srid: 4326}
	if !strings.Contains(mysql.bboxGeometry(), "axis-order=long-lat") {
		t.Fatalf("MySQL bbox expression = %q", mysql.bboxGeometry())
	}
	if mysqlJSONType("BIGINT") != "Number" || mysqlJSONType("BIT") != "Boolean" ||
		mysqlJSONType("varchar") != "String" {
		t.Fatal("mysql type mapping is incomplete")
	}
	if !isGeometryType("MULTIPOLYGON") || isGeometryType("json") {
		t.Fatal("geometry type detection is incorrect")
	}
}

func TestMySQLIntegration(t *testing.T) {
	dsn := os.Getenv("GEOVERSE_TEST_MYSQL_DSN")
	if dsn == "" {
		t.Skip("GEOVERSE_TEST_MYSQL_DSN is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	src, err := New(ctx, config.Source{
		Name:           "warehouses",
		Type:           "mysql",
		DSN:            dsn,
		Table:          "warehouse",
		GeometryColumn: "location",
		IDColumn:       "id",
		SRID:           4326,
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
	if result.NumberMatched < 3 || len(result.Features) < 3 {
		t.Fatalf("features = %d/%d", len(result.Features), result.NumberMatched)
	}
	firstID := result.Features[0].ID
	feature, err := src.Feature(ctx, fmt.Sprint(firstID))
	if err != nil || feature.ID == nil {
		t.Fatalf("feature %v = %#v, %v", firstID, feature, err)
	}
	for _, zoom := range []maptile.Zoom{0, 1, 2, 8} {
		tile := maptile.At(orb.Point{121.5893, 31.2047}, zoom)
		payload, err := src.Tile(ctx, uint32(tile.Z), tile.X, tile.Y)
		if err != nil {
			t.Fatalf("tile %v: %v", tile, err)
		}
		if len(payload) < 2 || payload[0] != 0x1f || payload[1] != 0x8b {
			t.Fatalf("tile %v is not gzipped MVT: %x", tile, payload[:min(len(payload), 8)])
		}
		layers, err := mvt.UnmarshalGzipped(payload)
		if err != nil || len(layers) != 1 || len(layers[0].Features) == 0 {
			t.Fatalf("tile %v decode = %d layers, %v", tile, len(layers), err)
		}
	}
}

func dsnWithUser(dsn, user, pass string) (string, error) {
	u, err := url.Parse(dsn)
	if err != nil {
		return "", err
	}
	u.User = url.UserPassword(user, pass)
	return u.String(), nil
}

// TestMySQLExcessPrivileges proves ExcessPrivileges tells a properly scoped
// account apart from an over-privileged one against a real server (MySQL 8
// or MariaDB — GEOVERSE_TEST_MYSQL_DSN selects which). The "ambient account
// shows excess" half only needs GEOVERSE_TEST_MYSQL_DSN; the deterministic
// "scoped role shows zero excess" half additionally needs
// GEOVERSE_TEST_MYSQL_ADMIN_DSN (a root-equivalent account able to CREATE
// USER), since a MySQL "ALL PRIVILEGES ON db.*" grant does not itself
// include the right to create other accounts.
func TestMySQLExcessPrivileges(t *testing.T) {
	dsn := os.Getenv("GEOVERSE_TEST_MYSQL_DSN")
	if dsn == "" {
		t.Skip("GEOVERSE_TEST_MYSQL_DSN is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	src, err := New(ctx, config.Source{
		Name: "warehouses", Type: "mysql", DSN: dsn,
		Table: "warehouse", GeometryColumn: "location", IDColumn: "id", SRID: 4326,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer src.Close()

	// Documents a real, current gap: the docker-compose demo account (like
	// most "just grant everything on my one database" setups, including
	// what MySQL's own official image bootstraps via MYSQL_USER/
	// MYSQL_DATABASE) holds far more than SELECT. See DEPLOY.md for the
	// least-privilege account script this check exists to point operators
	// at.
	excess, err := src.ExcessPrivileges(ctx)
	if err != nil {
		t.Fatalf("ExcessPrivileges: %v", err)
	}
	if len(excess) == 0 {
		t.Fatal("expected the demo DSN account to report excess privileges")
	}

	adminDSN := os.Getenv("GEOVERSE_TEST_MYSQL_ADMIN_DSN")
	if adminDSN == "" {
		t.Skip("GEOVERSE_TEST_MYSQL_ADMIN_DSN is not set; skipping the scoped-role half")
	}
	adminDriverCfg, err := parseURLDSN(adminDSN)
	if err != nil {
		t.Fatal(err)
	}
	admin, err := sql.Open("mysql", adminDriverCfg.FormatDSN())
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()

	roUser := fmt.Sprintf("gv_test_ro_%d", time.Now().UnixNano())
	if _, err := admin.ExecContext(ctx, fmt.Sprintf(
		"CREATE USER '%s'@'%%' IDENTIFIED BY 'gv_test_ro'", roUser)); err != nil {
		t.Fatalf("create test user: %v", err)
	}
	defer admin.ExecContext(ctx, fmt.Sprintf("DROP USER '%s'@'%%'", roUser))
	if _, err := admin.ExecContext(ctx, fmt.Sprintf(
		"GRANT SELECT ON %s.warehouse TO '%s'@'%%'", src.schema, roUser)); err != nil {
		t.Fatalf("grant select: %v", err)
	}
	defer admin.ExecContext(ctx, fmt.Sprintf("REVOKE SELECT ON %s.warehouse FROM '%s'@'%%'", src.schema, roUser))

	roDSN, err := dsnWithUser(dsn, roUser, "gv_test_ro")
	if err != nil {
		t.Fatal(err)
	}
	roSrc, err := New(ctx, config.Source{
		Name: "ro-check", Type: "mysql", DSN: roDSN,
		Table: "warehouse", GeometryColumn: "location", IDColumn: "id", SRID: 4326,
	})
	if err != nil {
		t.Fatalf("connect as read-only user: %v", err)
	}
	defer roSrc.Close()
	excess, err = roSrc.ExcessPrivileges(ctx)
	if err != nil {
		t.Fatalf("ExcessPrivileges (read-only user): %v", err)
	}
	if len(excess) != 0 {
		t.Fatalf("read-only user must report zero excess privileges, got %v", excess)
	}
}
