package server

import (
	"context"
	"fmt"
	"net/http"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// readinessTTL bounds how often /readyz actually touches the sources.
// Orchestrators probe readiness every few seconds per replica; without a
// cache a rolling restart would turn readiness checks into a load spike on
// the very PostGIS instance the probe is meant to protect.
const readinessTTL = 5 * time.Second

// readinessProbeTimeout caps a single round of source pings so a hung
// database cannot make the probe hang instead of reporting "not ready".
const readinessProbeTimeout = 3 * time.Second

// readiness memoizes the last source-ping sweep.
type readiness struct {
	mu       sync.Mutex
	checked  time.Time
	ready    bool
	statuses map[string]string
}

// snapshot returns the cached readiness verdict, refreshing it when stale.
func (s *Server) snapshot(ctx context.Context) (bool, map[string]string) {
	s.ready.mu.Lock()
	defer s.ready.mu.Unlock()
	if time.Since(s.ready.checked) < readinessTTL && s.ready.statuses != nil {
		return s.ready.ready, s.ready.statuses
	}
	probeCtx, cancel := context.WithTimeout(ctx, readinessProbeTimeout)
	defer cancel()

	statuses := map[string]string{}
	ok := true
	for name, err := range s.reg.Ping(probeCtx) {
		if err != nil {
			statuses[name] = err.Error()
			ok = false
		} else {
			statuses[name] = "ok"
		}
	}
	s.ready.checked, s.ready.ready, s.ready.statuses = time.Now(), ok, statuses
	return ok, statuses
}

// handleReadyz serves GET /readyz: a cheap, cacheable readiness probe.
//
// It is deliberately distinct from /health. /health is the operator-facing
// deep check and always re-pings; /readyz answers the orchestrator's
// narrower question — "should this replica receive traffic right now?" —
// and is safe to poll at probe frequency.
func (s *Server) handleReadyz(w http.ResponseWriter, r *http.Request) {
	ok, statuses := s.snapshot(r.Context())
	status := http.StatusOK
	verdict := "ready"
	if !ok {
		status = http.StatusServiceUnavailable
		verdict = "not ready"
	}
	// Probes must never be served from an intermediary's cache.
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, status, "application/json", map[string]interface{}{
		"status":  verdict,
		"sources": statuses,
	})
}

// handleStats serves GET /admin/stats: cache effectiveness plus runtime
// and feature-flag state, for the management UI and for humans debugging a
// slow deployment.
//
// It reports only counts, booleans and names — never a DSN, an API key or
// a filesystem path from a source config. An operator dashboard is exactly
// the kind of place where a leaked PostGIS password ends up in a screenshot.
func (s *Server) handleStats(w http.ResponseWriter, r *http.Request) {
	var mem runtime.MemStats
	runtime.ReadMemStats(&mem)

	tiles, features := 0, 0
	for _, name := range s.reg.Names() {
		if _, ok := s.reg.TileSource(name); ok {
			tiles++
		}
		if _, ok := s.reg.FeatureSource(name); ok {
			features++
		}
	}

	algoNames := []string{}
	if s.cfg.Algorithms.On() {
		algoNames = s.algos.Names()
	}
	networks := make([]string, 0, len(s.cfg.Networks))
	for _, n := range s.cfg.Networks {
		networks = append(networks, n.Name)
	}

	writeJSON(w, http.StatusOK, "application/json", map[string]interface{}{
		"version":       Version,
		"uptimeSeconds": int64(s.metrics.uptime().Seconds()),
		"startedAt":     s.metrics.started.UTC().Format(time.RFC3339),
		"cache":         s.cache.Stats(),
		"sources": map[string]interface{}{
			"total":    len(s.reg.Names()),
			"tile":     tiles,
			"feature":  features,
			"names":    s.reg.Names(),
			"networks": networks,
		},
		"requests": map[string]interface{}{
			"total":            s.metrics.totalRequests(),
			"inFlight":         s.metrics.inFlight.Load(),
			"bytesOut":         s.metrics.bytesOut.Load(),
			"byStatusClass":    s.statusClasses(),
			"avgDurationMicro": s.avgDurationMicros(),
		},
		"runtime": map[string]interface{}{
			"go":         runtime.Version(),
			"os":         runtime.GOOS,
			"arch":       runtime.GOARCH,
			"goroutines": runtime.NumGoroutine(),
			"heapBytes":  mem.HeapAlloc,
			"sysBytes":   mem.Sys,
			"gcCount":    mem.NumGC,
		},
		"features": map[string]bool{
			"auth":       s.cfg.Auth.Enabled,
			"mcp":        s.cfg.MCP.Enabled,
			"algorithms": s.cfg.Algorithms.On(),
			"cors":       s.cfg.Server.CORS,
			"diskCache":  s.cfg.Cache.Disk.Enabled,
		},
		"algorithms": algoNames,
	})
}

func (s *Server) statusClasses() map[string]uint64 {
	out := map[string]uint64{}
	for i, label := range []string{"1xx", "2xx", "3xx", "4xx", "5xx"} {
		out[label] = s.metrics.requests[i].Load()
	}
	return out
}

func (s *Server) avgDurationMicros() uint64 {
	total := s.metrics.totalRequests()
	if total == 0 {
		return 0
	}
	return s.metrics.durSum.Load() / total
}

// handlePurgeCache serves DELETE /admin/cache: drop every cached tile.
//
// This is the one mutating endpoint in an otherwise read-only service. It
// is safe by construction — tiles are derived data, so the worst outcome
// is that the next requests re-render — and idempotent, so a retrying
// client cannot make things worse.
func (s *Server) handlePurgeCache(w http.ResponseWriter, r *http.Request) {
	if s.cache == nil {
		writeJSON(w, http.StatusOK, "application/json", map[string]interface{}{
			"purged":  false,
			"message": "cache is disabled",
		})
		return
	}
	mem, disk := s.cache.Purge()
	s.log.Info("cache purged", "memory_entries", mem, "disk_files", disk)
	writeJSON(w, http.StatusOK, "application/json", map[string]interface{}{
		"purged":        true,
		"memoryEntries": mem,
		"diskFiles":     disk,
	})
}

// handleMetrics serves GET /metrics in Prometheus text exposition format.
func (s *Server) handleMetrics(w http.ResponseWriter, r *http.Request) {
	var b strings.Builder
	gauge := func(name, help string, v interface{}, labels ...string) {
		metricLine(&b, "gauge", name, help, v, labels...)
	}
	counter := func(name, help string, v interface{}, labels ...string) {
		metricLine(&b, "counter", name, help, v, labels...)
	}

	gauge("geoverse_build_info", "Build information; always 1.", 1, "version", Version, "go", runtime.Version())
	gauge("geoverse_uptime_seconds", "Seconds since process start.", int64(s.metrics.uptime().Seconds()))

	// HTTP.
	writeHelp(&b, "counter", "geoverse_http_requests_total", "Total HTTP requests by status class.")
	for i, label := range []string{"1xx", "2xx", "3xx", "4xx", "5xx"} {
		writeSample(&b, "geoverse_http_requests_total", s.metrics.requests[i].Load(), "status", label)
	}
	counter("geoverse_http_response_bytes_total", "Total response bytes written.", s.metrics.bytesOut.Load())
	gauge("geoverse_http_requests_in_flight", "Requests currently being served.", s.metrics.inFlight.Load())

	writeHelp(&b, "histogram", "geoverse_http_request_duration_seconds", "Request latency.")
	for i, ub := range durationBuckets {
		writeSample(&b, "geoverse_http_request_duration_seconds_bucket",
			s.metrics.bucketHits[i].Load(), "le", strconv.FormatFloat(ub, 'g', -1, 64))
	}
	writeSample(&b, "geoverse_http_request_duration_seconds_bucket",
		s.metrics.bucketHits[len(durationBuckets)].Load(), "le", "+Inf")
	writeSample(&b, "geoverse_http_request_duration_seconds_sum",
		float64(s.metrics.durSum.Load())/1e6)
	writeSample(&b, "geoverse_http_request_duration_seconds_count", s.metrics.totalRequests())

	// Cache.
	cs := s.cache.Stats()
	writeHelp(&b, "counter", "geoverse_cache_hits_total", "Cache hits by tier.")
	writeHelp(&b, "counter", "geoverse_cache_misses_total", "Cache misses by tier.")
	writeHelp(&b, "gauge", "geoverse_cache_entries", "Entries currently cached by tier.")
	writeHelp(&b, "gauge", "geoverse_cache_bytes", "Bytes currently cached by tier.")
	writeHelp(&b, "counter", "geoverse_cache_evictions_total", "Entries evicted by the LRU bound.")
	if cs.Memory != nil {
		writeSample(&b, "geoverse_cache_hits_total", cs.Memory.Hits, "tier", "memory")
		writeSample(&b, "geoverse_cache_misses_total", cs.Memory.Misses, "tier", "memory")
		writeSample(&b, "geoverse_cache_entries", cs.Memory.Entries, "tier", "memory")
		writeSample(&b, "geoverse_cache_bytes", cs.Memory.Bytes, "tier", "memory")
		writeSample(&b, "geoverse_cache_evictions_total", cs.Memory.Evictions, "tier", "memory")
	}
	if cs.Disk != nil {
		writeSample(&b, "geoverse_cache_hits_total", cs.Disk.Hits, "tier", "disk")
		writeSample(&b, "geoverse_cache_misses_total", cs.Disk.Misses, "tier", "disk")
		writeSample(&b, "geoverse_cache_entries", cs.Disk.Files, "tier", "disk")
		writeSample(&b, "geoverse_cache_bytes", cs.Disk.Bytes, "tier", "disk")
	}

	// Sources. Readiness comes from the cached snapshot so that a scrape
	// never turns into a fan-out of database pings.
	_, statuses := s.snapshot(r.Context())
	names := make([]string, 0, len(statuses))
	for n := range statuses {
		names = append(names, n)
	}
	sort.Strings(names) // stable scrape output
	gauge("geoverse_sources_total", "Configured data sources.", len(s.reg.Names()))
	writeHelp(&b, "gauge", "geoverse_source_up", "1 if the source answered its last ping, else 0.")
	for _, n := range names {
		up := 0
		if statuses[n] == "ok" {
			up = 1
		}
		writeSample(&b, "geoverse_source_up", up, "source", n)
	}

	// Process.
	var mem runtime.MemStats
	runtime.ReadMemStats(&mem)
	gauge("geoverse_goroutines", "Number of goroutines.", runtime.NumGoroutine())
	gauge("geoverse_heap_bytes", "Heap bytes in use.", mem.HeapAlloc)
	gauge("geoverse_sys_bytes", "Bytes obtained from the OS.", mem.Sys)
	counter("geoverse_gc_total", "Completed GC cycles.", mem.NumGC)

	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write([]byte(b.String()))
}

func writeHelp(b *strings.Builder, typ, name, help string) {
	fmt.Fprintf(b, "# HELP %s %s\n# TYPE %s %s\n", name, help, name, typ)
}

// metricLine writes a HELP/TYPE header plus a single sample.
func metricLine(b *strings.Builder, typ, name, help string, v interface{}, labels ...string) {
	writeHelp(b, typ, name, help)
	writeSample(b, name, v, labels...)
}

// writeSample writes one sample line. labels are flat key/value pairs.
func writeSample(b *strings.Builder, name string, v interface{}, labels ...string) {
	b.WriteString(name)
	if len(labels) >= 2 {
		b.WriteByte('{')
		for i := 0; i+1 < len(labels); i += 2 {
			if i > 0 {
				b.WriteByte(',')
			}
			fmt.Fprintf(b, "%s=%q", labels[i], escapeLabel(labels[i+1]))
		}
		b.WriteByte('}')
	}
	fmt.Fprintf(b, " %v\n", v)
}

// escapeLabel neutralises the characters the exposition format reserves
// inside label values. Source names are config-controlled, not user input,
// but a stray quote would corrupt the whole scrape rather than one line.
func escapeLabel(s string) string {
	return strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`).Replace(s)
}
