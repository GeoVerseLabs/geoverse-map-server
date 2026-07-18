// Package config loads and validates the YAML configuration file that
// drives the server and its data sources.
package config

import (
	"fmt"
	"os"
	"regexp"
	"time"

	"gopkg.in/yaml.v3"
)

// Config is the root of the YAML configuration.
type Config struct {
	Server  Server   `yaml:"server"`
	Cache   Cache    `yaml:"cache"`
	Sources []Source `yaml:"sources"`
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

// Cache holds tile cache settings.
type Cache struct {
	Enabled    bool          `yaml:"enabled"`
	MaxEntries int           `yaml:"max_entries"`
	TTL        time.Duration `yaml:"ttl"`
}

// Source describes one configured data source. Fields are a union across
// source types; each type validates the subset it needs.
type Source struct {
	Name        string `yaml:"name"`
	Type        string `yaml:"type"` // postgis | mbtiles | geojson | geopackage
	Title       string `yaml:"title"`
	Description string `yaml:"description"`

	// File-backed sources.
	Path  string `yaml:"path"`
	Layer string `yaml:"layer"` // geopackage: feature table to expose

	// PostGIS.
	DSN            string   `yaml:"dsn"`
	Table          string   `yaml:"table"`
	GeometryColumn string   `yaml:"geometry_column"`
	IDColumn       string   `yaml:"id_column"`
	SRID           int      `yaml:"srid"`
	Fields         []string `yaml:"fields"`

	// Tile behaviour (postgis / geojson / geopackage).
	MinZoom  *int          `yaml:"min_zoom"`
	MaxZoom  *int          `yaml:"max_zoom"`
	Buffer   *int          `yaml:"buffer"`   // MVT buffer in tile units, default 64
	Extent   *int          `yaml:"extent"`   // MVT extent, default 4096
	Simplify *bool         `yaml:"simplify"` // zoom-dependent simplification, default true
	Cache    *bool         `yaml:"cache"`    // per-source cache override
	TileTTL  time.Duration `yaml:"tile_ttl"` // reserved for future per-source TTL
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
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return cfg, nil
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
		},
	}
}

// Validate checks the configuration for structural errors.
func (c *Config) Validate() error {
	if c.Server.Port <= 0 || c.Server.Port > 65535 {
		return fmt.Errorf("server.port %d out of range", c.Server.Port)
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
		case "postgis":
			if s.DSN == "" || s.Table == "" {
				return fmt.Errorf("source %q: postgis requires dsn and table", s.Name)
			}
		case "mbtiles", "geojson", "geopackage":
			if s.Path == "" {
				return fmt.Errorf("source %q: %s requires path", s.Name, s.Type)
			}
		default:
			return fmt.Errorf("source %q: unknown type %q (want postgis, mbtiles, geojson or geopackage)", s.Name, s.Type)
		}
		if s.MinZoom != nil && s.MaxZoom != nil && *s.MinZoom > *s.MaxZoom {
			return fmt.Errorf("source %q: min_zoom > max_zoom", s.Name)
		}
	}
	return nil
}
