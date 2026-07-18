package config

import (
	"os"
	"path/filepath"
	"testing"
)

func writeConfig(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLoadValid(t *testing.T) {
	path := writeConfig(t, `
server:
  port: 9090
cache:
  ttl: 2m
sources:
  - name: pois
    type: geojson
    path: ./pois.geojson
  - name: roads
    type: postgis
    dsn: postgres://localhost/gis
    table: public.roads
`)
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Server.Port != 9090 {
		t.Errorf("port = %d", cfg.Server.Port)
	}
	if cfg.Server.Host != "0.0.0.0" {
		t.Errorf("default host lost: %q", cfg.Server.Host)
	}
	if cfg.Cache.TTL.Minutes() != 2 {
		t.Errorf("ttl = %v", cfg.Cache.TTL)
	}
	if len(cfg.Sources) != 2 {
		t.Errorf("sources = %d", len(cfg.Sources))
	}
}

func TestValidationErrors(t *testing.T) {
	cases := map[string]string{
		"no sources": `
server: {port: 8080}
`,
		"duplicate names": `
sources:
  - {name: a, type: geojson, path: x.geojson}
  - {name: a, type: geojson, path: y.geojson}
`,
		"bad type": `
sources:
  - {name: a, type: shapefile, path: x.shp}
`,
		"postgis missing table": `
sources:
  - {name: a, type: postgis, dsn: "postgres://x"}
`,
		"bad name chars": `
sources:
  - {name: "a/b", type: geojson, path: x.geojson}
`,
		"zoom inverted": `
sources:
  - {name: a, type: geojson, path: x.geojson, min_zoom: 10, max_zoom: 2}
`,
	}
	for label, body := range cases {
		if _, err := Load(writeConfig(t, body)); err == nil {
			t.Errorf("%s: expected validation error", label)
		}
	}
}
