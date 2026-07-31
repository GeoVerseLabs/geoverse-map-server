// Package pmtiles reads local PMTiles v3 archives as GeoVerse tile sources.
package pmtiles

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"

	"github.com/GeoVerseLabs/geoverse-map-server/internal/config"
	"github.com/GeoVerseLabs/geoverse-map-server/internal/source"
)

const headerLength = 127

const (
	compressionNone = 1
	compressionGzip = 2

	tileTypeMVT  = 1
	tileTypePNG  = 2
	tileTypeJPEG = 3
	tileTypeWebP = 4
)

type header struct {
	rootOffset     uint64
	rootLength     uint64
	metadataOffset uint64
	metadataLength uint64
	leafOffset     uint64
	tileDataOffset uint64
	internalComp   byte
	tileComp       byte
	tileType       byte
	minZoom        byte
	maxZoom        byte
	minLon         float64
	minLat         float64
	maxLon         float64
	maxLat         float64
	centerZoom     byte
	centerLon      float64
	centerLat      float64
}

type directoryEntry struct {
	tileID    uint64
	offset    uint64
	length    uint64
	runLength uint64
}

// Source serves one immutable PMTiles v3 archive.
type Source struct {
	name string
	path string
	file *os.File
	head header
	root []directoryEntry
	info source.TileInfo
}

var (
	_ source.Source        = (*Source)(nil)
	_ source.TileSource    = (*Source)(nil)
	_ source.ArchiveSource = (*Source)(nil)
)

// New opens and validates a local PMTiles v3 archive.
func New(cfg config.Source) (*Source, error) {
	f, err := os.Open(cfg.Path)
	if err != nil {
		return nil, fmt.Errorf("source %q: open PMTiles: %w", cfg.Name, err)
	}
	s := &Source{name: cfg.Name, path: cfg.Path, file: f}
	if err := s.load(cfg); err != nil {
		f.Close()
		return nil, fmt.Errorf("source %q: %w", cfg.Name, err)
	}
	return s, nil
}

func (s *Source) load(cfg config.Source) error {
	raw := make([]byte, headerLength)
	if _, err := s.file.ReadAt(raw, 0); err != nil {
		return fmt.Errorf("read header: %w", err)
	}
	h, err := parseHeader(raw)
	if err != nil {
		return err
	}
	if h.internalComp != compressionNone && h.internalComp != compressionGzip {
		return fmt.Errorf("unsupported directory compression %d (supported: none, gzip)", h.internalComp)
	}
	if h.tileComp != compressionNone && h.tileComp != compressionGzip {
		return fmt.Errorf("unsupported tile compression %d (supported: none, gzip)", h.tileComp)
	}
	format, err := formatForTileType(h.tileType)
	if err != nil {
		return err
	}
	rootRaw, err := s.readSection(h.rootOffset, h.rootLength)
	if err != nil {
		return fmt.Errorf("read root directory: %w", err)
	}
	root, err := decodeDirectory(rootRaw, h.internalComp)
	if err != nil {
		return fmt.Errorf("decode root directory: %w", err)
	}
	metadataRaw, err := s.readSection(h.metadataOffset, h.metadataLength)
	if err != nil {
		return fmt.Errorf("read metadata: %w", err)
	}
	metadataRaw, err = decompress(metadataRaw, h.internalComp)
	if err != nil {
		return fmt.Errorf("decode metadata: %w", err)
	}
	var metadata struct {
		Name         string               `json:"name"`
		Description  string               `json:"description"`
		VectorLayers []source.VectorLayer `json:"vector_layers"`
	}
	if len(metadataRaw) > 0 {
		if err := json.Unmarshal(metadataRaw, &metadata); err != nil {
			return fmt.Errorf("parse metadata: %w", err)
		}
	}
	title := cfg.Title
	if title == "" {
		title = metadata.Name
	}
	if title == "" {
		title = cfg.Name
	}
	description := cfg.Description
	if description == "" {
		description = metadata.Description
	}
	minZoom, maxZoom := int(h.minZoom), int(h.maxZoom)
	if cfg.MinZoom != nil {
		minZoom = *cfg.MinZoom
	}
	if cfg.MaxZoom != nil {
		maxZoom = *cfg.MaxZoom
	}
	s.head = h
	s.root = root
	s.info = source.TileInfo{
		Name:         cfg.Name,
		Title:        title,
		Description:  description,
		Format:       format,
		MinZoom:      minZoom,
		MaxZoom:      maxZoom,
		Bounds:       [4]float64{h.minLon, h.minLat, h.maxLon, h.maxLat},
		Center:       [3]float64{h.centerLon, h.centerLat, float64(h.centerZoom)},
		VectorLayers: metadata.VectorLayers,
		Gzipped:      h.tileComp == compressionGzip,
		Cacheable:    false,
	}
	return nil
}

func parseHeader(raw []byte) (header, error) {
	if len(raw) != headerLength {
		return header{}, fmt.Errorf("header is %d bytes, want %d", len(raw), headerLength)
	}
	if string(raw[:7]) != "PMTiles" || raw[7] != 3 {
		return header{}, errors.New("not a PMTiles v3 archive")
	}
	u64 := func(offset int) uint64 { return binary.LittleEndian.Uint64(raw[offset : offset+8]) }
	i32e7 := func(offset int) float64 {
		return float64(int32(binary.LittleEndian.Uint32(raw[offset:offset+4]))) / 1e7
	}
	h := header{
		rootOffset:     u64(8),
		rootLength:     u64(16),
		metadataOffset: u64(24),
		metadataLength: u64(32),
		leafOffset:     u64(40),
		tileDataOffset: u64(56),
		internalComp:   raw[97],
		tileComp:       raw[98],
		tileType:       raw[99],
		minZoom:        raw[100],
		maxZoom:        raw[101],
		minLon:         i32e7(102),
		minLat:         i32e7(106),
		maxLon:         i32e7(110),
		maxLat:         i32e7(114),
		centerZoom:     raw[118],
		centerLon:      i32e7(119),
		centerLat:      i32e7(123),
	}
	if h.rootLength == 0 {
		return header{}, errors.New("empty root directory")
	}
	if h.minZoom > h.maxZoom {
		return header{}, errors.New("header min zoom exceeds max zoom")
	}
	return h, nil
}

func formatForTileType(tileType byte) (source.TileFormat, error) {
	switch tileType {
	case tileTypeMVT:
		return source.FormatMVT, nil
	case tileTypePNG:
		return source.FormatPNG, nil
	case tileTypeJPEG:
		return source.FormatJPG, nil
	case tileTypeWebP:
		return source.FormatWebP, nil
	default:
		return "", fmt.Errorf("unsupported PMTiles tile type %d", tileType)
	}
}

func (s *Source) readSection(offset, length uint64) ([]byte, error) {
	if length > uint64(^uint(0)>>1) {
		return nil, errors.New("section too large")
	}
	buf := make([]byte, int(length))
	if _, err := s.file.ReadAt(buf, int64(offset)); err != nil {
		return nil, err
	}
	return buf, nil
}

func decompress(raw []byte, compression byte) ([]byte, error) {
	switch compression {
	case compressionNone:
		return raw, nil
	case compressionGzip:
		zr, err := gzip.NewReader(bytes.NewReader(raw))
		if err != nil {
			return nil, err
		}
		defer zr.Close()
		return io.ReadAll(zr)
	default:
		return nil, fmt.Errorf("unsupported compression %d", compression)
	}
}

func decodeDirectory(raw []byte, compression byte) ([]directoryEntry, error) {
	plain, err := decompress(raw, compression)
	if err != nil {
		return nil, err
	}
	r := bytes.NewReader(plain)
	count, err := binary.ReadUvarint(r)
	if err != nil || count == 0 {
		return nil, errors.New("invalid directory entry count")
	}
	if count > 10_000_000 {
		return nil, errors.New("directory entry count is unreasonable")
	}
	entries := make([]directoryEntry, int(count))
	var lastID uint64
	for i := range entries {
		delta, err := binary.ReadUvarint(r)
		if err != nil {
			return nil, fmt.Errorf("tile id %d: %w", i, err)
		}
		lastID += delta
		entries[i].tileID = lastID
	}
	for i := range entries {
		entries[i].runLength, err = binary.ReadUvarint(r)
		if err != nil {
			return nil, fmt.Errorf("run length %d: %w", i, err)
		}
	}
	for i := range entries {
		entries[i].length, err = binary.ReadUvarint(r)
		if err != nil || entries[i].length == 0 {
			return nil, fmt.Errorf("invalid length at entry %d", i)
		}
	}
	var nextOffset uint64
	for i := range entries {
		encoded, err := binary.ReadUvarint(r)
		if err != nil {
			return nil, fmt.Errorf("offset %d: %w", i, err)
		}
		if encoded == 0 && i > 0 {
			entries[i].offset = nextOffset
		} else {
			if encoded == 0 {
				return nil, errors.New("first directory offset cannot use contiguous marker")
			}
			entries[i].offset = encoded - 1
		}
		nextOffset = entries[i].offset + entries[i].length
	}
	return entries, nil
}

func findEntry(entries []directoryEntry, tileID uint64) (directoryEntry, bool) {
	i := sort.Search(len(entries), func(i int) bool { return entries[i].tileID > tileID }) - 1
	if i < 0 {
		return directoryEntry{}, false
	}
	e := entries[i]
	if e.runLength == 0 || tileID < e.tileID+e.runLength {
		return e, true
	}
	return directoryEntry{}, false
}

func (s *Source) tileEntry(tileID uint64) (directoryEntry, bool, error) {
	entries := s.root
	for depth := 0; depth < 4; depth++ {
		e, ok := findEntry(entries, tileID)
		if !ok {
			return directoryEntry{}, false, nil
		}
		if e.runLength > 0 {
			return e, true, nil
		}
		raw, err := s.readSection(s.head.leafOffset+e.offset, e.length)
		if err != nil {
			return directoryEntry{}, false, fmt.Errorf("read leaf directory: %w", err)
		}
		entries, err = decodeDirectory(raw, s.head.internalComp)
		if err != nil {
			return directoryEntry{}, false, fmt.Errorf("decode leaf directory: %w", err)
		}
	}
	return directoryEntry{}, false, errors.New("PMTiles directory nesting exceeds four levels")
}

// Tile returns the compressed bytes stored in the archive.
func (s *Source) Tile(_ context.Context, z, x, y uint32) ([]byte, error) {
	if z > 31 {
		return nil, source.ErrTileNotFound
	}
	entry, ok, err := s.tileEntry(zxyToID(uint8(z), x, y))
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, source.ErrTileNotFound
	}
	data, err := s.readSection(s.head.tileDataOffset+entry.offset, entry.length)
	if err != nil {
		return nil, fmt.Errorf("read tile data: %w", err)
	}
	return data, nil
}

func zxyToID(z uint8, x, y uint32) uint64 {
	acc := (uint64(1)<<(2*z) - 1) / 3
	n := uint32(1) << z
	var d uint64
	for step := n / 2; step > 0; step /= 2 {
		var rx, ry uint32
		if x&step != 0 {
			rx = 1
		}
		if y&step != 0 {
			ry = 1
		}
		d += uint64(step) * uint64(step) * uint64((3*rx)^ry)
		if ry == 0 {
			if rx == 1 {
				x = n - 1 - x
				y = n - 1 - y
			}
			x, y = y, x
		}
	}
	return acc + d
}

func (s *Source) Name() string               { return s.name }
func (s *Source) TileInfo() source.TileInfo  { return s.info }
func (s *Source) ArchivePath() string        { return s.path }
func (s *Source) ArchiveContentType() string { return "application/vnd.pmtiles" }
func (s *Source) Ping(context.Context) error { _, err := s.file.Stat(); return err }
func (s *Source) Close() error               { return s.file.Close() }
