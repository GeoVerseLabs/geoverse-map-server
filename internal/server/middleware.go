package server

import (
	"crypto/sha256"
	"crypto/subtle"
	"log/slog"
	"net/http"
	"strings"
	"time"
)

type statusRecorder struct {
	http.ResponseWriter
	status int
	bytes  int
}

func (r *statusRecorder) WriteHeader(code int) {
	r.status = code
	r.ResponseWriter.WriteHeader(code)
}

func (r *statusRecorder) Write(b []byte) (int, error) {
	if r.status == 0 {
		r.status = http.StatusOK
	}
	n, err := r.ResponseWriter.Write(b)
	r.bytes += n
	return n, err
}

func logMiddleware(log *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w}
		next.ServeHTTP(rec, r)
		if rec.status == 0 {
			rec.status = http.StatusOK
		}
		log.Info("request",
			"method", r.Method,
			"path", r.URL.Path,
			"status", rec.status,
			"bytes", rec.bytes,
			"duration", time.Since(start).Round(time.Microsecond).String(),
		)
	})
}

func corsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, HEAD, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "*")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// authMiddleware enforces API-key authentication. The key is accepted as
// `Authorization: Bearer <key>`, an `X-API-Key` header, or an `api_key`
// query parameter (for clients like QGIS that can only set URLs).
// /health stays open for load-balancer probes.
func authMiddleware(apiKeys []string, next http.Handler) http.Handler {
	// Compare fixed-size digests so key length is not observable.
	hashes := make([][32]byte, len(apiKeys))
	for i, k := range apiKeys {
		hashes[i] = sha256.Sum256([]byte(k))
	}
	validKey := func(k string) bool {
		h := sha256.Sum256([]byte(k))
		ok := false
		for _, want := range hashes {
			if subtle.ConstantTimeCompare(h[:], want[:]) == 1 {
				ok = true
			}
		}
		return ok
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/health" {
			next.ServeHTTP(w, r)
			return
		}
		key := ""
		if v := r.Header.Get("Authorization"); strings.HasPrefix(v, "Bearer ") {
			key = strings.TrimPrefix(v, "Bearer ")
		} else if v := r.Header.Get("X-API-Key"); v != "" {
			key = v
		} else {
			key = r.URL.Query().Get("api_key")
		}
		if key == "" || !validKey(key) {
			w.Header().Set("WWW-Authenticate", `Bearer realm="geoverse"`)
			writeError(w, http.StatusUnauthorized, "missing or invalid API key")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func recoverMiddleware(log *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if err := recover(); err != nil {
				log.Error("panic", "path", r.URL.Path, "error", err)
				writeError(w, http.StatusInternalServerError, "internal server error")
			}
		}()
		next.ServeHTTP(w, r)
	})
}
