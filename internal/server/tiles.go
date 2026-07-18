package server

import (
	"bytes"
	"compress/gzip"
	"errors"
	"fmt"
	"hash/fnv"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/GeoVerseLabs/geoverse-map-server/internal/source"
	"github.com/GeoVerseLabs/geoverse-map-server/internal/tilemath"
)

// handleTile serves GET /tiles/{layer}/{z}/{x}/{y}.{ext}.
func (s *Server) handleTile(w http.ResponseWriter, r *http.Request) {
	layer := r.PathValue("layer")
	yRaw, ext, ok := splitExt(r.PathValue("yext"))
	if !ok {
		writeError(w, http.StatusBadRequest, "tile path must end in .pbf, .mvt, .png, .jpg or .webp")
		return
	}
	tile, err := parseTile(r.PathValue("z"), r.PathValue("x"), yRaw)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	s.serveTile(w, r, layer, tile, ext)
}

func splitExt(last string) (name, ext string, ok bool) {
	i := strings.LastIndexByte(last, '.')
	if i < 0 {
		return "", "", false
	}
	name, ext = last[:i], strings.ToLower(last[i+1:])
	switch ext {
	case "pbf", "mvt", "png", "jpg", "jpeg", "webp":
		return name, ext, true
	}
	return "", "", false
}

func parseTile(zs, xs, ys string) (tilemath.Tile, error) {
	z, err1 := strconv.ParseUint(zs, 10, 32)
	x, err2 := strconv.ParseUint(xs, 10, 32)
	y, err3 := strconv.ParseUint(ys, 10, 32)
	if err1 != nil || err2 != nil || err3 != nil {
		return tilemath.Tile{}, fmt.Errorf("z/x/y must be non-negative integers")
	}
	t := tilemath.Tile{Z: uint32(z), X: uint32(x), Y: uint32(y)}
	if !t.Valid() {
		return t, fmt.Errorf("tile %s out of range", t)
	}
	return t, nil
}

// extMatches reports whether the requested extension fits the source format.
func extMatches(ext string, f source.TileFormat) bool {
	switch f {
	case source.FormatMVT:
		return ext == "pbf" || ext == "mvt"
	case source.FormatPNG:
		return ext == "png"
	case source.FormatJPG:
		return ext == "jpg" || ext == "jpeg"
	case source.FormatWebP:
		return ext == "webp"
	}
	return false
}

func (s *Server) serveTile(w http.ResponseWriter, r *http.Request, layer string, tile tilemath.Tile, ext string) {
	ts, ok := s.reg.TileSource(layer)
	if !ok {
		writeError(w, http.StatusNotFound, fmt.Sprintf("unknown tile layer %q", layer))
		return
	}
	info := ts.TileInfo()
	if !extMatches(ext, info.Format) {
		writeError(w, http.StatusBadRequest,
			fmt.Sprintf("layer %q serves .%s tiles, not .%s", layer, info.Format, ext))
		return
	}
	if tile.Z < uint32(info.MinZoom) || tile.Z > uint32(info.MaxZoom) {
		w.WriteHeader(http.StatusNoContent)
		return
	}

	key := fmt.Sprintf("%s/%s", layer, tile)
	data, hit := s.cache.Get(key)
	if !hit {
		var err error
		data, err = ts.Tile(r.Context(), tile.Z, tile.X, tile.Y)
		if errors.Is(err, source.ErrTileNotFound) {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if err != nil {
			s.log.Error("tile", "layer", layer, "tile", tile.String(), "error", err)
			writeError(w, http.StatusInternalServerError, "tile generation failed")
			return
		}
		if info.Cacheable {
			s.cache.Set(key, data)
		}
	}

	etag := fmt.Sprintf(`"%x"`, fnvSum(data))
	if r.Header.Get("If-None-Match") == etag {
		w.WriteHeader(http.StatusNotModified)
		return
	}

	h := w.Header()
	h.Set("Content-Type", info.Format.ContentType())
	h.Set("ETag", etag)
	h.Set("Cache-Control", "public, max-age=3600")

	// Payload may already be gzip-compressed (memengine output, MBTiles
	// vector tiles). Pass it through when the client accepts gzip,
	// otherwise decompress on the fly.
	if isGzipped(data) {
		if strings.Contains(r.Header.Get("Accept-Encoding"), "gzip") {
			h.Set("Content-Encoding", "gzip")
		} else {
			plain, err := gunzip(data)
			if err != nil {
				writeError(w, http.StatusInternalServerError, "corrupt gzip tile")
				return
			}
			data = plain
		}
	}
	h.Set("Content-Length", strconv.Itoa(len(data)))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(data)
}

func fnvSum(b []byte) uint64 {
	h := fnv.New64a()
	_, _ = h.Write(b)
	return h.Sum64()
}

func isGzipped(b []byte) bool {
	return len(b) > 2 && b[0] == 0x1f && b[1] == 0x8b
}

func gunzip(b []byte) ([]byte, error) {
	zr, err := gzip.NewReader(bytes.NewReader(b))
	if err != nil {
		return nil, err
	}
	defer zr.Close()
	return io.ReadAll(zr)
}

// handleTileJSON serves GET /tiles/{layer}.json (TileJSON 3.0).
func (s *Server) handleTileJSON(w http.ResponseWriter, r *http.Request) {
	name, ok := strings.CutSuffix(r.PathValue("layerjson"), ".json")
	if !ok {
		writeError(w, http.StatusNotFound, "not found; tile metadata lives at /tiles/{layer}.json")
		return
	}
	ts, ok := s.reg.TileSource(name)
	if !ok {
		writeError(w, http.StatusNotFound, fmt.Sprintf("unknown tile layer %q", name))
		return
	}
	info := ts.TileInfo()
	ext := string(info.Format)
	doc := map[string]interface{}{
		"tilejson": "3.0.0",
		"name":     info.Name,
		"tiles": []string{
			fmt.Sprintf("%s/tiles/%s/{z}/{x}/{y}.%s", s.baseURL(r), info.Name, ext),
		},
		"minzoom": info.MinZoom,
		"maxzoom": info.MaxZoom,
		"bounds":  info.Bounds,
		"center":  info.Center,
		"scheme":  "xyz",
	}
	if info.Title != "" {
		doc["description"] = info.Title
	}
	if info.Description != "" {
		doc["description"] = info.Description
	}
	if info.Format == source.FormatMVT {
		doc["format"] = "pbf"
		doc["vector_layers"] = info.VectorLayers
	} else {
		doc["format"] = ext
	}
	writeJSON(w, http.StatusOK, "application/json", doc)
}
