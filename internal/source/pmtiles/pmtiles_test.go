package pmtiles

import (
	"bytes"
	"compress/gzip"
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"

	"github.com/GeoVerseLabs/geoverse-map-server/internal/config"
	"github.com/GeoVerseLabs/geoverse-map-server/internal/source"
)

func appendUvarint(dst []byte, value uint64) []byte {
	var buf [binary.MaxVarintLen64]byte
	n := binary.PutUvarint(buf[:], value)
	return append(dst, buf[:n]...)
}

func gzipBytes(t *testing.T, plain []byte) []byte {
	t.Helper()
	var out bytes.Buffer
	zw := gzip.NewWriter(&out)
	if _, err := zw.Write(plain); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return out.Bytes()
}

func writeFixture(t *testing.T, internalCompression, tileCompression byte) string {
	t.Helper()
	tile := []byte{0x1a, 0x00}
	if tileCompression == compressionGzip {
		tile = gzipBytes(t, tile)
	}
	// One entry for z=1/x=1/y=0 (Hilbert tile id 4), one tile blob at
	// relative offset zero.
	var dir []byte
	for _, v := range []uint64{1, 4, 1, uint64(len(tile)), 1} {
		dir = appendUvarint(dir, v)
	}
	if internalCompression == compressionGzip {
		dir = gzipBytes(t, dir)
	}
	metadata := []byte(`{"name":"Tiny","description":"fixture","vector_layers":[{"id":"roads","fields":{"class":"String"}}]}`)
	if internalCompression == compressionGzip {
		metadata = gzipBytes(t, metadata)
	}
	rootOffset := uint64(headerLength)
	metadataOffset := rootOffset + uint64(len(dir))
	tileOffset := metadataOffset + uint64(len(metadata))
	header := make([]byte, headerLength)
	copy(header[:7], "PMTiles")
	header[7] = 3
	put64 := func(at int, value uint64) { binary.LittleEndian.PutUint64(header[at:at+8], value) }
	put32e7 := func(at int, value float64) {
		binary.LittleEndian.PutUint32(header[at:at+4], uint32(int32(value*1e7)))
	}
	put64(8, rootOffset)
	put64(16, uint64(len(dir)))
	put64(24, metadataOffset)
	put64(32, uint64(len(metadata)))
	put64(40, tileOffset) // empty leaf section starts where tile data starts
	put64(48, 0)
	put64(56, tileOffset)
	put64(64, uint64(len(tile)))
	put64(72, 1)
	put64(80, 1)
	put64(88, 1)
	header[96] = 1
	header[97] = internalCompression
	header[98] = tileCompression
	header[99] = tileTypeMVT
	header[100], header[101] = 1, 1
	put32e7(102, -180)
	put32e7(106, -85)
	put32e7(110, 180)
	put32e7(114, 85)
	header[118] = 1
	put32e7(119, 0)
	put32e7(123, 0)

	path := filepath.Join(t.TempDir(), "tiny.pmtiles")
	archive := append(append(append(header, dir...), metadata...), tile...)
	if err := os.WriteFile(path, archive, 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestSourceReadsHeaderMetadataAndTile(t *testing.T) {
	path := writeFixture(t, compressionGzip, compressionGzip)
	s, err := New(config.Source{Name: "tiny", Type: "pmtiles", Path: path})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	info := s.TileInfo()
	if info.Title != "Tiny" || info.Format != source.FormatMVT || !info.Gzipped {
		t.Fatalf("unexpected info: %+v", info)
	}
	if len(info.VectorLayers) != 1 || info.VectorLayers[0].ID != "roads" {
		t.Fatalf("vector layers: %+v", info.VectorLayers)
	}
	tile, err := s.Tile(t.Context(), 1, 1, 0)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(tile, gzipBytes(t, []byte{0x1a, 0x00})) {
		t.Fatalf("unexpected tile payload: %x", tile)
	}
	if _, err := s.Tile(t.Context(), 1, 0, 0); err != source.ErrTileNotFound {
		t.Fatalf("missing tile error = %v", err)
	}
	if s.ArchivePath() != path || s.ArchiveContentType() != "application/vnd.pmtiles" {
		t.Fatal("archive distribution metadata is wrong")
	}
}

func TestSourceRejectsUnsupportedCompression(t *testing.T) {
	path := writeFixture(t, 3, compressionNone)
	if _, err := New(config.Source{Name: "bad", Type: "pmtiles", Path: path}); err == nil {
		t.Fatal("expected unsupported brotli directory compression to fail")
	}
}

func TestZxyToIDKnownValues(t *testing.T) {
	cases := []struct {
		z, x, y uint32
		want    uint64
	}{
		{0, 0, 0, 0},
		{1, 0, 0, 1},
		{1, 0, 1, 2},
		{1, 1, 1, 3},
		{1, 1, 0, 4},
		{2, 0, 0, 5},
	}
	for _, tc := range cases {
		if got := zxyToID(uint8(tc.z), tc.x, tc.y); got != tc.want {
			t.Errorf("zxyToID(%d,%d,%d)=%d, want %d", tc.z, tc.x, tc.y, got, tc.want)
		}
	}
}
