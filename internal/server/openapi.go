package server

import (
	_ "embed"
	"net/http"
	"strings"

	"gopkg.in/yaml.v3"
)

//go:embed openapi.yaml
var openapiYAML []byte

// openapiDoc decodes the embedded OpenAPI document into a generic map and
// patches info.version to the running build's version, so the document
// doesn't ship a permanent "dev" placeholder. yaml.v3 decodes YAML mappings
// into map[string]interface{} (unlike yaml.v2's map[interface{}]interface{}),
// which is what lets this round-trip straight through encoding/json too.
func openapiDoc() (map[string]interface{}, error) {
	var doc map[string]interface{}
	if err := yaml.Unmarshal(openapiYAML, &doc); err != nil {
		return nil, err
	}
	if info, ok := doc["info"].(map[string]interface{}); ok {
		info["version"] = Version
	}
	return doc, nil
}

// openapiPaths returns the path templates ("/collections/{collectionId}",
// ...) the embedded document declares. TestOpenAPIPathsAreWired uses this
// to assert every declared path resolves to a real handler, not the mux's
// default "page not found" — the document is intentionally a subset of the
// server's routes (it excludes this server's own extension endpoints), so
// the check only runs in this direction.
func openapiPaths() ([]string, error) {
	doc, err := openapiDoc()
	if err != nil {
		return nil, err
	}
	paths, _ := doc["paths"].(map[string]interface{})
	out := make([]string, 0, len(paths))
	for p := range paths {
		out = append(out, p)
	}
	return out, nil
}

// handleOpenAPI serves GET /api. YAML is the canonical embedded
// representation; JSON is derived on demand from the same decoded document
// so the two can never drift from each other.
func (s *Server) handleOpenAPI(w http.ResponseWriter, r *http.Request) {
	doc, err := openapiDoc()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "openapi document is invalid")
		return
	}
	wantJSON := r.URL.Query().Get("f") == "json" || strings.Contains(r.Header.Get("Accept"), "json")
	if !wantJSON {
		out, err := yaml.Marshal(doc)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "openapi document is invalid")
			return
		}
		w.Header().Set("Content-Type", "text/vnd.yaml")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(out)
		return
	}
	writeJSON(w, http.StatusOK, "application/vnd.oai.openapi+json;version=3.0", doc)
}
