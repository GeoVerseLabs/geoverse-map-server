package geopackage

import (
	"context"
	"database/sql"
	"encoding/binary"
	"path/filepath"
	"testing"

	"github.com/paulmach/orb"
	"github.com/paulmach/orb/encoding/wkb"
	_ "modernc.org/sqlite"

	"github.com/GeoVerseLabs/geoverse-map-server/internal/config"
	"github.com/GeoVerseLabs/geoverse-map-server/internal/source"
)

// gpb wraps a geometry in a StandardGeoPackageBinary blob (no envelope).
func gpb(t *testing.T, g orb.Geometry, srs int32) []byte {
	t.Helper()
	wkbData, err := wkb.Marshal(g)
	if err != nil {
		t.Fatal(err)
	}
	header := make([]byte, 8)
	header[0], header[1] = 'G', 'P'
	header[2] = 0    // version
	header[3] = 0x01 // flags: little-endian header, no envelope
	binary.LittleEndian.PutUint32(header[4:], uint32(srs))
	return append(header, wkbData...)
}

func writeFixture(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "test.gpkg")
	db, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	stmts := []string{
		`CREATE TABLE gpkg_contents (
			table_name TEXT PRIMARY KEY, data_type TEXT, identifier TEXT,
			description TEXT, min_x REAL, min_y REAL, max_x REAL, max_y REAL, srs_id INTEGER)`,
		`CREATE TABLE gpkg_geometry_columns (
			table_name TEXT, column_name TEXT, geometry_type_name TEXT,
			srs_id INTEGER, z INTEGER, m INTEGER)`,
		`CREATE TABLE cities (fid INTEGER PRIMARY KEY, name TEXT, pop INTEGER, geom BLOB)`,
		`INSERT INTO gpkg_contents (table_name, data_type) VALUES ('cities', 'features')`,
		`INSERT INTO gpkg_geometry_columns VALUES ('cities', 'geom', 'POINT', 4326, 0, 0)`,
	}
	for _, s := range stmts {
		if _, err := db.Exec(s); err != nil {
			t.Fatalf("%s: %v", s, err)
		}
	}
	ins, err := db.Prepare(`INSERT INTO cities (name, pop, geom) VALUES (?, ?, ?)`)
	if err != nil {
		t.Fatal(err)
	}
	defer ins.Close()
	rows := []struct {
		name string
		pop  int
		pt   orb.Point
	}{
		{"beijing", 21893095, orb.Point{116.39, 39.91}},
		{"shanghai", 24870895, orb.Point{121.47, 31.23}},
	}
	for _, r := range rows {
		if _, err := ins.Exec(r.name, r.pop, gpb(t, r.pt, 4326)); err != nil {
			t.Fatal(err)
		}
	}
	return path
}

func TestLoadAndQuery(t *testing.T) {
	path := writeFixture(t)
	src, err := New(config.Source{Name: "cities", Type: "geopackage", Path: path})
	if err != nil {
		t.Fatal(err)
	}
	defer src.Close()

	if src.Count() != 2 {
		t.Fatalf("Count = %d, want 2", src.Count())
	}

	res, err := src.Features(context.Background(), source.FeatureQuery{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Features) != 2 {
		t.Fatalf("features = %d, want 2", len(res.Features))
	}
	f := res.Features[0]
	if f.Properties["name"] != "beijing" {
		t.Errorf("first feature name = %v", f.Properties["name"])
	}
	pt, ok := f.Geometry.(orb.Point)
	if !ok || pt[0] != 116.39 || pt[1] != 39.91 {
		t.Errorf("geometry wrong: %v", f.Geometry)
	}

	// The layer must also produce vector tiles.
	if _, err := src.Tile(context.Background(), 6, 52, 24); err != nil {
		t.Fatalf("tile: %v", err)
	}

	// Lookup by fid.
	got, err := src.Feature(context.Background(), "1")
	if err != nil {
		t.Fatal(err)
	}
	if got.Properties["name"] != "beijing" {
		t.Errorf("feature 1 = %v", got.Properties["name"])
	}
}

func TestExplicitLayerNotFound(t *testing.T) {
	path := writeFixture(t)
	_, err := New(config.Source{Name: "x", Type: "geopackage", Path: path, Layer: "nope"})
	if err == nil {
		t.Fatal("expected error for unknown layer")
	}
}

func TestParseGPBRejectsGarbage(t *testing.T) {
	if _, err := parseGPB([]byte("not a gpb blob")); err == nil {
		t.Fatal("expected error for bad magic")
	}
	if _, err := parseGPB([]byte{'G', 'P'}); err == nil {
		t.Fatal("expected error for short blob")
	}
}
