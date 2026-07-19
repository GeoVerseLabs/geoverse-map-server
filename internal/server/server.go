// Package server implements the HTTP API: XYZ/WMTS tiles, TileJSON,
// OGC API - Features and service metadata endpoints.
package server

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"

	"github.com/GeoVerseLabs/geoverse-map-server/internal/cache"
	"github.com/GeoVerseLabs/geoverse-map-server/internal/config"
	"github.com/GeoVerseLabs/geoverse-map-server/internal/mcpserver"
	"github.com/GeoVerseLabs/geoverse-map-server/internal/source/registry"
)

// Version is the server version reported in metadata documents.
var Version = "dev"

// Server wires the registry, cache and HTTP handlers together.
type Server struct {
	cfg   *config.Config
	reg   *registry.Registry
	cache *cache.Tiered
	log   *slog.Logger
}

// New creates a Server. store may be nil (caching disabled).
func New(cfg *config.Config, reg *registry.Registry, store *cache.Tiered, log *slog.Logger) *Server {
	return &Server{cfg: cfg, reg: reg, cache: store, log: log}
}

// Handler builds the full route table wrapped in middleware.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()

	// Service metadata.
	mux.HandleFunc("GET /{$}", s.handleLanding)
	mux.HandleFunc("GET /conformance", s.handleConformance)
	mux.HandleFunc("GET /health", s.handleHealth)
	mux.HandleFunc("GET /catalog", s.handleCatalog)

	// Tiles.
	mux.HandleFunc("GET /tiles/{layer}/{z}/{x}/{yext}", s.handleTile)
	mux.HandleFunc("GET /tiles/{layerjson}", s.handleTileJSON)

	// WMTS.
	mux.HandleFunc("GET /wmts/1.0.0/WMTSCapabilities.xml", s.handleWMTSCapabilities)
	mux.HandleFunc("GET /wmts/1.0.0/{layer}/{style}/{tms}/{z}/{y}/{xext}", s.handleWMTSTile)

	// OGC API - Features.
	mux.HandleFunc("GET /collections", s.handleCollections)
	mux.HandleFunc("GET /collections/{id}", s.handleCollection)
	mux.HandleFunc("GET /collections/{id}/items", s.handleItems)
	mux.HandleFunc("GET /collections/{id}/items/{fid}", s.handleItem)

	// MCP endpoint (Model Context Protocol over Streamable HTTP).
	if s.cfg.MCP.Enabled {
		mcp := mcpserver.New(s.reg, Version, s.baseURL)
		mux.Handle(s.cfg.MCP.Path, mcp)
	}

	var h http.Handler = mux
	if s.cfg.Auth.Enabled {
		h = authMiddleware(s.cfg.Auth.APIKeys, h)
	}
	if s.cfg.Server.CORS {
		h = corsMiddleware(h)
	}
	h = recoverMiddleware(s.log, h)
	h = logMiddleware(s.log, h)
	return h
}

// baseURL returns the externally visible URL prefix for building links.
func (s *Server) baseURL(r *http.Request) string {
	if s.cfg.Server.BaseURL != "" {
		return strings.TrimRight(s.cfg.Server.BaseURL, "/")
	}
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	if v := r.Header.Get("X-Forwarded-Proto"); v != "" {
		scheme = v
	}
	host := r.Host
	if v := r.Header.Get("X-Forwarded-Host"); v != "" {
		host = v
	}
	return fmt.Sprintf("%s://%s", scheme, host)
}

func writeJSON(w http.ResponseWriter, status int, contentType string, v interface{}) {
	w.Header().Set("Content-Type", contentType)
	w.WriteHeader(status)
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, "application/json", map[string]interface{}{
		"code":        status,
		"description": msg,
	})
}
