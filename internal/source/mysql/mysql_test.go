package mysql

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/paulmach/orb"
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
	tile := maptile.At(orb.Point{121.5893, 31.2047}, 8)
	payload, err := src.Tile(ctx, uint32(tile.Z), tile.X, tile.Y)
	if err != nil {
		t.Fatal(err)
	}
	if len(payload) < 2 || payload[0] != 0x1f || payload[1] != 0x8b {
		t.Fatalf("tile is not gzipped MVT: %x", payload[:min(len(payload), 8)])
	}
}
