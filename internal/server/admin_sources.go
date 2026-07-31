package server

import (
	"context"
	"embed"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/GeoVerseLabs/geoverse-map-server/internal/config"
	"github.com/GeoVerseLabs/geoverse-map-server/internal/source/registry"
)

//go:embed webui/index.html webui/app.css webui/app.js
var webUI embed.FS

func (s *Server) handleWebUI(w http.ResponseWriter, r *http.Request) {
	name := "index.html"
	contentType := "text/html; charset=utf-8"
	switch r.URL.Path {
	case "/admin/app.css":
		name = "app.css"
		contentType = "text/css; charset=utf-8"
	case "/admin/app.js":
		name = "app.js"
		contentType = "text/javascript; charset=utf-8"
	}
	body, err := webUI.ReadFile("webui/" + name)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "embedded WebUI is unavailable")
		return
	}
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Cache-Control", "no-cache")
	_, _ = w.Write(body)
}

type sourceView struct {
	config.Source
	DSNHint      string `json:"dsn_hint,omitempty"`
	Status       string `json:"status"`
	StatusDetail string `json:"status_detail,omitempty"`
	Tile         bool   `json:"tile"`
	Feature      bool   `json:"feature"`
	Archive      string `json:"archive,omitempty"`
}

func (s *Server) handleListSources(w http.ResponseWriter, r *http.Request) {
	_, statuses := s.snapshot(r.Context())
	s.configMu.Lock()
	configured := append([]config.Source(nil), s.cfg.Sources...)
	s.configMu.Unlock()

	out := make([]sourceView, 0, len(configured))
	for _, sc := range configured {
		view := sourceView{Source: sc, Status: "ok"}
		view.DSNHint = redactDSN(sc.DSN)
		view.DSN = ""
		if status := statuses[sc.Name]; status != "" && status != "ok" {
			view.Status = "error"
			view.StatusDetail = status
		}
		_, view.Tile = s.reg.TileSource(sc.Name)
		_, view.Feature = s.reg.FeatureSource(sc.Name)
		if sc.Type == "pmtiles" {
			view.Archive = fmt.Sprintf("%s/archives/%s.pmtiles", s.baseURL(r), sc.Name)
		}
		out = append(out, view)
	}
	writeJSON(w, http.StatusOK, "application/json", map[string]interface{}{
		"sources":  out,
		"writable": configWritable(s.configPath),
		"types":    []string{"postgis", "mysql", "geojson", "mbtiles", "pmtiles", "geopackage"},
	})
}

func redactDSN(raw string) string {
	if raw == "" {
		return ""
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme == "" {
		return "已配置（凭据已隐藏）"
	}
	if u.User != nil {
		username := u.User.Username()
		if _, hasPassword := u.User.Password(); hasPassword {
			u.User = url.UserPassword(username, "••••••")
		}
	}
	return u.String()
}

func decodeSource(w http.ResponseWriter, r *http.Request) (config.Source, bool) {
	var sc config.Source
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&sc); err != nil {
		writeError(w, http.StatusBadRequest, "invalid source configuration: "+err.Error())
		return config.Source{}, false
	}
	sc.Name = strings.TrimSpace(sc.Name)
	sc.Type = strings.ToLower(strings.TrimSpace(sc.Type))
	sc.Title = strings.TrimSpace(sc.Title)
	sc.Path = strings.TrimSpace(sc.Path)
	sc.Table = strings.TrimSpace(sc.Table)
	sc.GeometryColumn = strings.TrimSpace(sc.GeometryColumn)
	sc.IDColumn = strings.TrimSpace(sc.IDColumn)
	return sc, true
}

func (s *Server) preserveSecret(sc config.Source) config.Source {
	if (sc.Type != "postgis" && sc.Type != "mysql") || sc.DSN != "" {
		return sc
	}
	for _, existing := range s.cfg.Sources {
		if existing.Name == sc.Name && existing.Type == sc.Type {
			sc.DSN = existing.DSN
			break
		}
	}
	return sc
}

func (s *Server) handleProbeSource(w http.ResponseWriter, r *http.Request) {
	sc, ok := decodeSource(w, r)
	if !ok {
		return
	}
	s.configMu.Lock()
	sc = s.preserveSecret(sc)
	s.configMu.Unlock()
	if err := validateCandidate(sc); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	candidate, err := registry.Open(ctx, sc)
	if err == nil {
		defer candidate.Close()
		err = candidate.Ping(ctx)
	}
	if err != nil {
		writeJSON(w, http.StatusOK, "application/json", map[string]interface{}{
			"ok": false, "detail": err.Error(),
		})
		return
	}
	writeJSON(w, http.StatusOK, "application/json", map[string]interface{}{
		"ok": true, "detail": "连接成功，数据源可读取",
	})
}

func validateCandidate(sc config.Source) error {
	cfg := config.Default()
	cfg.Sources = []config.Source{sc}
	return cfg.Validate()
}

func (s *Server) handleSaveSource(w http.ResponseWriter, r *http.Request) {
	if s.configPath == "" {
		writeError(w, http.StatusConflict, "server was started without a writable config path")
		return
	}
	sc, ok := decodeSource(w, r)
	if !ok {
		return
	}
	s.configMu.Lock()
	defer s.configMu.Unlock()
	sc = s.preserveSecret(sc)
	if err := validateCandidate(sc); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	candidate, err := registry.Open(ctx, sc)
	if err == nil {
		err = candidate.Ping(ctx)
	}
	if err != nil {
		if candidate != nil {
			candidate.Close()
		}
		writeError(w, http.StatusBadRequest, "source probe failed: "+err.Error())
		return
	}

	sources := append([]config.Source(nil), s.cfg.Sources...)
	found := false
	for i := range sources {
		if sources[i].Name == sc.Name {
			sources[i] = sc
			found = true
			break
		}
	}
	if !found {
		sources = append(sources, sc)
	}
	next := *s.cfg
	next.Sources = sources
	if err := next.Validate(); err != nil {
		candidate.Close()
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := config.SaveSources(s.configPath, sources); err != nil {
		candidate.Close()
		s.log.Error("save source config", "source", sc.Name, "error", err)
		writeError(w, http.StatusInternalServerError, "persist source configuration: "+err.Error())
		return
	}
	s.reg.Replace(sc.Name, candidate)
	s.cfg.Sources = sources
	s.invalidateReadiness()
	s.log.Info("source configured", "name", sc.Name, "type", sc.Type, "replaced", found)
	writeJSON(w, http.StatusOK, "application/json", map[string]interface{}{
		"saved": true, "name": sc.Name, "replaced": found,
	})
}

func (s *Server) handleDeleteSource(w http.ResponseWriter, r *http.Request) {
	if s.configPath == "" {
		writeError(w, http.StatusConflict, "server was started without a writable config path")
		return
	}
	name := r.PathValue("name")
	s.configMu.Lock()
	defer s.configMu.Unlock()
	sources := make([]config.Source, 0, len(s.cfg.Sources))
	found := false
	for _, sc := range s.cfg.Sources {
		if sc.Name == name {
			found = true
			continue
		}
		sources = append(sources, sc)
	}
	if !found {
		writeError(w, http.StatusNotFound, fmt.Sprintf("unknown source %q", name))
		return
	}
	next := *s.cfg
	next.Sources = sources
	if err := next.Validate(); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := config.SaveSources(s.configPath, sources); err != nil {
		s.log.Error("delete source config", "source", name, "error", err)
		writeError(w, http.StatusInternalServerError, "persist source configuration: "+err.Error())
		return
	}
	s.reg.Remove(name)
	s.cfg.Sources = sources
	s.invalidateReadiness()
	s.log.Info("source removed", "name", name)
	writeJSON(w, http.StatusOK, "application/json", map[string]interface{}{
		"deleted": true, "name": name,
	})
}

func (s *Server) invalidateReadiness() {
	s.ready.mu.Lock()
	s.ready.checked = time.Time{}
	s.ready.statuses = nil
	s.ready.mu.Unlock()
}

func configWritable(path string) bool {
	if path == "" {
		return false
	}
	f, err := os.OpenFile(path, os.O_WRONLY, 0)
	if err != nil {
		return false
	}
	return f.Close() == nil
}
