// Package config loads and validates the YAML configuration file that
// drives the server and its data sources.
package config

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Config is the root of the YAML configuration.
type Config struct {
	Server     Server     `yaml:"server"`
	Cache      Cache      `yaml:"cache"`
	Auth       Auth       `yaml:"auth"`
	MCP        MCP        `yaml:"mcp"`
	Algorithms Algorithms `yaml:"algorithms"`
	Sources    []Source   `yaml:"sources"`
	Networks   []Network  `yaml:"networks"`
}

// Algorithms toggles the algorithm endpoints (enabled by default).
type Algorithms struct {
	Enabled *bool `yaml:"enabled"`
}

// On reports whether algorithm endpoints should be served.
func (a Algorithms) On() bool { return a.Enabled == nil || *a.Enabled }

// Network declares a routable graph built from a feature source's
// LineString features, used by the routing algorithms.
type Network struct {
	Name            string  `yaml:"name"`
	Source          string  `yaml:"source"`
	DefaultSpeedKMH float64 `yaml:"default_speed_kmh"`
	SpeedField      string  `yaml:"speed_field"`
	OnewayField     string  `yaml:"oneway_field"`
	LevelField      string  `yaml:"level_field"`
	NameField       string  `yaml:"name_field"`
}

// Server holds HTTP server settings.
type Server struct {
	Host string `yaml:"host"`
	Port int    `yaml:"port"`
	// BaseURL overrides the externally visible URL used in links
	// (useful behind a reverse proxy). Empty = derive from request.
	BaseURL string        `yaml:"base_url"`
	CORS    bool          `yaml:"cors"`
	Timeout time.Duration `yaml:"timeout"`
}

// Cache holds tile cache settings (in-memory first tier plus an
// optional disk-backed second tier).
type Cache struct {
	Enabled    bool          `yaml:"enabled"`
	MaxEntries int           `yaml:"max_entries"`
	TTL        time.Duration `yaml:"ttl"`
	Disk       DiskCache     `yaml:"disk"`
}

// DiskCache holds the persistent second-tier cache settings.
type DiskCache struct {
	Enabled   bool          `yaml:"enabled"`
	Dir       string        `yaml:"dir"`
	TTL       time.Duration `yaml:"ttl"`
	MaxSizeMB int64         `yaml:"max_size_mb"`
}

// Auth holds API-key authentication settings. Keys may also be supplied
// via the GEOVERSE_API_KEYS environment variable (comma-separated), which
// is appended to api_keys at load time.
type Auth struct {
	Enabled bool     `yaml:"enabled"`
	APIKeys []string `yaml:"api_keys"`
}

// MCP holds the Model Context Protocol endpoint settings.
type MCP struct {
	Enabled bool   `yaml:"enabled"`
	Path    string `yaml:"path"`
}

// Source describes one configured data source. Fields are a union across
// source types; each type validates the subset it needs.
type Source struct {
	Name        string `yaml:"name" json:"name"`
	Type        string `yaml:"type" json:"type"` // postgis | mysql | mbtiles | pmtiles | geojson | geopackage
	Title       string `yaml:"title" json:"title,omitempty"`
	Description string `yaml:"description" json:"description,omitempty"`

	// File-backed sources.
	Path  string `yaml:"path" json:"path,omitempty"`
	Layer string `yaml:"layer" json:"layer,omitempty"` // geopackage: feature table to expose

	// Database sources (PostGIS / MySQL 8).
	DSN            string   `yaml:"dsn" json:"dsn,omitempty"`
	Table          string   `yaml:"table" json:"table,omitempty"`
	GeometryColumn string   `yaml:"geometry_column" json:"geometry_column,omitempty"`
	IDColumn       string   `yaml:"id_column" json:"id_column,omitempty"`
	SRID           int      `yaml:"srid" json:"srid,omitempty"`
	Fields         []string `yaml:"fields" json:"fields,omitempty"`

	// Tile behaviour (postgis / mysql / pmtiles / geojson / geopackage).
	MinZoom  *int          `yaml:"min_zoom" json:"min_zoom,omitempty"`
	MaxZoom  *int          `yaml:"max_zoom" json:"max_zoom,omitempty"`
	Buffer   *int          `yaml:"buffer" json:"buffer,omitempty"`     // MVT buffer in tile units, default 64
	Extent   *int          `yaml:"extent" json:"extent,omitempty"`     // MVT extent, default 4096
	Simplify *bool         `yaml:"simplify" json:"simplify,omitempty"` // zoom-dependent simplification, default true
	Cache    *bool         `yaml:"cache" json:"cache,omitempty"`       // per-source cache override
	TileTTL  time.Duration `yaml:"tile_ttl" json:"-"`                  // reserved for future per-source TTL
}

var nameRe = regexp.MustCompile(`^[A-Za-z0-9_.-]+$`)

// Load reads, parses and validates the configuration at path.
func Load(path string) (*Config, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}
	cfg := Default()
	if err := yaml.Unmarshal(raw, cfg); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}
	if env := os.Getenv("GEOVERSE_API_KEYS"); env != "" {
		for _, k := range strings.Split(env, ",") {
			if k = strings.TrimSpace(k); k != "" {
				cfg.Auth.APIKeys = append(cfg.Auth.APIKeys, k)
			}
		}
	}
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return cfg, nil
}

// SaveSources atomically replaces only the top-level sources node in an
// existing YAML file. Keeping the rest of the document byte-for-byte out of
// the in-memory Config is important: environment-provided API keys must never
// be written back to disk by the management UI.
func SaveSources(path string, sources []Source) error {
	raw, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read config for update: %w", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("stat config for update: %w", err)
	}
	var doc yaml.Node
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		return fmt.Errorf("parse config for update: %w", err)
	}
	if len(doc.Content) != 1 || doc.Content[0].Kind != yaml.MappingNode {
		return fmt.Errorf("config root must be a YAML mapping")
	}
	var replacement yaml.Node
	if err := replacement.Encode(sources); err != nil {
		return fmt.Errorf("encode sources: %w", err)
	}
	root := doc.Content[0]
	replaced := false
	for i := 0; i+1 < len(root.Content); i += 2 {
		if root.Content[i].Value == "sources" {
			root.Content[i+1] = &replacement
			replaced = true
			break
		}
	}
	if !replaced {
		root.Content = append(root.Content,
			&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: "sources"},
			&replacement,
		)
	}
	out, err := yaml.Marshal(&doc)
	if err != nil {
		return fmt.Errorf("marshal config update: %w", err)
	}
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".*.tmp")
	if err != nil {
		return fmt.Errorf("create config temp file: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err := tmp.Chmod(info.Mode().Perm()); err != nil {
		tmp.Close()
		return fmt.Errorf("secure config temp file: %w", err)
	}
	if _, err := tmp.Write(out); err != nil {
		tmp.Close()
		return fmt.Errorf("write config temp file: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return fmt.Errorf("sync config temp file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close config temp file: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("replace config: %w", err)
	}
	return nil
}

// Default returns a configuration with sensible defaults applied.
func Default() *Config {
	return &Config{
		Server: Server{
			Host:    "0.0.0.0",
			Port:    8080,
			CORS:    true,
			Timeout: 30 * time.Second,
		},
		Cache: Cache{
			Enabled:    true,
			MaxEntries: 10000,
			TTL:        5 * time.Minute,
			Disk: DiskCache{
				Enabled:   false,
				Dir:       "./tile-cache",
				TTL:       24 * time.Hour,
				MaxSizeMB: 512,
			},
		},
		MCP: MCP{
			Enabled: false,
			Path:    "/mcp",
		},
	}
}

// Validate checks the configuration for structural errors.
func (c *Config) Validate() error {
	if c.Server.Port <= 0 || c.Server.Port > 65535 {
		return fmt.Errorf("server.port %d out of range", c.Server.Port)
	}
	if c.Auth.Enabled && len(c.Auth.APIKeys) == 0 {
		return fmt.Errorf("auth.enabled is true but no api_keys configured (yaml or GEOVERSE_API_KEYS)")
	}
	if c.Cache.Disk.Enabled && c.Cache.Disk.Dir == "" {
		return fmt.Errorf("cache.disk.enabled is true but cache.disk.dir is empty")
	}
	if c.MCP.Enabled && !strings.HasPrefix(c.MCP.Path, "/") {
		return fmt.Errorf("mcp.path must start with '/'")
	}
	if len(c.Sources) == 0 {
		return fmt.Errorf("no sources configured")
	}
	seen := map[string]bool{}
	for i, s := range c.Sources {
		if s.Name == "" {
			return fmt.Errorf("sources[%d]: name is required", i)
		}
		if !nameRe.MatchString(s.Name) {
			return fmt.Errorf("sources[%d]: name %q may only contain letters, digits, '_', '-', '.'", i, s.Name)
		}
		if seen[s.Name] {
			return fmt.Errorf("sources[%d]: duplicate name %q", i, s.Name)
		}
		seen[s.Name] = true
		switch s.Type {
		case "postgis", "mysql":
			if s.DSN == "" || s.Table == "" {
				return fmt.Errorf("source %q: %s requires dsn and table", s.Name, s.Type)
			}
			if s.Type == "mysql" && !strings.HasPrefix(s.DSN, "mysql://") {
				return fmt.Errorf("source %q: mysql dsn must use mysql://", s.Name)
			}
		case "mbtiles", "pmtiles", "geojson", "geopackage":
			if s.Path == "" {
				return fmt.Errorf("source %q: %s requires path", s.Name, s.Type)
			}
		default:
			return fmt.Errorf("source %q: unknown type %q (want postgis, mysql, mbtiles, pmtiles, geojson or geopackage)", s.Name, s.Type)
		}
		if s.MinZoom != nil && s.MaxZoom != nil && *s.MinZoom > *s.MaxZoom {
			return fmt.Errorf("source %q: min_zoom > max_zoom", s.Name)
		}
	}
	netSeen := map[string]bool{}
	for i, n := range c.Networks {
		if n.Name == "" || !nameRe.MatchString(n.Name) {
			return fmt.Errorf("networks[%d]: invalid name %q", i, n.Name)
		}
		if netSeen[n.Name] {
			return fmt.Errorf("networks[%d]: duplicate name %q", i, n.Name)
		}
		netSeen[n.Name] = true
		if !seen[n.Source] {
			return fmt.Errorf("network %q: source %q is not a configured source", n.Name, n.Source)
		}
	}
	return nil
}
