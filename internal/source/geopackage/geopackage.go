// Package geopackage loads vector layers from an OGC GeoPackage file into
// the in-memory engine. It parses the GeoPackage binary geometry format
// (GPB header + WKB) directly, keeping the build pure Go.
package geopackage

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/paulmach/orb"
	"github.com/paulmach/orb/encoding/wkb"
	"github.com/paulmach/orb/geojson"
	"github.com/paulmach/orb/project"
	_ "modernc.org/sqlite"

	"github.com/GeoVerseLabs/geoverse-map-server/internal/config"
	"github.com/GeoVerseLabs/geoverse-map-server/internal/source"
	"github.com/GeoVerseLabs/geoverse-map-server/internal/source/memengine"
)

// Source is a GeoPackage-file-backed source (one feature table).
type Source struct {
	*memengine.Engine
	name string
}

var (
	_ source.Source        = (*Source)(nil)
	_ source.TileSource    = (*Source)(nil)
	_ source.FeatureSource = (*Source)(nil)
)

// New loads one feature table from the GeoPackage at cfg.Path. When
// cfg.Layer is empty and the package has exactly one feature table, that
// table is used.
func New(cfg config.Source) (*Source, error) {
	dsn := fmt.Sprintf("file:%s?mode=ro&_pragma=query_only(1)", cfg.Path)
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("source %q: open %s: %w", cfg.Name, cfg.Path, err)
	}
	defer db.Close()

	table, geomCol, srs, err := pickLayer(db, cfg.Layer)
	if err != nil {
		return nil, fmt.Errorf("source %q: %w", cfg.Name, err)
	}
	feats, err := loadFeatures(db, table, geomCol, srs)
	if err != nil {
		return nil, fmt.Errorf("source %q: layer %s: %w", cfg.Name, table, err)
	}

	opts := memengine.Options{
		Name:        cfg.Name,
		Title:       firstNonEmpty(cfg.Title, table),
		Description: cfg.Description,
		Simplify:    cfg.Simplify == nil || *cfg.Simplify,
	}
	if cfg.MinZoom != nil {
		opts.MinZoom = *cfg.MinZoom
	}
	if cfg.MaxZoom != nil {
		opts.MaxZoom = *cfg.MaxZoom
	}
	eng, err := memengine.New(opts, feats)
	if err != nil {
		return nil, err
	}
	return &Source{Engine: eng, name: cfg.Name}, nil
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

func pickLayer(db *sql.DB, want string) (table, geomCol string, srs int, err error) {
	rows, err := db.Query(`
		SELECT c.table_name, g.column_name, g.srs_id
		FROM gpkg_contents c
		JOIN gpkg_geometry_columns g ON g.table_name = c.table_name
		WHERE c.data_type = 'features'`)
	if err != nil {
		return "", "", 0, fmt.Errorf("read gpkg_contents: %w", err)
	}
	defer rows.Close()
	type layer struct {
		table, col string
		srs        int
	}
	var layers []layer
	for rows.Next() {
		var l layer
		if err := rows.Scan(&l.table, &l.col, &l.srs); err != nil {
			return "", "", 0, err
		}
		layers = append(layers, l)
	}
	if err := rows.Err(); err != nil {
		return "", "", 0, err
	}
	if len(layers) == 0 {
		return "", "", 0, fmt.Errorf("no feature tables in geopackage")
	}
	if want == "" {
		if len(layers) > 1 {
			names := make([]string, len(layers))
			for i, l := range layers {
				names[i] = l.table
			}
			return "", "", 0, fmt.Errorf("geopackage has multiple feature tables %v; set 'layer' in the source config", names)
		}
		want = layers[0].table
	}
	for _, l := range layers {
		if l.table == want {
			if l.srs != 4326 && l.srs != 3857 && l.srs != 0 {
				return "", "", 0, fmt.Errorf("layer %s uses SRS %d; only EPSG:4326 and EPSG:3857 are supported", l.table, l.srs)
			}
			return l.table, l.col, l.srs, nil
		}
	}
	return "", "", 0, fmt.Errorf("feature table %q not found", want)
}

func loadFeatures(db *sql.DB, table, geomCol string, srs int) ([]*geojson.Feature, error) {
	// Table and column names come from gpkg system tables; quote them.
	q := fmt.Sprintf(`SELECT * FROM %q`, table)
	rows, err := db.Query(q)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	cols, err := rows.Columns()
	if err != nil {
		return nil, err
	}
	var feats []*geojson.Feature
	vals := make([]interface{}, len(cols))
	ptrs := make([]interface{}, len(cols))
	for i := range vals {
		ptrs[i] = &vals[i]
	}
	for rows.Next() {
		if err := rows.Scan(ptrs...); err != nil {
			return nil, err
		}
		var geom orb.Geometry
		props := geojson.Properties{}
		var id interface{}
		for i, c := range cols {
			v := vals[i]
			switch {
			case c == geomCol:
				blob, ok := v.([]byte)
				if !ok || len(blob) == 0 {
					continue
				}
				g, err := parseGPB(blob)
				if err == errEmptyGeom {
					continue
				}
				if err != nil {
					return nil, fmt.Errorf("parse geometry: %w", err)
				}
				geom = g
			case c == "fid" || c == "id":
				id = normalize(v)
				props[c] = normalize(v)
			default:
				if v != nil {
					props[c] = normalize(v)
				}
			}
		}
		if geom == nil {
			continue
		}
		if srs == 3857 {
			geom = project.Geometry(geom, project.Mercator.ToWGS84)
		}
		f := geojson.NewFeature(geom)
		f.Properties = props
		f.ID = id
		feats = append(feats, f)
	}
	return feats, rows.Err()
}

func normalize(v interface{}) interface{} {
	if b, ok := v.([]byte); ok {
		return string(b)
	}
	return v
}

var errEmptyGeom = fmt.Errorf("empty geometry")

// parseGPB decodes a GeoPackage StandardGeoPackageBinary blob into a
// geometry (header per OGC 12-128r19 §2.1.3).
func parseGPB(b []byte) (orb.Geometry, error) {
	if len(b) < 8 || b[0] != 'G' || b[1] != 'P' {
		return nil, fmt.Errorf("bad GPB magic")
	}
	flags := b[3]
	if flags&(1<<5) != 0 {
		return nil, fmt.Errorf("extended GPB geometry not supported")
	}
	if flags&(1<<4) != 0 {
		return nil, errEmptyGeom
	}
	envSizes := []int{0, 32, 48, 48, 64}
	envInd := int(flags>>1) & 0x7
	if envInd >= len(envSizes) {
		return nil, fmt.Errorf("invalid envelope indicator %d", envInd)
	}
	offset := 8 + envSizes[envInd]
	if len(b) < offset {
		return nil, fmt.Errorf("truncated GPB header")
	}
	return wkb.Unmarshal(b[offset:])
}

// Name implements source.Source.
func (s *Source) Name() string { return s.name }

// Ping implements source.Source; data is fully loaded into memory.
func (s *Source) Ping(context.Context) error { return nil }

// Close implements source.Source.
func (s *Source) Close() error { return nil }
