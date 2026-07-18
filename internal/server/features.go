package server

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/paulmach/orb/geojson"

	"github.com/GeoVerseLabs/geoverse-map-server/internal/source"
)

const (
	geoJSONType  = "application/geo+json"
	defaultLimit = 100
	maxLimit     = 10000
)

type link struct {
	Href  string `json:"href"`
	Rel   string `json:"rel"`
	Type  string `json:"type,omitempty"`
	Title string `json:"title,omitempty"`
}

func collectionDoc(base string, ci source.CollectionInfo) map[string]interface{} {
	title := ci.Title
	if title == "" {
		title = ci.Name
	}
	return map[string]interface{}{
		"id":          ci.Name,
		"title":       title,
		"description": ci.Description,
		"extent": map[string]interface{}{
			"spatial": map[string]interface{}{
				"bbox": [][4]float64{ci.Bounds},
				"crs":  "http://www.opengis.net/def/crs/OGC/1.3/CRS84",
			},
		},
		"itemType": "feature",
		"crs":      []string{"http://www.opengis.net/def/crs/OGC/1.3/CRS84"},
		"links": []link{
			{Href: fmt.Sprintf("%s/collections/%s", base, ci.Name), Rel: "self", Type: "application/json"},
			{Href: fmt.Sprintf("%s/collections/%s/items", base, ci.Name), Rel: "items", Type: geoJSONType, Title: title},
		},
	}
}

// handleCollections serves GET /collections.
func (s *Server) handleCollections(w http.ResponseWriter, r *http.Request) {
	base := s.baseURL(r)
	var cols []map[string]interface{}
	for _, fs := range s.reg.FeatureSources() {
		cols = append(cols, collectionDoc(base, fs.CollectionInfo()))
	}
	writeJSON(w, http.StatusOK, "application/json", map[string]interface{}{
		"collections": cols,
		"links": []link{
			{Href: base + "/collections", Rel: "self", Type: "application/json"},
		},
	})
}

// handleCollection serves GET /collections/{id}.
func (s *Server) handleCollection(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	fs, ok := s.reg.FeatureSource(id)
	if !ok {
		writeError(w, http.StatusNotFound, fmt.Sprintf("unknown collection %q", id))
		return
	}
	writeJSON(w, http.StatusOK, "application/json", collectionDoc(s.baseURL(r), fs.CollectionInfo()))
}

func parseBBox(v string) (*[4]float64, error) {
	if v == "" {
		return nil, nil
	}
	parts := strings.Split(v, ",")
	if len(parts) != 4 && len(parts) != 6 {
		return nil, fmt.Errorf("bbox must have 4 (or 6) comma-separated numbers")
	}
	var b [4]float64
	// For a 6-number (3D) bbox, take the horizontal components.
	idx := []int{0, 1, 2, 3}
	if len(parts) == 6 {
		idx = []int{0, 1, 3, 4}
	}
	for i, p := range idx {
		f, err := strconv.ParseFloat(strings.TrimSpace(parts[p]), 64)
		if err != nil {
			return nil, fmt.Errorf("bbox: invalid number %q", parts[p])
		}
		b[i] = f
	}
	if b[0] > b[2] || b[1] > b[3] {
		return nil, fmt.Errorf("bbox: min must not exceed max")
	}
	return &b, nil
}

// handleItems serves GET /collections/{id}/items.
func (s *Server) handleItems(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	fs, ok := s.reg.FeatureSource(id)
	if !ok {
		writeError(w, http.StatusNotFound, fmt.Sprintf("unknown collection %q", id))
		return
	}
	q := source.FeatureQuery{Limit: defaultLimit}
	bbox, err := parseBBox(r.URL.Query().Get("bbox"))
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	q.BBox = bbox
	if v := r.URL.Query().Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 {
			writeError(w, http.StatusBadRequest, "limit must be a positive integer")
			return
		}
		q.Limit = min(n, maxLimit)
	}
	if v := r.URL.Query().Get("offset"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 0 {
			writeError(w, http.StatusBadRequest, "offset must be a non-negative integer")
			return
		}
		q.Offset = n
	}

	res, err := fs.Features(r.Context(), q)
	if err != nil {
		s.log.Error("features", "collection", id, "error", err)
		writeError(w, http.StatusInternalServerError, "feature query failed")
		return
	}

	base := s.baseURL(r)
	links := []link{{
		Href: fmt.Sprintf("%s/collections/%s/items?%s", base, id, r.URL.RawQuery),
		Rel:  "self", Type: geoJSONType,
	}}
	if res.NumberMatched > q.Offset+len(res.Features) {
		next := r.URL.Query()
		next.Set("offset", strconv.Itoa(q.Offset+len(res.Features)))
		next.Set("limit", strconv.Itoa(q.Limit))
		links = append(links, link{
			Href: fmt.Sprintf("%s/collections/%s/items?%s", base, id, next.Encode()),
			Rel:  "next", Type: geoJSONType,
		})
	}

	features := res.Features
	if features == nil {
		features = []*geojson.Feature{}
	}
	writeJSON(w, http.StatusOK, geoJSONType, map[string]interface{}{
		"type":           "FeatureCollection",
		"features":       features,
		"numberMatched":  res.NumberMatched,
		"numberReturned": len(features),
		"links":          links,
	})
}

// handleItem serves GET /collections/{id}/items/{fid}.
func (s *Server) handleItem(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	fid := r.PathValue("fid")
	fs, ok := s.reg.FeatureSource(id)
	if !ok {
		writeError(w, http.StatusNotFound, fmt.Sprintf("unknown collection %q", id))
		return
	}
	f, err := fs.Feature(r.Context(), fid)
	if errors.Is(err, source.ErrFeatureNotFound) {
		writeError(w, http.StatusNotFound, fmt.Sprintf("feature %q not found", fid))
		return
	}
	if err != nil {
		s.log.Error("feature", "collection", id, "fid", fid, "error", err)
		writeError(w, http.StatusInternalServerError, "feature lookup failed")
		return
	}
	writeJSON(w, http.StatusOK, geoJSONType, f)
}
