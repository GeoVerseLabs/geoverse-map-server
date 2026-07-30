package server

import (
	"net/http"
	"sync/atomic"
	"time"
)

// durationBuckets are the cumulative upper bounds (seconds) exported for
// the request-latency histogram. They straddle the range that matters for
// tile serving: a memory-cache hit lands in the first bucket, a cold
// PostGIS ST_AsMVT render in the middle, a timeout in the last.
// Declared as an array rather than a slice so that len() is a constant
// expression and can size the bucket-counter array below.
var durationBuckets = [9]float64{0.001, 0.005, 0.01, 0.05, 0.1, 0.5, 1, 5, 10}

// metrics is a dependency-free counter set scraped by /metrics. It stays
// deliberately small: this server's selling point is "single static binary,
// no runtime dependencies", so pulling in a Prometheus client library to
// export a dozen numbers would be a poor trade. The exposition format is
// plain text and stable, which is all a scraper needs.
type metrics struct {
	started time.Time

	requests   [5]atomic.Uint64 // index = status class: 0=1xx … 4=5xx
	bytesOut   atomic.Uint64
	inFlight   atomic.Int64
	bucketHits [len(durationBuckets) + 1]atomic.Uint64 // last = +Inf
	durSum     atomic.Uint64                           // microseconds, summed
}

func newMetrics() *metrics {
	return &metrics{started: time.Now()}
}

// observe records one finished request.
func (m *metrics) observe(status int, bytes int, d time.Duration) {
	class := status/100 - 1
	if class < 0 || class >= len(m.requests) {
		class = 4
	}
	m.requests[class].Add(1)
	m.bytesOut.Add(uint64(bytes))
	m.durSum.Add(uint64(d.Microseconds()))

	secs := d.Seconds()
	for i, ub := range durationBuckets {
		if secs <= ub {
			m.bucketHits[i].Add(1)
		}
	}
	m.bucketHits[len(durationBuckets)].Add(1)
}

func (m *metrics) uptime() time.Duration { return time.Since(m.started) }

func (m *metrics) totalRequests() uint64 {
	var n uint64
	for i := range m.requests {
		n += m.requests[i].Load()
	}
	return n
}

// metricsMiddleware records every request. It wraps the innermost handler
// so that the numbers describe what clients actually experienced,
// including auth rejections.
func metricsMiddleware(m *metrics, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		m.inFlight.Add(1)
		rec := &statusRecorder{ResponseWriter: w}
		defer func() {
			m.inFlight.Add(-1)
			status := rec.status
			if status == 0 {
				status = http.StatusOK
			}
			m.observe(status, rec.bytes, time.Since(start))
		}()
		next.ServeHTTP(rec, r)
	})
}
