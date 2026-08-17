package diagnostics

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const tinyGeoJSON = `{"type":"FeatureCollection","features":[{"type":"Feature","properties":{"name":"A"},"geometry":{"type":"Point","coordinates":[0,0]}}]}`

func diagnosticConfig(t *testing.T, extra string) string {
	t.Helper()
	dir := t.TempDir()
	dataDir := filepath.Join(dir, "data")
	if err := os.Mkdir(dataDir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dataDir, "cities.geojson")
	if err := os.WriteFile(path, []byte(tinyGeoJSON), 0o644); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(dir, "config.yaml")
	body := "assets:\n  root: " + filepath.ToSlash(dataDir) + "\n  enforce_root: true\n  max_file_size_mb: 1\n  max_memory_file_size_mb: 1\nsources:\n  - name: cities\n    type: geojson\n    path: " + filepath.ToSlash(path) + "\n" + extra
	if err := os.WriteFile(configPath, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return configPath
}

func TestDoctorAndInspect(t *testing.T) {
	path := diagnosticConfig(t, "")
	doctor := Doctor(context.Background(), path)
	if doctor.Status != "ok" || doctor.ExitCode() != 0 || len(doctor.Sources) != 1 {
		t.Fatalf("doctor report = %+v", doctor)
	}
	src := doctor.Sources[0]
	if src.Status != "ok" || src.Asset == nil || src.Tile == nil || src.Feature == nil {
		t.Fatalf("source report = %+v", src)
	}
	inspect := Inspect(context.Background(), path, "cities")
	if inspect.Status != "ok" || len(inspect.Sources) != 1 {
		t.Fatalf("inspect report = %+v", inspect)
	}
	missing := Inspect(context.Background(), path, "missing")
	if missing.ExitCode() != 1 || missing.Findings[0].Code != "source.not_found" {
		t.Fatalf("missing report = %+v", missing)
	}
}

func TestInspectDoesNotOpenUnselectedSources(t *testing.T) {
	path := diagnosticConfig(t, "  - name: unreachable\n    type: postgis\n    dsn: postgres://reader:secret@127.0.0.1:1/gis\n    table: public.roads\n")
	report := Inspect(context.Background(), path, "cities")
	if report.Status != "ok" || report.ExitCode() != 0 || len(report.Sources) != 1 {
		t.Fatalf("selected source inspection opened another source: %+v", report)
	}
	if report.Sources[0].Name != "cities" {
		t.Fatalf("inspected source = %q, want cities", report.Sources[0].Name)
	}
}

func TestDoctorCompatibilityWarningsDoNotFail(t *testing.T) {
	dir := t.TempDir()
	asset := filepath.Join(dir, "cities.geojson")
	if err := os.WriteFile(asset, []byte(tinyGeoJSON), 0o644); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(dir, "config.yaml")
	body := "sources:\n  - name: cities\n    type: geojson\n    path: " + filepath.ToSlash(asset) + "\n"
	if err := os.WriteFile(configPath, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	report := Doctor(context.Background(), configPath)
	if report.Status != "warning" || report.ExitCode() != 0 || len(report.Findings) != 3 {
		t.Fatalf("compatibility report = %+v", report)
	}
}

func TestDiagnosticOutputLeaksNoDSNPassword(t *testing.T) {
	path := diagnosticConfig(t, "  - name: db\n    type: postgis\n    dsn: postgres://reader:topsecret@127.0.0.1:1/gis\n    table: public.roads\n")
	report := Inspect(context.Background(), path, "db")
	if report.ExitCode() != 1 {
		t.Fatalf("expected unreachable db: %+v", report)
	}
	for _, format := range []string{"text", "json"} {
		var out bytes.Buffer
		if err := Write(&out, report, format); err != nil {
			t.Fatal(err)
		}
		if strings.Contains(out.String(), "topsecret") {
			t.Fatalf("%s output leaked password: %s", format, out.String())
		}
	}
}
