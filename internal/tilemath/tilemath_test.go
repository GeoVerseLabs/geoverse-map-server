package tilemath

import (
	"math"
	"testing"
)

func almostEqual(a, b, eps float64) bool { return math.Abs(a-b) < eps }

func TestTileValid(t *testing.T) {
	cases := []struct {
		tile Tile
		want bool
	}{
		{Tile{0, 0, 0}, true},
		{Tile{0, 1, 0}, false},
		{Tile{5, 31, 31}, true},
		{Tile{5, 32, 0}, false},
		{Tile{25, 0, 0}, false},
	}
	for _, c := range cases {
		if got := c.tile.Valid(); got != c.want {
			t.Errorf("%v.Valid() = %v, want %v", c.tile, got, c.want)
		}
	}
}

func TestFlipY(t *testing.T) {
	tile := Tile{Z: 3, X: 2, Y: 1}
	if got := tile.FlipY(); got.Y != 6 {
		t.Errorf("FlipY: got y=%d, want 6", got.Y)
	}
	if got := tile.FlipY().FlipY(); got != tile {
		t.Errorf("FlipY twice: got %v, want %v", got, tile)
	}
}

func TestBounds3857World(t *testing.T) {
	minx, miny, maxx, maxy := Tile{0, 0, 0}.Bounds3857()
	if !almostEqual(minx, -WorldExtent, 1e-6) || !almostEqual(maxx, WorldExtent, 1e-6) ||
		!almostEqual(miny, -WorldExtent, 1e-6) || !almostEqual(maxy, WorldExtent, 1e-6) {
		t.Errorf("world tile bounds wrong: %v %v %v %v", minx, miny, maxx, maxy)
	}
}

func TestBounds4326(t *testing.T) {
	// Tile 1/0/0 covers the north-west quadrant.
	w, s, e, n := (Tile{1, 0, 0}).Bounds4326()
	if !almostEqual(w, -180, 1e-9) || !almostEqual(e, 0, 1e-9) {
		t.Errorf("lon range: got [%v, %v], want [-180, 0]", w, e)
	}
	if !almostEqual(s, 0, 1e-9) || !almostEqual(n, MaxLat, 1e-6) {
		t.Errorf("lat range: got [%v, %v], want [0, %v]", s, n, MaxLat)
	}
}

func TestRoundTripProjection(t *testing.T) {
	lon, lat := 116.397, 39.909 // Beijing
	x, y := LonLatToMeters(lon, lat)
	lon2, lat2 := MetersToLonLat(x, y)
	if !almostEqual(lon, lon2, 1e-9) || !almostEqual(lat, lat2, 1e-9) {
		t.Errorf("round trip drifted: (%v,%v) -> (%v,%v)", lon, lat, lon2, lat2)
	}
}
