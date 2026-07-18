// Package tilemath implements Web Mercator (EPSG:3857 / WebMercatorQuad)
// tile arithmetic shared by all tile handlers and sources.
package tilemath

import (
	"fmt"
	"math"
)

const (
	// EarthRadius is the WGS84 spherical radius used by Web Mercator.
	EarthRadius = 6378137.0
	// MaxLat is the latitude limit of Web Mercator.
	MaxLat = 85.05112877980659
	// WorldExtent is half the width of the 3857 world in meters.
	WorldExtent = math.Pi * EarthRadius
	// MaxZoom supported by the service.
	MaxZoom = 24
)

// Tile identifies a single tile in the WebMercatorQuad scheme (XYZ, y down).
type Tile struct {
	Z, X, Y uint32
}

func (t Tile) String() string { return fmt.Sprintf("%d/%d/%d", t.Z, t.X, t.Y) }

// Valid reports whether x/y are within range for zoom z.
func (t Tile) Valid() bool {
	if t.Z > MaxZoom {
		return false
	}
	n := uint32(1) << t.Z
	return t.X < n && t.Y < n
}

// FlipY converts between XYZ and TMS row order at the tile's zoom.
func (t Tile) FlipY() Tile {
	return Tile{Z: t.Z, X: t.X, Y: (uint32(1) << t.Z) - 1 - t.Y}
}

// Bounds3857 returns the tile envelope in EPSG:3857 meters (minx, miny, maxx, maxy).
func (t Tile) Bounds3857() (float64, float64, float64, float64) {
	n := float64(uint64(1) << t.Z)
	size := 2 * WorldExtent / n
	minx := -WorldExtent + float64(t.X)*size
	maxy := WorldExtent - float64(t.Y)*size
	return minx, maxy - size, minx + size, maxy
}

// Bounds4326 returns the tile envelope in WGS84 degrees (west, south, east, north).
func (t Tile) Bounds4326() (float64, float64, float64, float64) {
	minx, miny, maxx, maxy := t.Bounds3857()
	w, s := MetersToLonLat(minx, miny)
	e, n := MetersToLonLat(maxx, maxy)
	return w, s, e, n
}

// LonLatToMeters projects WGS84 degrees to EPSG:3857 meters.
func LonLatToMeters(lon, lat float64) (float64, float64) {
	lat = math.Max(-MaxLat, math.Min(MaxLat, lat))
	x := lon * WorldExtent / 180
	y := math.Log(math.Tan((90+lat)*math.Pi/360)) * EarthRadius
	return x, y
}

// MetersToLonLat unprojects EPSG:3857 meters to WGS84 degrees.
func MetersToLonLat(x, y float64) (float64, float64) {
	lon := x / WorldExtent * 180
	lat := math.Atan(math.Sinh(y/EarthRadius)) * 180 / math.Pi
	return lon, lat
}
