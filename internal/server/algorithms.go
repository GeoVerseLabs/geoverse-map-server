package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"

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
	result, err := a.Run(r.Context(), s.env, json.RawMessage(body))
	if err != nil {
		var ue *algo.UserError
		if errors.As(err, &ue) {
			writeError(w, http.StatusBadRequest, ue.Msg)
			return
		}
		s.log.Error("algorithm", "name", name, "error", err)
		writeError(w, http.StatusInternalServerError, "algorithm execution failed")
		return
	}
	writeJSON(w, http.StatusOK, "application/json", result)
}
