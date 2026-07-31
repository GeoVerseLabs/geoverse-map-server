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
	name     string
	pool     *pgxpool.Pool
	schema   string
	table    string
	geomCol  string
	idCol    string
	mvtIDCol string
	srid     int
	fields   []string
	extent   int
	buffer   int
	info     source.TileInfo
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
	rows, err := s.pool.Query(ctx,
		`SELECT column_name, udt_name FROM information_schema.columns
		 WHERE table_schema=$1 AND table_name=$2 ORDER BY ordinal_position`,
		s.schema, s.table)
	if err != nil {
		return fmt.Errorf("introspect columns: %w", err)
	}
	columnTypes := map[string]string{}
	var discoveredFields []string
	geometryFound := false
	for rows.Next() {
		var col, typ string
		if err := rows.Scan(&col, &typ); err != nil {
			rows.Close()
			return err
		}
		columnTypes[col] = typ
		if col == s.geomCol && (typ == "geometry" || typ == "geography") {
			geometryFound = true
		}
		if col != s.geomCol && typ != "geometry" && typ != "geography" {
			discoveredFields = append(discoveredFields, col)
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	if !geometryFound {
		return fmt.Errorf("geometry column %q was not found in %s", s.geomCol, s.relation())
	}
	// Discover a primary key for feature ids if not configured.
	if s.idCol == "" {
		_ = s.pool.QueryRow(ctx, `
			SELECT a.attname FROM pg_index i
			JOIN pg_attribute a ON a.attrelid = i.indrelid AND a.attnum = ANY(i.indkey)
			WHERE i.indrelid = ($1::text)::regclass AND i.indisprimary
			LIMIT 1`, s.relation()).Scan(&s.idCol)
	}
	if s.idCol != "" {
		idType, ok := columnTypes[s.idCol]
		if !ok {
			return fmt.Errorf("id column %q was not found in %s", s.idCol, s.relation())
		}
		// PostGIS accepts only int2/int4/int8 for the optional MVT feature
		// id argument. UUID/text keys remain valid OGC feature IDs and MVT
		// properties, but must not be passed as feature_id_name.
		if isPostgresInteger(idType) {
			s.mvtIDCol = s.idCol
		}
	}
	if len(cfg.Fields) == 0 {
		s.fields = discoveredFields
	} else {
		seen := map[string]bool{}
		for _, field := range cfg.Fields {
			typ, ok := columnTypes[field]
			if !ok {
				return fmt.Errorf("field %q was not found in %s", field, s.relation())
			}
			if field == s.geomCol || typ == "geometry" || typ == "geography" || seen[field] {
				continue
			}
			s.fields = append(s.fields, field)
			seen[field] = true
		}
		if s.idCol != "" && !seen[s.idCol] {
			s.fields = append(s.fields, s.idCol)
		}
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
	err = s.pool.QueryRow(ctx, fmt.Sprintf(
		`SELECT ST_XMin(e), ST_YMin(e), ST_XMax(e), ST_YMax(e)
		 FROM (SELECT ST_Transform(ST_SetSRID(ST_EstimatedExtent($1,$2,$3),%d),4326) AS e) q`,
		s.srid), s.schema, s.table, s.geomCol).Scan(&b[0], &b[1], &b[2], &b[3])
	if err == nil {
		const maxMercatorLatitude = 85.05112877980659
		b[0] = max(-180, b[0])
		b[1] = max(-maxMercatorLatitude, b[1])
		b[2] = min(180, b[2])
		b[3] = min(maxMercatorLatitude, b[3])
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

func isPostgresInteger(udtName string) bool {
	switch udtName {
	case "int2", "int4", "int8":
		return true
	default:
		return false
	}
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
	// Clamp the buffered envelope to the global Web Mercator domain before
	// transforming it. Otherwise PROJ wraps x > 20037508 to the opposite
	// hemisphere, so eastern z1/z2 requests query western geometries.
	expandedBounds := `ST_Intersection(
		ST_Expand(
			tile.tile_bounds,
			(ST_XMax(tile.tile_bounds) - ST_XMin(tile.tile_bounds)) * 0.03125
		),
		ST_MakeEnvelope(
			-20037508.342789244, -20037508.342789244,
			 20037508.342789244,  20037508.342789244,
			3857
		)
	)`
	sourceBounds := fmt.Sprintf("ST_Transform(%s, %d)", expandedBounds, s.srid)
	geomExpr := fmt.Sprintf(
		"ST_Transform(ST_Intersection(t.%s, bounds.source_bounds), 3857)",
		quoteIdent(s.geomCol))
	if s.srid == 3857 {
		sourceBounds = expandedBounds
		geomExpr = fmt.Sprintf(
			"ST_Intersection(t.%s, bounds.source_bounds)",
			quoteIdent(s.geomCol))
	}
	mvtArgs := fmt.Sprintf("mvtgeom.*, '%s', %d, 'geom'", s.name, s.extent)
	if s.mvtIDCol != "" {
		mvtArgs += fmt.Sprintf(", %s", quoteLiteral(s.mvtIDCol))
	}
	sql := fmt.Sprintf(`
		WITH tile AS (
			SELECT ST_TileEnvelope($1,$2,$3) AS tile_bounds
		), bounds AS (
			SELECT tile.tile_bounds, %s AS source_bounds FROM tile
		), mvtgeom AS (
			SELECT ST_AsMVTGeom(%s, bounds.tile_bounds, %d, %d, true) AS geom%s
			FROM %s t CROSS JOIN bounds
			WHERE t.%s && bounds.source_bounds
		)
		SELECT ST_AsMVT(%s) FROM mvtgeom WHERE geom IS NOT NULL`,
		sourceBounds, geomExpr, s.extent, s.buffer, s.selectList("t."),
		s.relation(), quoteIdent(s.geomCol),
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
	case [16]byte:
		return fmt.Sprintf("%x-%x-%x-%x-%x",
			t[0:4], t[4:6], t[6:8], t[8:10], t[10:16])
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
