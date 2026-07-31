// Command serveprobe performs a black-box contract probe against a running
// GeoVerse Serve instance. It intentionally checks both successful distribution
// paths and representative negative boundaries.
package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

type response struct {
	status   int
	header   http.Header
	body     []byte
	duration time.Duration
}

type check struct {
	Name       string  `json:"name"`
	Pass       bool    `json:"pass"`
	Status     int     `json:"status,omitempty"`
	DurationMs float64 `json:"durationMs"`
	Detail     string  `json:"detail"`
}

type probeReport struct {
	Base       string  `json:"base"`
	CheckedAt  string  `json:"checkedAt"`
	Passed     int     `json:"passed"`
	Failed     int     `json:"failed"`
	DurationMs float64 `json:"durationMs"`
	Checks     []check `json:"checks"`
}

type probe struct {
	base   string
	client *http.Client
	checks []check
}

func main() {
	base := flag.String("base", "http://127.0.0.1:8080", "server base URL")
	output := flag.String("output", "", "optional JSON output file")
	flag.Parse()

	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.DisableCompression = true
	p := &probe{
		base: strings.TrimRight(*base, "/"),
		client: &http.Client{
			Transport: transport,
			Timeout:   30 * time.Second,
		},
	}
	defer transport.CloseIdleConnections()

	started := time.Now()
	p.run()
	report := probeReport{
		Base:       p.base,
		CheckedAt:  time.Now().UTC().Format(time.RFC3339),
		DurationMs: millis(time.Since(started)),
		Checks:     p.checks,
	}
	for _, item := range p.checks {
		if item.Pass {
			report.Passed++
		} else {
			report.Failed++
		}
	}
	writer, closeOutput, err := outputWriter(*output)
	if err != nil {
		fatalf("open output: %v", err)
	}
	defer closeOutput()
	encoder := json.NewEncoder(writer)
	encoder.SetEscapeHTML(false)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(report); err != nil {
		fatalf("encode report: %v", err)
	}
	if report.Failed > 0 {
		os.Exit(1)
	}
}

func outputWriter(path string) (io.Writer, func(), error) {
	if path == "" {
		return os.Stdout, func() {}, nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, func() {}, err
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return nil, func() {}, err
	}
	return file, func() { _ = file.Close() }, nil
}

func (p *probe) run() {
	p.jsonCheck("landing page", http.MethodGet, "/", nil, nil, http.StatusOK, func(doc map[string]interface{}) error {
		return requireSliceLenAtLeast(doc, "links", 8)
	})
	p.jsonCheck("conformance declaration", http.MethodGet, "/conformance", nil, nil, http.StatusOK, func(doc map[string]interface{}) error {
		return requireSliceLenAtLeast(doc, "conformsTo", 2)
	})
	p.jsonCheck("deep health checks four sources", http.MethodGet, "/health", nil, nil, http.StatusOK, func(doc map[string]interface{}) error {
		if doc["status"] != "ok" {
			return fmt.Errorf("status=%v", doc["status"])
		}
		sources, ok := doc["sources"].(map[string]interface{})
		if !ok || len(sources) != 4 {
			return fmt.Errorf("sources=%v", doc["sources"])
		}
		return nil
	})
	p.jsonCheck("readiness probe", http.MethodGet, "/readyz", nil, nil, http.StatusOK, func(doc map[string]interface{}) error {
		if doc["status"] != "ready" {
			return fmt.Errorf("status=%v", doc["status"])
		}
		return nil
	})
	p.jsonCheck("catalog distributes four layers", http.MethodGet, "/catalog", nil, nil, http.StatusOK, func(doc map[string]interface{}) error {
		layers, ok := doc["layers"].([]interface{})
		if !ok || len(layers) != 4 {
			return fmt.Errorf("layers=%v", doc["layers"])
		}
		names := make([]string, 0, len(layers))
		for _, raw := range layers {
			layer, _ := raw.(map[string]interface{})
			names = append(names, fmt.Sprint(layer["name"]))
		}
		sort.Strings(names)
		if strings.Join(names, ",") != "campus_paths,cities,countries,places" {
			return fmt.Errorf("names=%v", names)
		}
		return nil
	})
	p.jsonCheck("forwarded public links", http.MethodGet, "/catalog", nil, map[string]string{
		"X-Forwarded-Proto": "https",
		"X-Forwarded-Host":  "maps.example.test",
	}, http.StatusOK, func(doc map[string]interface{}) error {
		rawLayers, _ := doc["layers"].([]interface{})
		for _, raw := range rawLayers {
			layer, _ := raw.(map[string]interface{})
			for _, field := range []string{"tiles", "tilejson", "items"} {
				if !strings.HasPrefix(fmt.Sprint(layer[field]), "https://maps.example.test/") {
					return fmt.Errorf("%s=%v", field, layer[field])
				}
			}
		}
		return nil
	})

	p.jsonCheck("TileJSON 3.0", http.MethodGet, "/tiles/cities.json", nil, nil, http.StatusOK, func(doc map[string]interface{}) error {
		if doc["tilejson"] != "3.0.0" {
			return fmt.Errorf("tilejson=%v", doc["tilejson"])
		}
		return requireSliceLenAtLeast(doc, "tiles", 1)
	})

	plain := p.mustRequest("XYZ MVT uncompressed", http.MethodGet, "/tiles/cities/6/52/24.pbf", nil, nil)
	p.record("XYZ MVT uncompressed", plain, plain.status == http.StatusOK &&
		strings.HasPrefix(plain.header.Get("Content-Type"), "application/vnd.mapbox-vector-tile") &&
		plain.header.Get("Content-Encoding") == "" && len(plain.body) > 0,
		fmt.Sprintf("bytes=%d etag=%s encoding=%q", len(plain.body), plain.header.Get("ETag"), plain.header.Get("Content-Encoding")))

	gzipped := p.mustRequest("XYZ MVT gzip", http.MethodGet, "/tiles/cities/6/52/24.pbf", nil, map[string]string{"Accept-Encoding": "gzip"})
	p.record("XYZ MVT gzip", gzipped, gzipped.status == http.StatusOK &&
		gzipped.header.Get("Content-Encoding") == "gzip" && isGzip(gzipped.body),
		fmt.Sprintf("bytes=%d etag=%s encoding=%q", len(gzipped.body), gzipped.header.Get("ETag"), gzipped.header.Get("Content-Encoding")))

	wmts := p.mustRequest("WMTS REST tile", http.MethodGet, "/wmts/1.0.0/cities/default/GoogleMapsCompatible/6/24/52.pbf", nil, nil)
	p.record("WMTS REST tile equals XYZ", wmts, wmts.status == http.StatusOK && bytes.Equal(wmts.body, plain.body),
		fmt.Sprintf("xyzBytes=%d wmtsBytes=%d", len(plain.body), len(wmts.body)))

	capabilities := p.mustRequest("WMTS capabilities", http.MethodGet, "/wmts/1.0.0/WMTSCapabilities.xml", nil, nil)
	p.record("WMTS capabilities", capabilities, capabilities.status == http.StatusOK &&
		strings.Contains(string(capabilities.body), "<Capabilities") &&
		strings.Contains(string(capabilities.body), "cities"),
		fmt.Sprintf("bytes=%d", len(capabilities.body)))

	etag := plain.header.Get("ETag")
	notModified := p.mustRequest("ETag conditional request", http.MethodGet, "/tiles/cities/6/52/24.pbf", nil, map[string]string{"If-None-Match": etag})
	p.record("ETag conditional request", notModified, etag != "" && notModified.status == http.StatusNotModified && len(notModified.body) == 0,
		fmt.Sprintf("etag=%s bytes=%d", etag, len(notModified.body)))

	p.statusCheck("out-of-zoom tile is empty", http.MethodGet, "/tiles/cities/15/0/0.pbf", nil, nil, http.StatusNoContent)
	p.statusCheck("invalid tile coordinate rejected", http.MethodGet, "/tiles/cities/2/9/0.pbf", nil, nil, http.StatusBadRequest)
	p.statusCheck("unknown tile layer rejected", http.MethodGet, "/tiles/not-found/0/0/0.pbf", nil, nil, http.StatusNotFound)

	p.jsonCheck("OGC collections list", http.MethodGet, "/collections", nil, nil, http.StatusOK, func(doc map[string]interface{}) error {
		collections, ok := doc["collections"].([]interface{})
		if !ok || len(collections) != 4 {
			return fmt.Errorf("collections=%v", doc["collections"])
		}
		return nil
	})
	p.jsonCheck("OGC bbox feature query", http.MethodGet, "/collections/cities/items?bbox=115,39,117,41&limit=2", nil, nil, http.StatusOK, func(doc map[string]interface{}) error {
		if number(doc["numberMatched"]) < 1 || number(doc["numberReturned"]) < 1 {
			return fmt.Errorf("matched=%v returned=%v", doc["numberMatched"], doc["numberReturned"])
		}
		return nil
	})
	p.jsonCheck("OGC single feature", http.MethodGet, "/collections/cities/items/1", nil, nil, http.StatusOK, func(doc map[string]interface{}) error {
		if number(doc["id"]) != 1 || doc["type"] != "Feature" {
			return fmt.Errorf("id=%v type=%v", doc["id"], doc["type"])
		}
		return nil
	})
	p.statusCheck("invalid OGC bbox rejected", http.MethodGet, "/collections/cities/items?bbox=10,20,5,30", nil, nil, http.StatusBadRequest)
	p.statusCheck("unknown OGC feature rejected", http.MethodGet, "/collections/cities/items/not-found", nil, nil, http.StatusNotFound)

	p.jsonCheck("algorithm catalog", http.MethodGet, "/algorithms", nil, nil, http.StatusOK, func(doc map[string]interface{}) error {
		algorithms, ok := doc["algorithms"].([]interface{})
		if !ok || len(algorithms) != 4 {
			return fmt.Errorf("algorithms=%v", doc["algorithms"])
		}
		return nil
	})
	p.algorithmCheck("shortest_path", `{"network":"campus","from":[116.300,39.990],"to":[116.3055,39.9925],"to_level":2}`, "features")
	p.algorithmCheck("isochrone", `{"network":"campus","origin":[116.302,39.992],"cutoffs":[120,300]}`, "features")
	p.algorithmCheck("map_match", `{"network":"campus","trace":[[116.3001,39.9901],[116.3010,39.9899],[116.3021,39.9901]]}`, "points")
	p.algorithmCheck("dbscan", `{"collection":"places","eps_m":200000,"min_points":3,"include_points":false}`, "clusters")

	metrics := p.mustRequest("Prometheus metrics", http.MethodGet, "/metrics", nil, nil)
	metricsText := string(metrics.body)
	p.record("Prometheus metrics", metrics, metrics.status == http.StatusOK &&
		strings.Contains(metricsText, "geoverse_http_requests_total") &&
		strings.Contains(metricsText, "geoverse_cache_hits_total"),
		fmt.Sprintf("bytes=%d", len(metrics.body)))

	p.jsonCheck("admin runtime stats", http.MethodGet, "/admin/stats", nil, nil, http.StatusOK, func(doc map[string]interface{}) error {
		features, _ := doc["features"].(map[string]interface{})
		if features["auth"] != false || features["mcp"] != false || features["algorithms"] != true {
			return fmt.Errorf("features=%v", features)
		}
		return nil
	})
	p.statusCheck("MCP disabled on current instance", http.MethodGet, "/mcp", nil, nil, http.StatusNotFound)

	options := p.mustRequest("CORS preflight", http.MethodOptions, "/catalog", nil, map[string]string{
		"Origin":                        "https://client.example",
		"Access-Control-Request-Method": http.MethodGet,
	})
	p.record("CORS preflight", options, options.status == http.StatusNoContent &&
		strings.Contains(options.header.Get("Access-Control-Allow-Methods"), "GET"),
		fmt.Sprintf("allowMethods=%q", options.header.Get("Access-Control-Allow-Methods")))
	p.statusCheck("unsupported method rejected", http.MethodPost, "/catalog", nil, nil, http.StatusMethodNotAllowed)
}

func (p *probe) algorithmCheck(name, body, resultField string) {
	p.jsonCheck("algorithm "+name, http.MethodPost, "/algorithms/"+name, []byte(body),
		map[string]string{"Content-Type": "application/json"}, http.StatusOK,
		func(doc map[string]interface{}) error {
			if _, ok := doc[resultField]; !ok {
				return fmt.Errorf("missing %q in result", resultField)
			}
			return nil
		})
}

func (p *probe) statusCheck(name, method, path string, body []byte, headers map[string]string, want int) {
	resp := p.mustRequest(name, method, path, body, headers)
	p.record(name, resp, resp.status == want, fmt.Sprintf("want=%d got=%d bytes=%d", want, resp.status, len(resp.body)))
}

func (p *probe) jsonCheck(
	name, method, path string,
	body []byte,
	headers map[string]string,
	want int,
	validate func(map[string]interface{}) error,
) {
	resp := p.mustRequest(name, method, path, body, headers)
	var doc map[string]interface{}
	err := json.Unmarshal(resp.body, &doc)
	pass := resp.status == want && err == nil
	detail := fmt.Sprintf("bytes=%d", len(resp.body))
	if err != nil {
		detail = "JSON: " + err.Error()
	} else if validateErr := validate(doc); validateErr != nil {
		pass = false
		detail = validateErr.Error()
	}
	p.record(name, resp, pass, detail)
}

func (p *probe) mustRequest(name, method, path string, body []byte, headers map[string]string) response {
	req, err := http.NewRequest(method, p.base+path, bytes.NewReader(body))
	if err != nil {
		p.checks = append(p.checks, check{Name: name, Detail: err.Error()})
		return response{}
	}
	for key, value := range headers {
		req.Header.Set(key, value)
	}
	started := time.Now()
	resp, err := p.client.Do(req)
	elapsed := time.Since(started)
	if err != nil {
		p.checks = append(p.checks, check{Name: name, DurationMs: millis(elapsed), Detail: err.Error()})
		return response{duration: elapsed}
	}
	defer resp.Body.Close()
	data, readErr := io.ReadAll(resp.Body)
	if readErr != nil {
		p.checks = append(p.checks, check{Name: name, Status: resp.StatusCode, DurationMs: millis(elapsed), Detail: readErr.Error()})
	}
	return response{status: resp.StatusCode, header: resp.Header.Clone(), body: data, duration: elapsed}
}

func (p *probe) record(name string, resp response, pass bool, detail string) {
	p.checks = append(p.checks, check{
		Name:       name,
		Pass:       pass,
		Status:     resp.status,
		DurationMs: millis(resp.duration),
		Detail:     detail,
	})
}

func requireSliceLenAtLeast(doc map[string]interface{}, field string, count int) error {
	value, ok := doc[field].([]interface{})
	if !ok || len(value) < count {
		return fmt.Errorf("%s=%v", field, doc[field])
	}
	return nil
}

func number(value interface{}) float64 {
	number, _ := value.(float64)
	return number
}

func isGzip(data []byte) bool {
	return len(data) >= 2 && data[0] == 0x1f && data[1] == 0x8b
}

func millis(value time.Duration) float64 {
	return float64(value) / float64(time.Millisecond)
}

func fatalf(format string, args ...interface{}) {
	_, _ = fmt.Fprintf(os.Stderr, format+"\n", args...)
	os.Exit(2)
}
