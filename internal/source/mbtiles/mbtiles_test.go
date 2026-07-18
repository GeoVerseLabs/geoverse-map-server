package mbtiles

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"

	"github.com/GeoVerseLabs/geoverse-map-server/internal/config"
	"github.com/GeoVerseLabs/geoverse-map-server/internal/source"
)

func writeFixture(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "test.mbtiles")
	db, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	stmts := []string{
		`CREATE TABLE metadata (name TEXT, value TEXT)`,
		`CREATE TABLE tiles (zoom_level INTEGER, tile_column INTEGER, tile_row INTEGER, tile_data BLOB)`,
		`INSERT INTO metadata VALUES ('name', 'Test Set')`,
		`INSERT INTO metadata VALUES ('format', 'png')`,
		`INSERT INTO metadata VALUES ('minzoom', '0')`,
		`INSERT INTO metadata VALUES ('maxzoom', '2')`,
		`INSERT INTO metadata VALUES ('bounds', '-10,-10,10,10')`,
	}
	for _, s := range stmts {
		if _, err := db.Exec(s); err != nil {
			t.Fatal(err)
		}
	}
	// XYZ z=1,x=0,y=1 corresponds to TMS row 0.
	if _, err := db.Exec(`INSERT INTO tiles VALUES (1, 0, 0, ?)`, []byte("PNGDATA")); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestMetadataAndTile(t *testing.T) {
	src, err := New(config.Source{Name: "base", Type: "mbtiles", Path: writeFixture(t)})
	if err != nil {
		t.Fatal(err)
	}
	defer src.Close()

	info := src.TileInfo()
	if info.Format != source.FormatPNG {
		t.Errorf("format = %v, want png", info.Format)
	}
	if info.Title != "Test Set" || info.MinZoom != 0 || info.MaxZoom != 2 {
		t.Errorf("metadata wrong: %+v", info)
	}
	if info.Bounds != [4]float64{-10, -10, 10, 10} {
		t.Errorf("bounds = %v", info.Bounds)
	}

	// TMS row 0 at z=1 is XYZ y=1.
	data, err := src.Tile(context.Background(), 1, 0, 1)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "PNGDATA" {
		t.Errorf("tile data = %q", data)
	}

	if _, err := src.Tile(context.Background(), 1, 1, 1); !errors.Is(err, source.ErrTileNotFound) {
		t.Errorf("missing tile: got %v, want ErrTileNotFound", err)
	}
}

func TestUnsupportedFormat(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bad.mbtiles")
	db, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = db.Exec(`CREATE TABLE metadata (name TEXT, value TEXT)`)
	_, _ = db.Exec(`INSERT INTO metadata VALUES ('format', 'tiff')`)
	db.Close()
	if _, err := New(config.Source{Name: "bad", Type: "mbtiles", Path: path}); err == nil {
		t.Fatal("expected error for unsupported format")
	}
}
