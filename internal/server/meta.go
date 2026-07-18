package server

import (
	"fmt"
	"net/http"
	"time"
)

// handleLanding serves GET / (OGC API landing page).
func (s *Server) handleLanding(w http.ResponseWriter, r *http.Request) {
	base := s.baseURL(r)
	writeJSON(w, http.StatusOK, "application/json", map[string]interface{}{
		"title":       "GeoVerse Map Server",
		"description": "Lightweight geospatial data distribution: vector tiles, OGC API - Features, WMTS",
		"version":     Version,
		"links": []link{
			{Href: base + "/", Rel: "self", Type: "application/json", Title: "this document"},
			{Href: base + "/conformance", Rel: "conformance", Type: "application/json"},
			{Href: base + "/collections", Rel: "data", Type: "application/json", Title: "feature collections"},
			{Href: base + "/catalog", Rel: "catalog", Type: "application/json", Title: "all layers"},
			{Href: base + "/wmts/1.0.0/WMTSCapabilities.xml", Rel: "wmts", Type: "application/xml", Title: "WMTS capabilities"},
			{Href: base + "/health", Rel: "health", Type: "application/json"},
		},
	})
}

// handleConformance serves GET /conformance.
func (s *Server) handleConformance(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, "application/json", map[string]interface{}{
		"conformsTo": []string{
			"http://www.opengis.net/spec/ogcapi-common-1/1.0/conf/core",
			"http://www.opengis.net/spec/ogcapi-features-1/1.0/conf/core",
			"http://www.opengis.net/spec/ogcapi-features-1/1.0/conf/geojson",
		},
	})
}

// handleHealth serves GET /health, pinging every source.
func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	statuses := map[string]string{}
	healthy := true
	for name, err := range s.reg.Ping(r.Context()) {
		if err != nil {
			statuses[name] = err.Error()
			healthy = false
		} else {
			statuses[name] = "ok"
		}
	}
	status := http.StatusOK
	overall := "ok"
	if !healthy {
		status = http.StatusServiceUnavailable
		overall = "degraded"
	}
	writeJSON(w, status, "application/json", map[string]interface{}{
		"status":  overall,
		"time":    time.Now().UTC().Format(time.RFC3339),
		"sources": statuses,
	})
}

// handleCatalog serves GET /catalog: every layer with its access URLs.
func (s *Server) handleCatalog(w http.ResponseWriter, r *http.Request) {
	base := s.baseURL(r)
	type entry struct {
		Name     string            `json:"name"`
		Title    string            `json:"title,omitempty"`
		Tiles    string            `json:"tiles,omitempty"`
		TileJSON string            `json:"tilejson,omitempty"`
		Items    string            `json:"items,omitempty"`
		Format   string            `json:"format,omitempty"`
		Bounds   *[4]float64       `json:"bounds,omitempty"`
		Zooms    map[string]int    `json:"zooms,omitempty"`
		Extra    map[string]string `json:"-"`
	}
	var out []entry
	for _, name := range s.reg.Names() {
		e := entry{Name: name}
		if ts, ok := s.reg.TileSource(name); ok {
			info := ts.TileInfo()
			e.Title = info.Title
			e.Format = string(info.Format)
			b := info.Bounds
			e.Bounds = &b
			e.Tiles = fmt.Sprintf("%s/tiles/%s/{z}/{x}/{y}.%s", base, name, info.Format)
			e.TileJSON = fmt.Sprintf("%s/tiles/%s.json", base, name)
			e.Zooms = map[string]int{"min": info.MinZoom, "max": info.MaxZoom}
		}
		if _, ok := s.reg.FeatureSource(name); ok {
			e.Items = fmt.Sprintf("%s/collections/%s/items", base, name)
		}
		out = append(out, e)
	}
	writeJSON(w, http.StatusOK, "application/json", map[string]interface{}{"layers": out})
}
