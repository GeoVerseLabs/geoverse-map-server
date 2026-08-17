package server

import (
	"fmt"
	"math"
	"net/http"
	"strconv"
)

// This server only ever renders Web Mercator tiles (internal/tilemath is
// hardcoded to EPSG:3857), so WebMercatorQuad is the only TileMatrixSet we
// can honestly advertise. Values follow the OGC WebMercatorQuad definition:
// https://docs.ogc.org/is/17-083r4/17-083r4.html#toc30 — a topLeft-origin
// quad tree over the standard Web Mercator extent, 256x256 px tiles,
// levels 0-24.
const (
	webMercatorQuadID = "WebMercatorQuad"
	// earthCircumferenceM is 2*pi*a for the WGS84 semi-major axis, matching
	// the sphere EPSG:3857 projects onto.
	earthCircumferenceM = 2 * math.Pi * 6378137.0
	originM             = earthCircumferenceM / 2 // 20037508.342789244
	// standardizedPixelSizeM is the OGC standard rendering pixel size (0.28mm)
	// used to derive scaleDenominator from ground resolution.
	standardizedPixelSizeM = 0.00028
	tmsTileSizePx          = 256
	webMercatorQuadMaxZoom = 24
)

type tileMatrix struct {
	ID               string     `json:"id"`
	ScaleDenominator float64    `json:"scaleDenominator"`
	CellSize         float64    `json:"cellSize"`
	CornerOfOrigin   string     `json:"cornerOfOrigin"`
	PointOfOrigin    [2]float64 `json:"pointOfOrigin"`
	TileWidth        int        `json:"tileWidth"`
	TileHeight       int        `json:"tileHeight"`
	MatrixWidth      int        `json:"matrixWidth"`
	MatrixHeight     int        `json:"matrixHeight"`
}

func webMercatorQuadMatrices() []tileMatrix {
	out := make([]tileMatrix, 0, webMercatorQuadMaxZoom+1)
	for z := 0; z <= webMercatorQuadMaxZoom; z++ {
		n := 1 << uint(z)
		cellSize := (earthCircumferenceM / tmsTileSizePx) / float64(n)
		out = append(out, tileMatrix{
			ID:               strconv.Itoa(z),
			ScaleDenominator: cellSize / standardizedPixelSizeM,
			CellSize:         cellSize,
			CornerOfOrigin:   "topLeft",
			PointOfOrigin:    [2]float64{-originM, originM},
			TileWidth:        tmsTileSizePx,
			TileHeight:       tmsTileSizePx,
			MatrixWidth:      n,
			MatrixHeight:     n,
		})
	}
	return out
}

// handleTileMatrixSets serves GET /tileMatrixSets.
func (s *Server) handleTileMatrixSets(w http.ResponseWriter, r *http.Request) {
	base := s.baseURL(r)
	writeJSON(w, http.StatusOK, "application/json", map[string]interface{}{
		"tileMatrixSets": []map[string]interface{}{{
			"id": webMercatorQuadID,
			"links": []link{
				{Href: fmt.Sprintf("%s/tileMatrixSets/%s", base, webMercatorQuadID), Rel: "self", Type: "application/json"},
			},
		}},
	})
}

// handleTileMatrixSet serves GET /tileMatrixSets/{id}.
func (s *Server) handleTileMatrixSet(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id != webMercatorQuadID {
		writeError(w, http.StatusNotFound, fmt.Sprintf("unknown tile matrix set %q", id))
		return
	}
	writeJSON(w, http.StatusOK, "application/json", map[string]interface{}{
		"id":           webMercatorQuadID,
		"title":        "Google Maps Compatible for the World",
		"uri":          "http://www.opengis.net/def/tilematrixset/OGC/1.0/WebMercatorQuad",
		"crs":          "http://www.opengis.net/def/crs/EPSG/0/3857",
		"orderedAxes":  []string{"X", "Y"},
		"tileMatrices": webMercatorQuadMatrices(),
	})
}
