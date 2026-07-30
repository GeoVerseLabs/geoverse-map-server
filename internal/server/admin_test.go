package server

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
)

// getJSONReq is getJSON for requests that need a method or headers.
func getJSONReq(t *testing.T, req *http.Request, wantStatus int) map[string]interface{} {
	t.Helper()
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != wantStatus {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("%s %s: status %d, want %d; body: %s",
			req.Method, req.URL.Path, resp.StatusCode, wantStatus, body)
	}
	var out map[string]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("%s %s: bad json: %v", req.Method, req.URL.Path, err)
	}
	return out
}

func TestReadyz(t *testing.T) {
	ts := testServer(t)
	doc := getJSON(t, ts.URL+"/readyz", http.StatusOK)
	if doc["status"] != "ready" {
		t.Errorf("readyz = %v", doc)
	}
	if _, ok := doc["sources"].(map[string]interface{})["cities"]; !ok {
		t.Errorf("readyz should report each source: %v", doc)
	}
}

// Probe responses must not be cached by proxies, or a load balancer can
// keep routing to a replica that went unhealthy minutes ago.
func TestProbesAreNotCacheable(t *testing.T) {
	ts := testServer(t)
	resp, err := http.Get(ts.URL + "/readyz")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if got := resp.Header.Get("Cache-Control"); got != "no-store" {
		t.Errorf("readyz Cache-Control = %q, want no-store", got)
	}
}

func TestStats(t *testing.T) {
	ts := testServer(t)
	// Generate traffic so the counters have something to report.
	for i := 0; i < 3; i++ {
		resp, err := http.Get(ts.URL + "/tiles/cities/6/52/24.pbf")
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
	}
	doc := getJSON(t, ts.URL+"/admin/stats", http.StatusOK)

	if doc["version"] == nil || doc["uptimeSeconds"] == nil {
		t.Errorf("stats missing version/uptime: %v", doc)
	}
	sources := doc["sources"].(map[string]interface{})
	if sources["total"].(float64) != 1 {
		t.Errorf("sources.total = %v, want 1", sources["total"])
	}
	reqs := doc["requests"].(map[string]interface{})
	if reqs["total"].(float64) < 3 {
		t.Errorf("requests.total = %v, want >= 3", reqs["total"])
	}
	if _, ok := doc["cache"].(map[string]interface{}); !ok {
		t.Errorf("stats missing cache block: %v", doc)
	}
	if _, ok := doc["features"].(map[string]interface{}); !ok {
		t.Errorf("stats missing features block: %v", doc)
	}
}

// The management UI renders /admin/stats verbatim, so a credential leaking
// into it would land straight in an operator's browser (and screenshots).
func TestStatsLeaksNoCredentials(t *testing.T) {
	ts := authedServer(t)
	req, _ := http.NewRequest("GET", ts.URL+"/admin/stats", nil)
	req.Header.Set("X-API-Key", "sekrit")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	for _, secret := range []string{"sekrit", "other-key"} {
		if strings.Contains(string(body), secret) {
			t.Errorf("/admin/stats leaked API key %q: %s", secret, body)
		}
	}
}

func TestPurgeCache(t *testing.T) {
	ts := testServer(t)
	// Warm the cache, then confirm the purge reports what it dropped.
	resp, err := http.Get(ts.URL + "/tiles/cities/6/52/24.pbf")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	req, _ := http.NewRequest(http.MethodDelete, ts.URL+"/admin/cache", nil)
	dresp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer dresp.Body.Close()
	if dresp.StatusCode != http.StatusOK {
		t.Fatalf("purge status = %d", dresp.StatusCode)
	}

	// Purging is idempotent: a retrying client must not get an error, and
	// the second call must report nothing left to drop.
	req2, _ := http.NewRequest(http.MethodDelete, ts.URL+"/admin/cache", nil)
	doc := getJSONReq(t, req2, http.StatusOK)
	if doc["memoryEntries"].(float64) != 0 {
		t.Errorf("second purge dropped %v entries, want 0", doc["memoryEntries"])
	}
}

func TestMetricsExposition(t *testing.T) {
	ts := testServer(t)
	resp, err := http.Get(ts.URL + "/catalog")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	mresp, err := http.Get(ts.URL + "/metrics")
	if err != nil {
		t.Fatal(err)
	}
	defer mresp.Body.Close()
	if ct := mresp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/plain") {
		t.Errorf("metrics Content-Type = %q", ct)
	}
	body, _ := io.ReadAll(mresp.Body)
	text := string(body)
	for _, want := range []string{
		"geoverse_build_info",
		"geoverse_uptime_seconds",
		"geoverse_http_requests_total{status=\"2xx\"}",
		"geoverse_http_request_duration_seconds_bucket{le=\"+Inf\"}",
		"geoverse_cache_hits_total",
		"geoverse_source_up{source=\"cities\"} 1",
		"geoverse_goroutines",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("metrics missing %q\n---\n%s", want, text)
		}
	}
	assertEveryMetricIsDeclared(t, text)
}

// assertEveryMetricIsDeclared checks that each emitted sample belongs to a
// metric that also has HELP and TYPE lines.
//
// Counting "# HELP" against "# TYPE" is not enough: those two are always
// written together, so a sample emitted with neither keeps the counts
// balanced and slips through.
func assertEveryMetricIsDeclared(t *testing.T, text string) {
	t.Helper()
	declared := map[string]bool{}
	var samples []string
	for _, line := range strings.Split(text, "\n") {
		switch {
		case strings.HasPrefix(line, "# HELP "):
			declared[strings.Fields(line)[2]] = true
		case strings.HasPrefix(line, "# TYPE "), line == "":
			// TYPE always accompanies HELP; blank lines are noise.
		default:
			name, _, _ := strings.Cut(line, " ")
			name, _, _ = strings.Cut(name, "{")
			samples = append(samples, name)
		}
	}
	if len(samples) == 0 {
		t.Fatal("no samples parsed from exposition")
	}
	for _, name := range samples {
		base := name
		// Histograms emit <name>_bucket/_sum/_count under one HELP line.
		for _, suffix := range []string{"_bucket", "_sum", "_count"} {
			if trimmed, ok := strings.CutSuffix(base, suffix); ok && declared[trimmed] {
				base = trimmed
				break
			}
		}
		if !declared[base] {
			t.Errorf("metric %q emitted without a # HELP/# TYPE declaration", name)
		}
	}
}

// Operational endpoints expose source names and cache behaviour, and the
// purge endpoint mutates state — none of that may be anonymous when auth
// is on. The two probes are the deliberate exception.
func TestAdminEndpointsRequireAuth(t *testing.T) {
	ts := authedServer(t)
	for _, tc := range []struct {
		method, path string
		want         int
	}{
		{"GET", "/admin/stats", http.StatusUnauthorized},
		{"DELETE", "/admin/cache", http.StatusUnauthorized},
		{"GET", "/metrics", http.StatusUnauthorized},
		{"GET", "/readyz", http.StatusOK},
		{"GET", "/health", http.StatusOK},
	} {
		req, _ := http.NewRequest(tc.method, ts.URL+tc.path, nil)
		if got := status(t, req); got != tc.want {
			t.Errorf("anonymous %s %s: status = %d, want %d", tc.method, tc.path, got, tc.want)
		}
	}
}
