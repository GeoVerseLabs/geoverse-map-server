package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func cliFixture(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	asset := filepath.Join(dir, "cities.geojson")
	body := `{"type":"FeatureCollection","features":[{"type":"Feature","properties":{},"geometry":{"type":"Point","coordinates":[0,0]}}]}`
	if err := os.WriteFile(asset, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(dir, "config.yaml")
	configBody := "assets:\n  root: " + filepath.ToSlash(dir) + "\n  enforce_root: true\n  max_file_size_mb: 1\n  max_memory_file_size_mb: 1\nsources:\n  - name: cities\n    type: geojson\n    path: " + filepath.ToSlash(asset) + "\n"
	if err := os.WriteFile(configPath, []byte(configBody), 0o644); err != nil {
		t.Fatal(err)
	}
	return configPath
}

func TestRunCLIDiagnosticModes(t *testing.T) {
	path := cliFixture(t)
	for _, tc := range []struct {
		args []string
		want int
		mark string
	}{
		{[]string{"-config", path, "-doctor"}, 0, "geoverse doctor: ok"},
		{[]string{"-config", path, "-inspect", "cities", "-format", "json"}, 0, `"schemaVersion": 1`},
		{[]string{"-config", path, "-inspect", "missing"}, 1, "source.not_found"},
	} {
		var stdout, stderr bytes.Buffer
		if got := runCLI(tc.args, &stdout, &stderr); got != tc.want {
			t.Fatalf("runCLI(%v)=%d, want %d; stderr=%s", tc.args, got, tc.want, stderr.String())
		}
		if !strings.Contains(stdout.String(), tc.mark) {
			t.Fatalf("runCLI(%v) output missing %q: %s", tc.args, tc.mark, stdout.String())
		}
	}
}

func TestRunCLIUsageErrors(t *testing.T) {
	for _, args := range [][]string{
		{"-doctor", "-validate"},
		{"-doctor", "-format", "yaml"},
		{"-format", "json"},
		{"unexpected"},
	} {
		var stdout, stderr bytes.Buffer
		if got := runCLI(args, &stdout, &stderr); got != 2 {
			t.Fatalf("runCLI(%v)=%d, want 2", args, got)
		}
	}
}
