// Package postgis serves vector tiles and features straight from a
// PostGIS table. Tile encoding is pushed down to the database with
// ST_AsMVT / ST_TileEnvelope (PostGIS >= 3.0); feature queries use
// ST_AsGeoJSON with bbox pushdown.
package postgis

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/paulmach/orb/geojson"

	"github.com/GeoVerseLabs/geoverse-map-server/internal/config"
	"github.com/GeoVerseLabs/geoverse-map-server/internal/source"
)

// Source serves one PostGIS table.
type Source struct {
	name    string
	pool    *pgxpool.Pool
	schema  string
	table   string
	geomCol string
	idCol   string
	srid    int
	fields  []string
	extent  int
	buffer  int
	info    source.TileInfo
}

var (
	_ source.Source        = (*Source)(nil)
	_ source.TileSource    = (*Source)(nil)
	_ source.FeatureSource = (*Source)(nil)
)

// New connects to the database and introspects the configured table.
func New(ctx context.Context, cfg config.Source) (*Source, error) {
	pool, err := pgxpool.New(ctx, cfg.DSN)
	if err != nil {
		return nil, fmt.Errorf("source %q: connect: %w", cfg.Name, err)
	}
	s := &Source{
		name:    cfg.Name,
		pool:    pool,
		geomCol: firstNonEmpty(cfg.GeometryColumn, "geom"),
		idCol:   cfg.IDColumn,
		srid:    cfg.SRID,
		fields:  cfg.Fields,
		extent:  4096,
		buffer:  64,
	}
	if cfg.Extent != nil {
		s.extent = *cfg.Extent
	}
	if cfg.Buffer != nil {
		s.buffer = *cfg.Buffer
	}
	s.schema, s.table = splitTable(cfg.Table)
	if err := s.introspect(ctx, cfg); err != nil {
		pool.Close()
		return nil, fmt.Errorf("source %q: %w", cfg.Name, err)
	}
	return s, nil
}

func splitTable(t string) (schema, table string) {
	if i := strings.IndexByte(t, '.'); i >= 0 {
		return t[:i], t[i+1:]
	}
	return "public", t
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

// quoteIdent quotes a SQL identifier (config values are operator-supplied,
// but quoting keeps reserved words and mixed case working).
func quoteIdent(s string) string {
	return `"` + strings.ReplaceAll(s, `"`, `""`) + `"`
}

func (s *Source) relation() string {
	return quoteIdent(s.schema) + "." + quoteIdent(s.table)
}

func (s *Source) introspect(ctx context.Context, cfg config.Source) error {
	// Discover SRID if not configured.
	if s.srid == 0 {
		err := s.pool.QueryRow(ctx,
			`SELECT srid FROM geometry_columns
			 WHERE f_table_schema=$1 AND f_table_name=$2 AND f_geometry_column=$3`,
			s.schema, s.table, s.geomCol).Scan(&s.srid)
		if err != nil {
			s.srid = 4326
		}
	}
	// Discover attribute columns if not configured.
	if len(s.fields) == 0 {
		rows, err := s.pool.Query(ctx,
			`SELECT column_name, udt_name FROM information_schema.columns
			 WHERE table_schema=$1 AND table_name=$2 ORDER BY ordinal_position`,
			s.schema, s.table)
		if err != nil {
			return fmt.Errorf("introspect columns: %w", err)
		}
		defer rows.Close()
		for rows.Next() {
			var col, typ string
			if err := rows.Scan(&col, &typ); err != nil {
				return err
			}
			if col == s.geomCol || typ == "geometry" || typ == "geography" {
				continue
			}
			s.fields = append(s.fields, col)
		}
		if err := rows.Err(); err != nil {
			return err
		}
	}
	// Discover a primary key for feature ids if not configured.
	if s.idCol == "" {
		_ = s.pool.QueryRow(ctx, `
			SELECT a.attname FROM pg_index i
			JOIN pg_attribute a ON a.attrelid = i.indrelid AND a.attnum = ANY(i.indkey)
			WHERE i.indrelid = ($1::text)::regclass AND i.indisprimary
			LIMIT 1`, s.relation()).Scan(&s.idCol)
	}

	info := source.TileInfo{
		Name:        cfg.Name,
		Title:       firstNonEmpty(cfg.Title, s.table),
		Description: cfg.Description,
		Format:      source.FormatMVT,
		MinZoom:     0,
		MaxZoom:     22,
		Bounds:      [4]float64{-180, -85.05112877980659, 180, 85.05112877980659},
		Gzipped:     false,
		Cacheable:   true,
	}
	if cfg.MinZoom != nil {
		info.MinZoom = *cfg.MinZoom
	}
	if cfg.MaxZoom != nil {
		info.MaxZoom = *cfg.MaxZoom
	}
	// Cheap extent estimate; fall back to world bounds on empty stats.
	var b [4]float64
	err := s.pool.QueryRow(ctx, fmt.Sprintf(
		`SELECT ST_XMin(e), ST_YMin(e), ST_XMax(e), ST_YMax(e)
		 FROM (SELECT ST_Transform(ST_SetSRID(ST_EstimatedExtent($1,$2,$3),%d),4326) AS e) q`,
		s.srid), s.schema, s.table, s.geomCol).Scan(&b[0], &b[1], &b[2], &b[3])
	if err == nil {
		info.Bounds = b
	}
	info.Center = [3]float64{
		(info.Bounds[0] + info.Bounds[2]) / 2,
		(info.Bounds[1] + info.Bounds[3]) / 2,
		float64(info.MinZoom),
	}
	fields := map[string]string{}
	for _, f := range s.fields {
		fields[f] = "String"
	}
	info.VectorLayers = []source.VectorLayer{{ID: cfg.Name, Fields: fields}}
	s.info = info
	return nil
}

func (s *Source) selectList(prefix string) string {
	var sb strings.Builder
	for _, f := range s.fields {
		sb.WriteString(", ")
		sb.WriteString(prefix)
		sb.WriteString(quoteIdent(f))
	}
	return sb.String()
}

// Tile implements source.TileSource.
func (s *Source) Tile(ctx context.Context, z, x, y uint32) ([]byte, error) {
	geomExpr := fmt.Sprintf("ST_Transform(t.%s, 3857)", quoteIdent(s.geomCol))
	if s.srid == 3857 {
		geomExpr = "t." + quoteIdent(s.geomCol)
	}
	mvtArgs := fmt.Sprintf("mvtgeom.*, '%s', %d, 'geom'", s.name, s.extent)
	if s.idCol != "" {
		mvtArgs += fmt.Sprintf(", %s", quoteLiteral(s.idCol))
	}
	sql := fmt.Sprintf(`
		WITH mvtgeom AS (
			SELECT ST_AsMVTGeom(%s, ST_TileEnvelope($1,$2,$3), %d, %d, true) AS geom%s
			FROM %s t
			WHERE t.%s && ST_Transform(ST_TileEnvelope($1,$2,$3, margin => 0.03125), %d)
		)
		SELECT ST_AsMVT(%s) FROM mvtgeom WHERE geom IS NOT NULL`,
		geomExpr, s.extent, s.buffer, s.selectList("t."),
		s.relation(), quoteIdent(s.geomCol), s.srid,
		mvtArgs)
	var data []byte
	if err := s.pool.QueryRow(ctx, sql, z, x, y).Scan(&data); err != nil {
		return nil, fmt.Errorf("st_asmvt: %w", err)
	}
	if len(data) == 0 {
		return nil, source.ErrTileNotFound
	}
	return data, nil
}

func quoteLiteral(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "''") + "'"
}

// TileInfo implements source.TileSource.
func (s *Source) TileInfo() source.TileInfo { return s.info }

// Features implements source.FeatureSource.
func (s *Source) Features(ctx context.Context, q source.FeatureQuery) (*source.FeatureResult, error) {
	where := "TRUE"
	args := []interface{}{}
	if q.BBox != nil {
		where = fmt.Sprintf(
			"t.%s && ST_Transform(ST_MakeEnvelope($1,$2,$3,$4,4326), %d)",
			quoteIdent(s.geomCol), s.srid)
		args = append(args, q.BBox[0], q.BBox[1], q.BBox[2], q.BBox[3])
	}

	var total int
	countSQL := fmt.Sprintf(`SELECT count(*) FROM %s t WHERE %s`, s.relation(), where)
	if err := s.pool.QueryRow(ctx, countSQL, args...).Scan(&total); err != nil {
		return nil, fmt.Errorf("count: %w", err)
	}

	limit, offset := q.Limit, q.Offset
	if limit <= 0 {
		limit = 10
	}
	orderBy := ""
	if s.idCol != "" {
		orderBy = "ORDER BY t." + quoteIdent(s.idCol)
	}
	sql := fmt.Sprintf(`
		SELECT ST_AsGeoJSON(ST_Transform(t.%s, 4326))%s
		FROM %s t WHERE %s %s
		LIMIT %d OFFSET %d`,
		quoteIdent(s.geomCol), s.selectList("t."), s.relation(), where, orderBy, limit, offset)
	rows, err := s.pool.Query(ctx, sql, args...)
	if err != nil {
		return nil, fmt.Errorf("query features: %w", err)
	}
	defer rows.Close()

	res := &source.FeatureResult{NumberMatched: total}
	for rows.Next() {
		f, err := s.scanFeature(rows)
		if err != nil {
			return nil, err
		}
		res.Features = append(res.Features, f)
	}
	return res, rows.Err()
}

func (s *Source) scanFeature(rows pgx.Rows) (*geojson.Feature, error) {
	vals, err := rows.Values()
	if err != nil {
		return nil, err
	}
	gj, _ := vals[0].(string)
	geom, err := geojson.UnmarshalGeometry([]byte(gj))
	if err != nil {
		return nil, fmt.Errorf("parse geometry: %w", err)
	}
	f := geojson.NewFeature(geom.Geometry())
	for i, name := range s.fields {
		v := vals[i+1]
		if v == nil {
			continue
		}
		if name == s.idCol {
			f.ID = normalizeJSON(v)
		}
		f.Properties[name] = normalizeJSON(v)
	}
	return f, nil
}

// normalizeJSON converts pgx-native values into JSON-friendly ones.
func normalizeJSON(v interface{}) interface{} {
	switch t := v.(type) {
	case []byte:
		return string(t)
	case json.RawMessage:
		return string(t)
	default:
		return v
	}
}

// Feature implements source.FeatureSource.
func (s *Source) Feature(ctx context.Context, id string) (*geojson.Feature, error) {
	if s.idCol == "" {
		return nil, fmt.Errorf("source %q has no id column configured", s.name)
	}
	sql := fmt.Sprintf(`
		SELECT ST_AsGeoJSON(ST_Transform(t.%s, 4326))%s
		FROM %s t WHERE t.%s::text = $1 LIMIT 1`,
		quoteIdent(s.geomCol), s.selectList("t."), s.relation(), quoteIdent(s.idCol))
	rows, err := s.pool.Query(ctx, sql, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return nil, err
		}
		return nil, source.ErrFeatureNotFound
	}
	return s.scanFeature(rows)
}

// CollectionInfo implements source.FeatureSource.
func (s *Source) CollectionInfo() source.CollectionInfo {
	return source.CollectionInfo{
		Name:        s.info.Name,
		Title:       s.info.Title,
		Description: s.info.Description,
		Bounds:      s.info.Bounds,
	}
}

// Name implements source.Source.
func (s *Source) Name() string { return s.name }

// Ping implements source.Source.
func (s *Source) Ping(ctx context.Context) error { return s.pool.Ping(ctx) }

// Close implements source.Source.
func (s *Source) Close() error {
	s.pool.Close()
	return nil
}
