package server

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/GeoVerseLabs/geoverse-map-server/internal/config"
	"github.com/GeoVerseLabs/geoverse-map-server/internal/source/registry"
)

func authedServer(t *testing.T) *httptest.Server {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "cities.geojson")
	if err := os.WriteFile(path, []byte(testGeoJSON), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg := config.Default()
	cfg.Sources = []config.Source{{Name: "cities", Type: "geojson", Path: path}}
	cfg.Auth = config.Auth{Enabled: true, APIKeys: []string{"sekrit", "other-key"}}
	reg, err := registry.Build(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(reg.Close)
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	ts := httptest.NewServer(New(cfg, reg, nil, log).Handler())
	t.Cleanup(ts.Close)
	return ts
}

func status(t *testing.T, req *http.Request) int {
	t.Helper()
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	return resp.StatusCode
}

func TestAuthRejectsAnonymous(t *testing.T) {
	ts := authedServer(t)
	req, _ := http.NewRequest("GET", ts.URL+"/catalog", nil)
	if got := status(t, req); got != http.StatusUnauthorized {
		t.Errorf("anonymous: status = %d, want 401", got)
	}
	req, _ = http.NewRequest("GET", ts.URL+"/catalog", nil)
	req.Header.Set("X-API-Key", "wrong")
	if got := status(t, req); got != http.StatusUnauthorized {
		t.Errorf("wrong key: status = %d, want 401", got)
	}
}

func TestAuthAcceptsAllCredentialStyles(t *testing.T) {
	ts := authedServer(t)

	bearer, _ := http.NewRequest("GET", ts.URL+"/catalog", nil)
	bearer.Header.Set("Authorization", "Bearer sekrit")
	if got := status(t, bearer); got != http.StatusOK {
		t.Errorf("bearer: status = %d", got)
	}

	header, _ := http.NewRequest("GET", ts.URL+"/catalog", nil)
	header.Header.Set("X-API-Key", "other-key")
	if got := status(t, header); got != http.StatusOK {
		t.Errorf("x-api-key: status = %d", got)
	}

	query, _ := http.NewRequest("GET", ts.URL+"/tiles/cities/6/52/24.pbf?api_key=sekrit", nil)
	if got := status(t, query); got != http.StatusOK {
		t.Errorf("query param: status = %d", got)
	}
}

func TestAuthExemptsHealth(t *testing.T) {
	ts := authedServer(t)
	req, _ := http.NewRequest("GET", ts.URL+"/health", nil)
	if got := status(t, req); got != http.StatusOK {
		t.Errorf("health: status = %d, want 200 without credentials", got)
	}
}
