package config

import (
	"os"
	"path/filepath"
	"strings"
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

func TestSaveSourcesReplacesOnlySourceDocument(t *testing.T) {
	path := writeConfig(t, `
# keep this operator comment
server:
  port: 8080
auth:
  enabled: false
  api_keys: [yaml-only-key]
sources:
  - {name: old, type: geojson, path: old.geojson}
`)
	t.Setenv("GEOVERSE_API_KEYS", "environment-secret")
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := SaveSources(path, []Source{{
		Name: "archive", Type: "pmtiles", Path: "./data/base.pmtiles",
		AssetPolicy: Assets{Root: "/must-not-persist", EnforceRoot: true},
	}}); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	if strings.Contains(text, "environment-secret") {
		t.Fatal("environment API key leaked into persisted YAML")
	}
	if strings.Contains(text, "must-not-persist") || strings.Contains(text, "asset_policy") {
		t.Fatal("runtime asset policy leaked into persisted YAML")
	}
	if !strings.Contains(text, "keep this operator comment") {
		t.Fatal("operator comment was discarded")
	}
	updated, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(updated.Sources) != 1 || updated.Sources[0].Type != "pmtiles" {
		t.Fatalf("sources not replaced: %+v", updated.Sources)
	}
	if len(cfg.Auth.APIKeys) != 2 {
		t.Fatalf("test setup did not merge env key: %+v", cfg.Auth.APIKeys)
	}
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
		"mysql missing table": `
sources:
  - {name: a, type: mysql, dsn: "mysql://reader@localhost/geoverse"}
`,
		"mysql native driver dsn rejected": `
sources:
  - {name: a, type: mysql, dsn: "reader@tcp(localhost:3306)/geoverse", table: roads}
`,
		"pmtiles missing path": `
sources:
  - {name: archive, type: pmtiles}
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
