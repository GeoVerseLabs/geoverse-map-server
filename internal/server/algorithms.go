package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/GeoVerseLabs/geoverse-map-server/internal/algo"
)

const maxAlgoBodyBytes = 10 << 20 // GPS traces can be sizeable

// handleAlgorithms serves GET /algorithms: every registered algorithm's
// descriptor plus the configured networks.
func (s *Server) handleAlgorithms(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, "application/json", map[string]interface{}{
		"algorithms": s.algos.Describe(),
		"networks":   s.env.Networks.Names(),
		"links": []link{
			{Href: s.baseURL(r) + "/algorithms", Rel: "self", Type: "application/json"},
		},
	})
}

// handleAlgorithmGet serves GET /algorithms/{name}: one descriptor.
func (s *Server) handleAlgorithmGet(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	a, ok := s.algos.Get(name)
	if !ok {
		writeError(w, http.StatusNotFound, fmt.Sprintf("unknown algorithm %q", name))
		return
	}
	writeJSON(w, http.StatusOK, "application/json", a.Describe())
}

// handleAlgorithmRun serves POST /algorithms/{name} with JSON params.
func (s *Server) handleAlgorithmRun(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	a, ok := s.algos.Get(name)
	if !ok {
		writeError(w, http.StatusNotFound, fmt.Sprintf("unknown algorithm %q", name))
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, maxAlgoBodyBytes))
	if err != nil {
		writeError(w, http.StatusBadRequest, "read request body failed")
		return
	}
	if len(body) == 0 {
		body = []byte("{}")
	}
	started := time.Now()
	result, err := a.Run(r.Context(), s.env, json.RawMessage(body))
	elapsed := time.Since(started)
	if err != nil {
		var ue *algo.UserError
		if errors.As(err, &ue) {
			s.logAlgorithmRun(name, "http", len(body), elapsed, "rejected", ue.Msg)
			writeError(w, http.StatusBadRequest, ue.Msg)
			return
		}
		// A cancelled run is the client (or the server deadline) giving
		// up, not a server fault: log it as such and skip the response
		// write, which http.TimeoutHandler has already taken over.
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			s.logAlgorithmRun(name, "http", len(body), elapsed, "cancelled", err.Error())
			writeError(w, http.StatusRequestTimeout, "algorithm run was cancelled")
			return
		}
		s.logAlgorithmRun(name, "http", len(body), elapsed, "failed", err.Error())
		s.log.Error("algorithm", "name", name, "error", err)
		writeError(w, http.StatusInternalServerError, "algorithm execution failed")
		return
	}
	s.logAlgorithmRun(name, "http", len(body), elapsed, "ok", "")
	writeJSON(w, http.StatusOK, "application/json", result)
}

// logAlgorithmRun records one algorithm execution. Deliberately a summary
// and not the parameters themselves: a trace or a bbox is business data,
// and an audit trail is not a reason to copy it into the log stream.
func (s *Server) logAlgorithmRun(name, via string, paramBytes int, elapsed time.Duration, outcome, detail string) {
	attrs := []interface{}{
		"algorithm", name,
		"via", via,
		"param_bytes", paramBytes,
		"duration_ms", elapsed.Milliseconds(),
		"outcome", outcome,
	}
	if detail != "" {
		attrs = append(attrs, "detail", detail)
	}
	s.log.Info("algorithm run", attrs...)
}
