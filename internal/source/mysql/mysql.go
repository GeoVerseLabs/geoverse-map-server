// Package mysql serves MySQL 8 spatial tables as dynamic vector tiles and
// OGC API - Features collections. Spatial filtering is pushed down to MySQL;
// the matching rows for one tile are encoded with the shared memory engine.
package mysql

import (
	"context"
	"database/sql"
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"
	"time"

	mysqldriver "github.com/go-sql-driver/mysql"
	"github.com/paulmach/orb"
	"github.com/paulmach/orb/geojson"
	"github.com/paulmach/orb/maptile"

	"github.com/GeoVerseLabs/geoverse-map-server/internal/config"
	"github.com/GeoVerseLabs/geoverse-map-server/internal/source"
	"github.com/GeoVerseLabs/geoverse-map-server/internal/source/memengine"
)

const maxTileFeatures = 50000

type fieldInfo struct {
	name     string
	jsonType string
}

// Source serves one MySQL spatial table.
type Source struct {
	name     string
	db       *sql.DB
	schema   string
	table    string
	geomCol  string
	idCol    string
	srid     int
	fields   []fieldInfo
	info     source.TileInfo
	simplify bool
	mariaDB  bool
}

var (
	_ source.Source        = (*Source)(nil)
	_ source.TileSource    = (*Source)(nil)
	_ source.FeatureSource = (*Source)(nil)
)

// New connects to MySQL and introspects the configured spatial table.
func New(ctx context.Context, cfg config.Source) (*Source, error) {
	driverCfg, err := parseURLDSN(cfg.DSN)
	if err != nil {
		return nil, fmt.Errorf("source %q: %w", cfg.Name, err)
	}
	connector, err := mysqldriver.NewConnector(driverCfg)
	if err != nil {
		return nil, fmt.Errorf("source %q: connector: %w", cfg.Name, err)
	}
	db := sql.OpenDB(connector)
	db.SetConnMaxLifetime(3 * time.Minute)
	db.SetMaxOpenConns(10)
	db.SetMaxIdleConns(5)
	if err := db.PingContext(ctx); err != nil {
		db.Close()
		return nil, fmt.Errorf("source %q: connect: %w", cfg.Name, err)
	}
	var serverVersion string
	if err := db.QueryRowContext(ctx, "SELECT VERSION()").Scan(&serverVersion); err != nil {
		db.Close()
		return nil, fmt.Errorf("source %q: detect server version: %w", cfg.Name, err)
	}

	schema, table := splitTable(cfg.Table, driverCfg.DBName)
	s := &Source{
		name:     cfg.Name,
		db:       db,
		schema:   schema,
		table:    table,
		geomCol:  firstNonEmpty(cfg.GeometryColumn, "geom"),
		idCol:    cfg.IDColumn,
		srid:     cfg.SRID,
		simplify: cfg.Simplify == nil || *cfg.Simplify,
		mariaDB:  strings.Contains(strings.ToLower(serverVersion), "mariadb"),
	}
	if err := s.introspect(ctx, cfg); err != nil {
		db.Close()
		return nil, fmt.Errorf("source %q: %w", cfg.Name, err)
	}
	return s, nil
}

// parseURLDSN turns the redaction-friendly mysql:// URL used by config and the
// WebUI into the driver's native DSN without ever logging the password.
func parseURLDSN(raw string) (*mysqldriver.Config, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("parse mysql dsn: %w", err)
	}
	if u.Scheme != "mysql" {
		return nil, fmt.Errorf("mysql dsn must use mysql://")
	}
	if u.Hostname() == "" {
		return nil, fmt.Errorf("mysql dsn requires a host")
	}
	dbName, err := url.PathUnescape(strings.TrimPrefix(u.EscapedPath(), "/"))
	if err != nil || dbName == "" {
		return nil, fmt.Errorf("mysql dsn requires a database")
	}
	addr := u.Host
	if u.Port() == "" {
		addr = net.JoinHostPort(u.Hostname(), "3306")
	}
	cfg := mysqldriver.NewConfig()
	cfg.Net = "tcp"
	cfg.Addr = addr
	cfg.DBName = dbName
	cfg.ParseTime = true
	cfg.Loc = time.UTC
	cfg.Collation = "utf8mb4_unicode_ci"
	if u.User != nil {
		cfg.User = u.User.Username()
		cfg.Passwd, _ = u.User.Password()
	}
	query := u.Query()
	cfg.TLSConfig = query.Get("tls")
	for key, target := range map[string]*time.Duration{
		"timeout":       &cfg.Timeout,
		"read_timeout":  &cfg.ReadTimeout,
		"write_timeout": &cfg.WriteTimeout,
	} {
		if rawDuration := query.Get(key); rawDuration != "" {
			duration, err := time.ParseDuration(rawDuration)
			if err != nil {
				return nil, fmt.Errorf("mysql dsn %s: %w", key, err)
			}
			*target = duration
		}
	}
	return cfg, nil
}

func splitTable(raw, defaultSchema string) (schema, table string) {
	parts := strings.SplitN(raw, ".", 2)
	if len(parts) == 2 {
		return parts[0], parts[1]
	}
	return defaultSchema, raw
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

func quoteIdent(value string) string {
	return "`" + strings.ReplaceAll(value, "`", "``") + "`"
}

func (s *Source) relation() string {
	return quoteIdent(s.schema) + "." + quoteIdent(s.table)
}

func (s *Source) introspect(ctx context.Context, cfg config.Source) error {
	rows, err := s.db.QueryContext(ctx, `
		SELECT COLUMN_NAME, DATA_TYPE, COLUMN_KEY
		FROM INFORMATION_SCHEMA.COLUMNS
		WHERE TABLE_SCHEMA = ? AND TABLE_NAME = ?
		ORDER BY ORDINAL_POSITION`, s.schema, s.table)
	if err != nil {
		return fmt.Errorf("introspect columns: %w", err)
	}
	defer rows.Close()

	type column struct {
		name, dataType, key string
	}
	var columns []column
	geometryFound := false
	for rows.Next() {
		var item column
		if err := rows.Scan(&item.name, &item.dataType, &item.key); err != nil {
			return fmt.Errorf("scan columns: %w", err)
		}
		columns = append(columns, item)
		if item.name == s.geomCol && isGeometryType(item.dataType) {
			geometryFound = true
		}
		if s.idCol == "" && item.key == "PRI" {
			s.idCol = item.name
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("introspect columns: %w", err)
	}
	if len(columns) == 0 {
		return fmt.Errorf("table %s does not exist or has no visible columns", s.relation())
	}
	if !geometryFound {
		return fmt.Errorf("geometry column %q was not found in %s", s.geomCol, s.relation())
	}

	wanted := map[string]bool{}
	for _, field := range cfg.Fields {
		wanted[field] = true
	}
	if s.idCol != "" {
		wanted[s.idCol] = true
	}
	for _, item := range columns {
		if item.name == s.geomCol || isGeometryType(item.dataType) {
			continue
		}
		if len(cfg.Fields) > 0 && !wanted[item.name] {
			continue
		}
		s.fields = append(s.fields, fieldInfo{
			name: item.name, jsonType: mysqlJSONType(item.dataType),
		})
	}

	if s.srid == 0 {
		var srid sql.NullInt64
		query := fmt.Sprintf(
			"SELECT ST_SRID(%s) FROM %s WHERE %s IS NOT NULL LIMIT 1",
			quoteIdent(s.geomCol), s.relation(), quoteIdent(s.geomCol))
		if err := s.db.QueryRowContext(ctx, query).Scan(&srid); err != nil && err != sql.ErrNoRows {
			return fmt.Errorf("discover srid: %w", err)
		}
		if srid.Valid {
			s.srid = int(srid.Int64)
		}
	}
	if s.mariaDB && s.srid != 0 && s.srid != 4326 {
		return fmt.Errorf(
			"MariaDB source SRID %d is unsupported because MariaDB does not provide ST_Transform; use EPSG:4326 data",
			s.srid)
	}

	minZoom, maxZoom := 0, 22
	if cfg.MinZoom != nil {
		minZoom = *cfg.MinZoom
	}
	if cfg.MaxZoom != nil {
		maxZoom = *cfg.MaxZoom
	}
	fields := make(map[string]string, len(s.fields))
	for _, field := range s.fields {
		fields[field.name] = field.jsonType
	}
	s.info = source.TileInfo{
		Name:        cfg.Name,
		Title:       firstNonEmpty(cfg.Title, s.table),
		Description: cfg.Description,
		Format:      source.FormatMVT,
		MinZoom:     minZoom,
		MaxZoom:     maxZoom,
		Bounds:      [4]float64{-180, -85.05112877980659, 180, 85.05112877980659},
		Center:      [3]float64{0, 0, float64(minZoom)},
		VectorLayers: []source.VectorLayer{{
			ID: cfg.Name, Fields: fields,
		}},
		Gzipped:   true,
		Cacheable: true,
	}
	return nil
}

func isGeometryType(dataType string) bool {
	switch strings.ToLower(dataType) {
	case "geometry", "point", "linestring", "polygon", "multipoint",
		"multilinestring", "multipolygon", "geometrycollection":
		return true
	default:
		return false
	}
}

func mysqlJSONType(dataType string) string {
	switch strings.ToLower(dataType) {
	case "tinyint", "smallint", "mediumint", "int", "integer", "bigint",
		"decimal", "numeric", "float", "double", "real", "year":
		return "Number"
	case "bit", "boolean", "bool":
		return "Boolean"
	default:
		return "String"
	}
}

func (s *Source) geometry4326() string {
	geom := "t." + quoteIdent(s.geomCol)
	switch s.srid {
	case 0:
		if s.mariaDB {
			return geom
		}
		return fmt.Sprintf("ST_SRID(%s, 4326)", geom)
	case 4326:
		return geom
	default:
		return fmt.Sprintf("ST_Transform(%s, 4326)", geom)
	}
}

func (s *Source) bboxGeometry() string {
	switch s.srid {
	case 0:
		return "ST_GeomFromText(?, 0)"
	case 4326:
		if s.mariaDB {
			return "ST_GeomFromText(?, 4326)"
		}
		return "ST_GeomFromText(?, 4326, 'axis-order=long-lat')"
	default:
		return fmt.Sprintf(
			"ST_Transform(ST_GeomFromText(?, 4326, 'axis-order=long-lat'), %d)",
			s.srid)
	}
}

func boundWKT(bound orb.Bound) string {
	return "POLYGON((" +
		strconv.FormatFloat(bound.Min[0], 'f', -1, 64) + " " +
		strconv.FormatFloat(bound.Min[1], 'f', -1, 64) + "," +
		strconv.FormatFloat(bound.Max[0], 'f', -1, 64) + " " +
		strconv.FormatFloat(bound.Min[1], 'f', -1, 64) + "," +
		strconv.FormatFloat(bound.Max[0], 'f', -1, 64) + " " +
		strconv.FormatFloat(bound.Max[1], 'f', -1, 64) + "," +
		strconv.FormatFloat(bound.Min[0], 'f', -1, 64) + " " +
		strconv.FormatFloat(bound.Max[1], 'f', -1, 64) + "," +
		strconv.FormatFloat(bound.Min[0], 'f', -1, 64) + " " +
		strconv.FormatFloat(bound.Min[1], 'f', -1, 64) + "))"
}

func clampWGS84Bound(bound orb.Bound) orb.Bound {
	const maxMercatorLatitude = 85.05112877980659
	const antimeridianEpsilon = 1e-9
	bound.Min[0] = max(-180+antimeridianEpsilon, bound.Min[0])
	bound.Max[0] = min(180-antimeridianEpsilon, bound.Max[0])
	bound.Min[1] = max(-maxMercatorLatitude, bound.Min[1])
	bound.Max[1] = min(maxMercatorLatitude, bound.Max[1])
	return bound
}

func (s *Source) selectList() string {
	var builder strings.Builder
	builder.WriteString("ST_AsGeoJSON(")
	builder.WriteString(s.geometry4326())
	builder.WriteString(", 10, 0)")
	for _, field := range s.fields {
		builder.WriteString(", t.")
		builder.WriteString(quoteIdent(field.name))
	}
	return builder.String()
}

func (s *Source) queryFeatures(
	ctx context.Context,
	where string,
	args []interface{},
	limit, offset int,
) ([]*geojson.Feature, error) {
	orderBy := ""
	if s.idCol != "" {
		orderBy = " ORDER BY t." + quoteIdent(s.idCol)
	}
	query := fmt.Sprintf(
		"SELECT %s FROM %s t WHERE %s%s LIMIT %d OFFSET %d",
		s.selectList(), s.relation(), where, orderBy, limit, offset)
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("query features: %w", err)
	}
	defer rows.Close()
	var features []*geojson.Feature
	for rows.Next() {
		feature, err := s.scanFeature(rows)
		if err != nil {
			return nil, err
		}
		features = append(features, feature)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("query features: %w", err)
	}
	return features, nil
}

func (s *Source) scanFeature(rows *sql.Rows) (*geojson.Feature, error) {
	values := make([]interface{}, len(s.fields)+1)
	destinations := make([]interface{}, len(values))
	for i := range values {
		destinations[i] = &values[i]
	}
	if err := rows.Scan(destinations...); err != nil {
		return nil, fmt.Errorf("scan feature: %w", err)
	}
	geometryJSON := normalizeString(values[0])
	geometry, err := geojson.UnmarshalGeometry([]byte(geometryJSON))
	if err != nil {
		return nil, fmt.Errorf("parse geometry: %w", err)
	}
	feature := geojson.NewFeature(geometry.Geometry())
	for i, field := range s.fields {
		value := normalizeValue(values[i+1])
		if value == nil {
			continue
		}
		if field.name == s.idCol {
			feature.ID = value
		}
		feature.Properties[field.name] = value
	}
	return feature, nil
}

func normalizeString(value interface{}) string {
	switch typed := value.(type) {
	case []byte:
		return string(typed)
	case string:
		return typed
	default:
		return fmt.Sprint(typed)
	}
}

func normalizeValue(value interface{}) interface{} {
	switch typed := value.(type) {
	case []byte:
		return string(typed)
	case time.Time:
		return typed.UTC().Format(time.RFC3339Nano)
	default:
		return value
	}
}

// Tile implements source.TileSource.
func (s *Source) Tile(ctx context.Context, z, x, y uint32) ([]byte, error) {
	tile := maptile.New(x, y, maptile.Zoom(z))
	// maptile's selection buffer intentionally extends beyond the tile. At
	// the antimeridian this can exceed the legal EPSG:4326 longitude range,
	// which MySQL 8 rejects before MBRIntersects can run (notably z0-z2 for
	// data in the easternmost tile). Clamp only the database query envelope;
	// the MVT encoder still clips against the original tile.
	queryBound := clampWGS84Bound(tile.Bound(0.1))
	where := fmt.Sprintf("t.%s IS NOT NULL", quoteIdent(s.geomCol))
	var args []interface{}
	// MySQL interprets geographic polygons wider than a hemisphere as their
	// smaller spherical complement. At z0/z1 the buffered tile is wider than
	// 180 degrees, so an MBR predicate would incorrectly exclude ordinary
	// features. The existing candidate cap remains the safety guard there.
	if queryBound.Max[0]-queryBound.Min[0] < 180 {
		where += fmt.Sprintf(
			" AND MBRIntersects(t.%s, %s)",
			quoteIdent(s.geomCol), s.bboxGeometry())
		args = append(args, boundWKT(queryBound))
	}
	features, err := s.queryFeatures(
		ctx, where, args, maxTileFeatures+1, 0)
	if err != nil {
		return nil, err
	}
	if len(features) == 0 {
		return nil, source.ErrTileNotFound
	}
	if len(features) > maxTileFeatures {
		return nil, fmt.Errorf(
			"tile %d/%d/%d matched more than %d features; raise the zoom or pre-tile the dataset",
			z, x, y, maxTileFeatures)
	}
	engine, err := memengine.New(memengine.Options{
		Name: s.name, Title: s.info.Title, Description: s.info.Description,
		MinZoom: s.info.MinZoom, MaxZoom: s.info.MaxZoom, Simplify: s.simplify,
	}, features)
	if err != nil {
		return nil, err
	}
	return engine.Tile(ctx, z, x, y)
}

// TileInfo implements source.TileSource.
func (s *Source) TileInfo() source.TileInfo { return s.info }

// Features implements source.FeatureSource.
func (s *Source) Features(ctx context.Context, query source.FeatureQuery) (*source.FeatureResult, error) {
	where := "t." + quoteIdent(s.geomCol) + " IS NOT NULL"
	var args []interface{}
	if query.BBox != nil {
		bound := orb.Bound{
			Min: orb.Point{query.BBox[0], query.BBox[1]},
			Max: orb.Point{query.BBox[2], query.BBox[3]},
		}
		where += fmt.Sprintf(
			" AND MBRIntersects(t.%s, %s)",
			quoteIdent(s.geomCol), s.bboxGeometry())
		args = append(args, boundWKT(bound))
	}
	var total int
	countSQL := fmt.Sprintf("SELECT COUNT(*) FROM %s t WHERE %s", s.relation(), where)
	if err := s.db.QueryRowContext(ctx, countSQL, args...).Scan(&total); err != nil {
		return nil, fmt.Errorf("count features: %w", err)
	}
	limit := query.Limit
	if limit <= 0 {
		limit = 10
	}
	offset := query.Offset
	if offset < 0 {
		offset = 0
	}
	features, err := s.queryFeatures(ctx, where, args, limit, offset)
	if err != nil {
		return nil, err
	}
	return &source.FeatureResult{Features: features, NumberMatched: total}, nil
}

// Feature implements source.FeatureSource.
func (s *Source) Feature(ctx context.Context, id string) (*geojson.Feature, error) {
	if s.idCol == "" {
		return nil, fmt.Errorf("source %q has no id column configured", s.name)
	}
	where := fmt.Sprintf(
		"t.%s IS NOT NULL AND CAST(t.%s AS CHAR) = ?",
		quoteIdent(s.geomCol), quoteIdent(s.idCol))
	features, err := s.queryFeatures(ctx, where, []interface{}{id}, 1, 0)
	if err != nil {
		return nil, err
	}
	if len(features) == 0 {
		return nil, source.ErrFeatureNotFound
	}
	return features[0], nil
}

// CollectionInfo implements source.FeatureSource.
func (s *Source) CollectionInfo() source.CollectionInfo {
	return source.CollectionInfo{
		Name: s.info.Name, Title: s.info.Title,
		Description: s.info.Description, Bounds: s.info.Bounds,
	}
}

// Name implements source.Source.
func (s *Source) Name() string { return s.name }

// Ping implements source.Source.
func (s *Source) Ping(ctx context.Context) error { return s.db.PingContext(ctx) }

// Close implements source.Source.
func (s *Source) Close() error { return s.db.Close() }
