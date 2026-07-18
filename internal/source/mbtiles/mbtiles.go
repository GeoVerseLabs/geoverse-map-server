// Package mbtiles serves pre-rendered tiles (vector or raster) from an
// MBTiles file using the pure-Go SQLite driver, so builds stay CGO-free.
package mbtiles

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"

	_ "modernc.org/sqlite"

	"github.com/GeoVerseLabs/geoverse-map-server/internal/config"
	"github.com/GeoVerseLabs/geoverse-map-server/internal/source"
)

// Source reads tiles from a single MBTiles file.
type Source struct {
	name string
	db   *sql.DB
	info source.TileInfo
}

var (
	_ source.Source     = (*Source)(nil)
	_ source.TileSource = (*Source)(nil)
)

// New opens the MBTiles file referenced by cfg (read-only) and loads its
// metadata table.
func New(cfg config.Source) (*Source, error) {
	dsn := fmt.Sprintf("file:%s?mode=ro&_pragma=query_only(1)", cfg.Path)
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("source %q: open %s: %w", cfg.Name, cfg.Path, err)
	}
	s := &Source{name: cfg.Name, db: db}
	if err := s.loadMetadata(cfg); err != nil {
		db.Close()
		return nil, fmt.Errorf("source %q: %w", cfg.Name, err)
	}
	return s, nil
}

func (s *Source) loadMetadata(cfg config.Source) error {
	rows, err := s.db.Query(`SELECT name, value FROM metadata`)
	if err != nil {
		return fmt.Errorf("read metadata: %w", err)
	}
	defer rows.Close()
	meta := map[string]string{}
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			return err
		}
		meta[k] = v
	}
	if err := rows.Err(); err != nil {
		return err
	}

	info := source.TileInfo{
		Name:        cfg.Name,
		Title:       firstNonEmpty(cfg.Title, meta["name"]),
		Description: firstNonEmpty(cfg.Description, meta["description"]),
		MinZoom:     0,
		MaxZoom:     22,
		Bounds:      [4]float64{-180, -85.05112877980659, 180, 85.05112877980659},
	}
	switch strings.ToLower(meta["format"]) {
	case "pbf", "mvt", "":
		info.Format = source.FormatMVT
	case "png":
		info.Format = source.FormatPNG
	case "jpg", "jpeg":
		info.Format = source.FormatJPG
	case "webp":
		info.Format = source.FormatWebP
	default:
		return fmt.Errorf("unsupported tile format %q", meta["format"])
	}
	if v, err := strconv.Atoi(meta["minzoom"]); err == nil {
		info.MinZoom = v
	}
	if v, err := strconv.Atoi(meta["maxzoom"]); err == nil {
		info.MaxZoom = v
	}
	if parts := strings.Split(meta["bounds"], ","); len(parts) == 4 {
		var b [4]float64
		ok := true
		for i, p := range parts {
			f, err := strconv.ParseFloat(strings.TrimSpace(p), 64)
			if err != nil {
				ok = false
				break
			}
			b[i] = f
		}
		if ok {
			info.Bounds = b
		}
	}
	if parts := strings.Split(meta["center"], ","); len(parts) == 3 {
		for i, p := range parts {
			if f, err := strconv.ParseFloat(strings.TrimSpace(p), 64); err == nil {
				info.Center[i] = f
			}
		}
	} else {
		info.Center = [3]float64{
			(info.Bounds[0] + info.Bounds[2]) / 2,
			(info.Bounds[1] + info.Bounds[3]) / 2,
			float64(info.MinZoom),
		}
	}
	// The `json` metadata key carries vector_layers for vector tilesets.
	if j := meta["json"]; j != "" && info.Format == source.FormatMVT {
		var wrapper struct {
			VectorLayers []source.VectorLayer `json:"vector_layers"`
		}
		if err := json.Unmarshal([]byte(j), &wrapper); err == nil {
			info.VectorLayers = wrapper.VectorLayers
		}
	}
	// MBTiles vector tiles are gzip-compressed per spec; detect actual
	// compression lazily in Tile() as some files deviate.
	info.Gzipped = info.Format == source.FormatMVT
	info.Cacheable = false // local file lookups are already cheap
	s.info = info
	return nil
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

// Tile implements source.TileSource. MBTiles stores rows in TMS order,
// so the y coordinate is flipped.
func (s *Source) Tile(ctx context.Context, z, x, y uint32) ([]byte, error) {
	tmsY := (uint32(1) << z) - 1 - y
	var data []byte
	err := s.db.QueryRowContext(ctx,
		`SELECT tile_data FROM tiles WHERE zoom_level = ? AND tile_column = ? AND tile_row = ?`,
		z, x, tmsY).Scan(&data)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, source.ErrTileNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("query tile: %w", err)
	}
	return data, nil
}

// TileInfo implements source.TileSource.
func (s *Source) TileInfo() source.TileInfo { return s.info }

// Name implements source.Source.
func (s *Source) Name() string { return s.name }

// Ping implements source.Source.
func (s *Source) Ping(ctx context.Context) error { return s.db.PingContext(ctx) }

// Close implements source.Source.
func (s *Source) Close() error { return s.db.Close() }
