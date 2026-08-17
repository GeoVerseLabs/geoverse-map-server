package server

import (
	"fmt"
	"net/http"
)

// handleCollectionTileset serves GET /collections/{id}/tiles: the OGC API -
// Tiles "tileset" resource describing how to fetch this layer's tiles.
func (s *Server) handleCollectionTileset(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	ts, ok := s.reg.TileSource(id)
	if !ok {
		writeError(w, http.StatusNotFound, fmt.Sprintf("layer %q has no tiles", id))
		return
	}
	info := ts.TileInfo()
	title := info.Title
	if title == "" {
		title = info.Name
	}
	base := s.baseURL(r)
	tileURLTemplate := fmt.Sprintf("%s/collections/%s/tiles/%s/{tileMatrix}/{tileRow}/{tileCol}", base, id, webMercatorQuadID)

	limits := make([]map[string]interface{}, 0, info.MaxZoom-info.MinZoom+1)
	for z := info.MinZoom; z <= info.MaxZoom; z++ {
		n := 1<<uint(z) - 1
		limits = append(limits, map[string]interface{}{
			"tileMatrix": fmt.Sprintf("%d", z),
			"minTileRow": 0, "maxTileRow": n,
			"minTileCol": 0, "maxTileCol": n,
		})
	}

	writeJSON(w, http.StatusOK, "application/json", map[string]interface{}{
		"title":               title,
		"dataType":            "map",
		"crs":                 "http://www.opengis.net/def/crs/EPSG/0/3857",
		"tileMatrixSetURI":    fmt.Sprintf("%s/tileMatrixSets/%s", base, webMercatorQuadID),
		"tileMatrixSetLimits": limits,
		"boundingBox": map[string]interface{}{
			"lowerLeft":  [2]float64{info.Bounds[0], info.Bounds[1]},
			"upperRight": [2]float64{info.Bounds[2], info.Bounds[3]},
			"crs":        "http://www.opengis.net/def/crs/OGC/1.3/CRS84",
		},
		"links": []link{
			{Href: base + "/collections/" + id + "/tiles", Rel: "self", Type: "application/json"},
			{Href: tileURLTemplate, Rel: "item", Type: info.Format.ContentType(), Title: "tile template (tileMatrix=z, tileRow=y, tileCol=x)"},
		},
	})
}

// handleStandardTile serves GET
// /collections/{id}/tiles/{tms}/{tileMatrix}/{tileRow}/{tileCol}: the OGC
// API - Tiles Core tile route. It resolves the layer's own format and
// delegates to serveTile so caching, ETags and gzip passthrough behave
// identically to the XYZ route — this is a second door onto the same tile,
// not a second tile pipeline.
func (s *Server) handleStandardTile(w http.ResponseWriter, r *http.Request) {
	layer := r.PathValue("id")
	tmsID := r.PathValue("tms")
	if tmsID != webMercatorQuadID {
		writeError(w, http.StatusNotFound, fmt.Sprintf("unknown tile matrix set %q", tmsID))
		return
	}
	ts, ok := s.reg.TileSource(layer)
	if !ok {
		writeError(w, http.StatusNotFound, fmt.Sprintf("unknown tile layer %q", layer))
		return
	}
	// tileRow is Y, tileCol is X — same ordering as this server's existing
	// WMTS RESTful route, per the OGC Tiles path template
	// {tileMatrix}/{tileRow}/{tileCol}.
	tile, err := parseTile(r.PathValue("tileMatrix"), r.PathValue("tileCol"), r.PathValue("tileRow"))
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	s.serveTile(w, r, layer, tile, string(ts.TileInfo().Format))
}
