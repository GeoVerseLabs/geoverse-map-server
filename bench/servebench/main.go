// Command servebench is a small, dependency-free HTTP load generator used by
// GeoVerse Serve performance reports. It deliberately records response status
// classes separately from transport failures so 204/304 map-service responses
// are not mistaken for network errors.
package main

import (
	"encoding/base64"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

type stringList []string

func (s *stringList) String() string { return strings.Join(*s, ",") }

func (s *stringList) Set(value string) error {
	*s = append(*s, value)
	return nil
}

type workerResult struct {
	latencies    []time.Duration
	statusCounts map[int]int64
	bytes        int64
	transportErr int64
	errorSamples []string
}

type report struct {
	Scenario        string           `json:"scenario"`
	Method          string           `json:"method"`
	Concurrency     int              `json:"concurrency"`
	RequestedWindow string           `json:"requestedWindow,omitempty"`
	MaxRequests     int64            `json:"maxRequests,omitempty"`
	ElapsedSeconds  float64          `json:"elapsedSeconds"`
	Requests        int64            `json:"requests"`
	RPS             float64          `json:"rps"`
	SuccessRate     float64          `json:"successRate"`
	TransportErrors int64            `json:"transportErrors"`
	StatusCounts    map[string]int64 `json:"statusCounts"`
	Bytes           int64            `json:"bytes"`
	BytesPerSecond  float64          `json:"bytesPerSecond"`
	LatencyMs       latencyReport    `json:"latencyMs"`
	ErrorSamples    []string         `json:"errorSamples,omitempty"`
}

type latencyReport struct {
	Min  float64 `json:"min"`
	Mean float64 `json:"mean"`
	P50  float64 `json:"p50"`
	P95  float64 `json:"p95"`
	P99  float64 `json:"p99"`
	Max  float64 `json:"max"`
}

func main() {
	var paths stringList
	var headers stringList
	var (
		base         = flag.String("base", "http://127.0.0.1:8080", "server base URL")
		scenario     = flag.String("scenario", "unnamed", "scenario name in the JSON report")
		method       = flag.String("method", http.MethodGet, "HTTP method")
		body         = flag.String("body", "", "request body")
		bodyBase64   = flag.String("body-base64", "", "base64-encoded request body")
		contentType  = flag.String("content-type", "application/json", "request Content-Type")
		concurrency  = flag.Int("concurrency", 1, "number of concurrent workers")
		duration     = flag.Duration("duration", 5*time.Second, "measurement window; ignored when requests is set")
		maxRequests  = flag.Int64("requests", 0, "fixed request count instead of a time window")
		warmup       = flag.Int("warmup", 20, "sequential warm-up requests")
		timeout      = flag.Duration("timeout", 30*time.Second, "per-request timeout")
		acceptGzip   = flag.Bool("gzip", true, "request gzip without automatic client decompression")
		tileGrid     = flag.String("tile-grid", "", "generate unique tile paths: layer:z:count")
		output       = flag.String("output", "", "optional JSON/JSONL output file")
		appendOutput = flag.Bool("append", false, "append one JSON line to output instead of truncating it")
		ifNoneMatch  = flag.String("if-none-match", "", "ETag token for an If-None-Match request (quotes optional)")
	)
	flag.Var(&paths, "url", "relative path or absolute URL; repeat for round-robin traffic")
	flag.Var(&headers, "header", "request header in Name: Value form; repeat as needed")
	flag.Parse()
	if *body != "" && *bodyBase64 != "" {
		exitf("body and body-base64 are mutually exclusive")
	}
	if *bodyBase64 != "" {
		decoded, err := base64.StdEncoding.DecodeString(*bodyBase64)
		if err != nil {
			exitf("body-base64: %v", err)
		}
		*body = string(decoded)
	}

	if *concurrency < 1 {
		exitf("concurrency must be positive")
	}
	if *maxRequests < 0 {
		exitf("requests must not be negative")
	}
	if *maxRequests == 0 && *duration <= 0 {
		exitf("duration must be positive")
	}
	if *tileGrid != "" {
		generated, err := tileGridPaths(*tileGrid)
		if err != nil {
			exitf("tile-grid: %v", err)
		}
		paths = append(paths, generated...)
	}
	if len(paths) == 0 {
		exitf("at least one -url or -tile-grid is required")
	}
	requestHeaders := make(http.Header)
	for _, raw := range headers {
		name, value, ok := strings.Cut(raw, ":")
		if !ok || strings.TrimSpace(name) == "" {
			exitf("header %q must use Name: Value form", raw)
		}
		requestHeaders.Add(strings.TrimSpace(name), strings.TrimSpace(value))
	}
	if *ifNoneMatch != "" {
		value := *ifNoneMatch
		if !strings.HasPrefix(value, `"`) {
			value = `"` + value + `"`
		}
		requestHeaders.Set("If-None-Match", value)
	}

	urls := make([]string, len(paths))
	for i, path := range paths {
		resolved, err := resolveURL(*base, path)
		if err != nil {
			exitf("url %q: %v", path, err)
		}
		urls[i] = resolved
	}

	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.MaxIdleConns = *concurrency * 2
	transport.MaxIdleConnsPerHost = *concurrency
	transport.MaxConnsPerHost = 0
	transport.DisableCompression = true
	client := &http.Client{Transport: transport, Timeout: *timeout}
	defer transport.CloseIdleConnections()

	makeRequest := func(index uint64) (*http.Request, error) {
		target := urls[index%uint64(len(urls))]
		req, err := http.NewRequest(*method, target, strings.NewReader(*body))
		if err != nil {
			return nil, err
		}
		if *body != "" {
			req.Header.Set("Content-Type", *contentType)
		}
		if *acceptGzip {
			req.Header.Set("Accept-Encoding", "gzip")
		}
		for name, values := range requestHeaders {
			for _, value := range values {
				req.Header.Add(name, value)
			}
		}
		return req, nil
	}

	for i := 0; i < *warmup; i++ {
		req, err := makeRequest(uint64(i))
		if err != nil {
			exitf("warm-up request: %v", err)
		}
		resp, err := client.Do(req)
		if err != nil {
			exitf("warm-up request: %v", err)
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
	}

	results := make([]workerResult, *concurrency)
	startGate := make(chan struct{})
	var sequence atomic.Uint64
	var wg sync.WaitGroup
	wg.Add(*concurrency)

	var deadline time.Time
	if *maxRequests == 0 {
		deadline = time.Now().Add(*duration)
	}
	started := time.Now()
	for worker := 0; worker < *concurrency; worker++ {
		go func(worker int) {
			defer wg.Done()
			local := workerResult{statusCounts: make(map[int]int64)}
			<-startGate
			for {
				index := sequence.Add(1) - 1
				if *maxRequests > 0 {
					if index >= uint64(*maxRequests) {
						break
					}
				} else if time.Now().After(deadline) {
					break
				}

				req, err := makeRequest(index)
				if err != nil {
					local.transportErr++
					addSample(&local.errorSamples, err.Error())
					continue
				}
				requestStarted := time.Now()
				resp, err := client.Do(req)
				if err != nil {
					local.latencies = append(local.latencies, time.Since(requestStarted))
					local.transportErr++
					addSample(&local.errorSamples, err.Error())
					continue
				}
				n, readErr := io.Copy(io.Discard, resp.Body)
				closeErr := resp.Body.Close()
				local.latencies = append(local.latencies, time.Since(requestStarted))
				local.bytes += n
				local.statusCounts[resp.StatusCode]++
				if readErr != nil {
					local.transportErr++
					addSample(&local.errorSamples, readErr.Error())
				} else if closeErr != nil {
					local.transportErr++
					addSample(&local.errorSamples, closeErr.Error())
				}
			}
			results[worker] = local
		}(worker)
	}
	close(startGate)
	wg.Wait()
	elapsed := time.Since(started)

	merged := workerResult{statusCounts: make(map[int]int64)}
	for _, local := range results {
		merged.latencies = append(merged.latencies, local.latencies...)
		merged.bytes += local.bytes
		merged.transportErr += local.transportErr
		for status, count := range local.statusCounts {
			merged.statusCounts[status] += count
		}
		for _, sample := range local.errorSamples {
			addSample(&merged.errorSamples, sample)
		}
	}

	total := int64(len(merged.latencies))
	var successful int64
	statusCounts := make(map[string]int64, len(merged.statusCounts))
	for status, count := range merged.statusCounts {
		statusCounts[strconv.Itoa(status)] = count
		if status >= 200 && status < 400 {
			successful += count
		}
	}
	successRate := 0.0
	if total > 0 {
		successRate = float64(successful) / float64(total)
	}
	out := report{
		Scenario:        *scenario,
		Method:          *method,
		Concurrency:     *concurrency,
		MaxRequests:     *maxRequests,
		ElapsedSeconds:  elapsed.Seconds(),
		Requests:        total,
		RPS:             float64(total) / elapsed.Seconds(),
		SuccessRate:     successRate,
		TransportErrors: merged.transportErr,
		StatusCounts:    statusCounts,
		Bytes:           merged.bytes,
		BytesPerSecond:  float64(merged.bytes) / elapsed.Seconds(),
		LatencyMs:       summarizeLatency(merged.latencies),
		ErrorSamples:    merged.errorSamples,
	}
	if *maxRequests == 0 {
		out.RequestedWindow = duration.String()
	}
	writer, closeOutput, err := outputWriter(*output, *appendOutput)
	if err != nil {
		exitf("open output: %v", err)
	}
	defer closeOutput()
	encoder := json.NewEncoder(writer)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(out); err != nil {
		exitf("encode report: %v", err)
	}
}

func outputWriter(path string, appendOutput bool) (io.Writer, func(), error) {
	if path == "" {
		return os.Stdout, func() {}, nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, func() {}, err
	}
	flags := os.O_CREATE | os.O_WRONLY | os.O_TRUNC
	if appendOutput {
		flags = os.O_CREATE | os.O_WRONLY | os.O_APPEND
	}
	file, err := os.OpenFile(path, flags, 0o644)
	if err != nil {
		return nil, func() {}, err
	}
	return file, func() { _ = file.Close() }, nil
}

func resolveURL(base, path string) (string, error) {
	parsed, err := url.Parse(path)
	if err != nil {
		return "", err
	}
	if parsed.IsAbs() {
		return parsed.String(), nil
	}
	root, err := url.Parse(strings.TrimRight(base, "/") + "/")
	if err != nil {
		return "", err
	}
	return root.ResolveReference(parsed).String(), nil
}

func tileGridPaths(spec string) ([]string, error) {
	parts := strings.Split(spec, ":")
	if len(parts) != 3 {
		return nil, fmt.Errorf("want layer:z:count")
	}
	layer := parts[0]
	z, err := strconv.Atoi(parts[1])
	if err != nil || z < 0 || z > 22 {
		return nil, fmt.Errorf("zoom must be between 0 and 22")
	}
	count, err := strconv.Atoi(parts[2])
	if err != nil || count < 1 {
		return nil, fmt.Errorf("count must be positive")
	}
	side := 1 << z
	total := side * side
	if count > total {
		return nil, fmt.Errorf("count %d exceeds %d unique tiles at z=%d", count, total, z)
	}
	paths := make([]string, 0, count)
	// The odd multiplier is coprime with the power-of-two grid size, yielding
	// a deterministic permutation instead of a spatially clustered scan.
	for i := 0; i < count; i++ {
		index := (i*40503 + 7919) % total
		x, y := index%side, index/side
		paths = append(paths, fmt.Sprintf("/tiles/%s/%d/%d/%d.pbf", layer, z, x, y))
	}
	return paths, nil
}

func summarizeLatency(values []time.Duration) latencyReport {
	if len(values) == 0 {
		return latencyReport{}
	}
	sort.Slice(values, func(i, j int) bool { return values[i] < values[j] })
	var sum float64
	for _, value := range values {
		sum += float64(value) / float64(time.Millisecond)
	}
	return latencyReport{
		Min:  milliseconds(values[0]),
		Mean: sum / float64(len(values)),
		P50:  percentile(values, 0.50),
		P95:  percentile(values, 0.95),
		P99:  percentile(values, 0.99),
		Max:  milliseconds(values[len(values)-1]),
	}
}

func percentile(values []time.Duration, quantile float64) float64 {
	if len(values) == 1 {
		return milliseconds(values[0])
	}
	position := quantile * float64(len(values)-1)
	lower := int(math.Floor(position))
	upper := int(math.Ceil(position))
	if lower == upper {
		return milliseconds(values[lower])
	}
	weight := position - float64(lower)
	return milliseconds(values[lower])*(1-weight) + milliseconds(values[upper])*weight
}

func milliseconds(value time.Duration) float64 {
	return float64(value) / float64(time.Millisecond)
}

func addSample(samples *[]string, sample string) {
	if len(*samples) < 5 {
		*samples = append(*samples, sample)
	}
}

func exitf(format string, args ...interface{}) {
	_, _ = fmt.Fprintf(os.Stderr, format+"\n", args...)
	os.Exit(2)
}
