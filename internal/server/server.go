// Package server implements the HTTP API: XYZ/WMTS tiles, TileJSON,
// OGC API - Features and service metadata endpoints.
package server

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"

	"github.com/GeoVerseLabs/geoverse-map-server/internal/algo"
	"github.com/GeoVerseLabs/geoverse-map-server/internal/algo/cluster"
	"github.com/GeoVerseLabs/geoverse-map-server/internal/algo/network"
	"github.com/GeoVerseLabs/geoverse-map-server/internal/algo/routing"
	"github.com/GeoVerseLabs/geoverse-map-server/internal/cache"
	"github.com/GeoVerseLabs/geoverse-map-server/internal/config"
	"github.com/GeoVerseLabs/geoverse-map-server/internal/mcpserver"
	"github.com/GeoVerseLabs/geoverse-map-server/internal/source/registry"
)

// Version is the server version reported in metadata documents.
var Version = "dev"

// Server wires the registry, cache, algorithms and HTTP handlers together.
type Server struct {
	cfg        *config.Config
	reg        *registry.Registry
	cache      *cache.Tiered
	log        *slog.Logger
	algos      *algo.Registry
	env        *algo.Env
	metrics    *metrics
	ready      readiness
	configPath string
	configMu   sync.Mutex
}

// New creates a Server. store may be nil (caching disabled).
func New(cfg *config.Config, reg *registry.Registry, store *cache.Tiered, log *slog.Logger) *Server {
	return NewManaged(cfg, "", reg, store, log)
}

// NewManaged creates a Server whose data-source configuration can be updated
// through the embedded WebUI. An empty configPath keeps the UI inspect-only,
// which is useful for tests and for callers that assemble Config in memory.
func NewManaged(cfg *config.Config, configPath string, reg *registry.Registry, store *cache.Tiered, log *slog.Logger) *Server {
	nets := make([]network.NetworkConfig, 0, len(cfg.Networks))
	for _, n := range cfg.Networks {
		nets = append(nets, network.NetworkConfig{
			Name:            n.Name,
			Source:          n.Source,
			DefaultSpeedKMH: n.DefaultSpeedKMH,
			SpeedField:      n.SpeedField,
			OnewayField:     n.OnewayField,
			LevelField:      n.LevelField,
			NameField:       n.NameField,
		})
	}
	algos := algo.NewRegistry()
	algos.Register(routing.ShortestPath{})
	algos.Register(routing.Isochrone{})
	algos.Register(routing.MapMatch{})
	algos.Register(cluster.DBSCAN{})
	return &Server{
		cfg: cfg, reg: reg, cache: store, log: log,
		algos:      algos,
		metrics:    newMetrics(),
		configPath: configPath,
		env: &algo.Env{
			Features: reg.FeatureSource,
			Networks: network.NewManager(nets, reg.FeatureSource),
		},
	}
}

// Handler builds the full route table wrapped in middleware.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()

	// Service metadata.
	mux.HandleFunc("GET /{$}", s.handleLanding)
	mux.HandleFunc("GET /api", s.handleOpenAPI)
	mux.HandleFunc("GET /conformance", s.handleConformance)
	mux.HandleFunc("GET /health", s.handleHealth)
	mux.HandleFunc("GET /readyz", s.handleReadyz)
	mux.HandleFunc("GET /catalog", s.handleCatalog)

	// Operations. /metrics and /admin/* stay behind API-key auth when it is
	// enabled — they expose source names and cache behaviour, and the purge
	// endpoint mutates state. Only the two probes are exempt (see
	// authMiddleware), because a load balancer cannot carry a key.
	mux.HandleFunc("GET /metrics", s.handleMetrics)
	mux.HandleFunc("GET /admin/{$}", s.handleWebUI)
	mux.HandleFunc("GET /admin/app.css", s.handleWebUI)
	mux.HandleFunc("GET /admin/app.js", s.handleWebUI)
	mux.HandleFunc("GET /admin/stats", s.handleStats)
	mux.HandleFunc("DELETE /admin/cache", s.handlePurgeCache)
	mux.HandleFunc("GET /admin/sources", s.handleListSources)
	mux.HandleFunc("POST /admin/sources", s.handleSaveSource)
	mux.HandleFunc("POST /admin/sources/probe", s.handleProbeSource)
	mux.HandleFunc("DELETE /admin/sources/{name}", s.handleDeleteSource)

	// Tiles.
	mux.HandleFunc("GET /tiles/{layer}/{z}/{x}/{yext}", s.handleTile)
	mux.HandleFunc("GET /tiles/{layerjson}", s.handleTileJSON)
	mux.HandleFunc("GET /archives/{archive}", s.handleArchive)
	mux.HandleFunc("HEAD /archives/{archive}", s.handleArchive)

	// WMTS.
	mux.HandleFunc("GET /wmts/1.0.0/WMTSCapabilities.xml", s.handleWMTSCapabilities)
	mux.HandleFunc("GET /wmts/1.0.0/{layer}/{style}/{tms}/{z}/{y}/{xext}", s.handleWMTSTile)

	// OGC API - Features.
	mux.HandleFunc("GET /collections", s.handleCollections)
	mux.HandleFunc("GET /collections/{id}", s.handleCollection)
	mux.HandleFunc("GET /collections/{id}/items", s.handleItems)
	mux.HandleFunc("GET /collections/{id}/items/{fid}", s.handleItem)

	// OGC API - Tiles. Additive: XYZ (/tiles/...) and WMTS above are
	// unaffected and stay the primary route for existing clients.
	mux.HandleFunc("GET /tileMatrixSets", s.handleTileMatrixSets)
	mux.HandleFunc("GET /tileMatrixSets/{id}", s.handleTileMatrixSet)
	mux.HandleFunc("GET /collections/{id}/tiles", s.handleCollectionTileset)
	mux.HandleFunc("GET /collections/{id}/tiles/{tms}/{tileMatrix}/{tileRow}/{tileCol}", s.handleStandardTile)

	// Algorithm endpoints.
	if s.cfg.Algorithms.On() {
		mux.HandleFunc("GET /algorithms", s.handleAlgorithms)
		mux.HandleFunc("GET /algorithms/{name}", s.handleAlgorithmGet)
		mux.HandleFunc("POST /algorithms/{name}", s.handleAlgorithmRun)
	}

	// MCP endpoint (Model Context Protocol over Streamable HTTP).
	if s.cfg.MCP.Enabled {
		var algos *algo.Registry
		if s.cfg.Algorithms.On() {
			algos = s.algos
		}
		mcp := mcpserver.New(s.reg, Version, s.baseURL, algos, s.env)
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
	// Metrics sit outside recover so a panicking handler still counts as
	// the 500 the client saw, and outside auth so rejected requests are
	// visible — an unauthenticated flood is exactly what you want graphed.
	h = metricsMiddleware(s.metrics, h)
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
