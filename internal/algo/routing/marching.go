package routing

import (
	"math"
	"sort"

	"github.com/paulmach/orb"
	"github.com/paulmach/orb/planar"
	"github.com/paulmach/orb/simplify"
)

// binaryGrid is a boolean raster used to turn a cloud of reached points /
// segments into contour polygons. Sample (x, y) maps to a lon/lat via the
// linear transform (minX + x*cw, minY + y*ch).
type binaryGrid struct {
	nx, ny     int
	minX, minY float64
	cw, ch     float64 // cell size in degrees
	v          []bool
}

func newBinaryGrid(nx, ny int, minX, minY, cw, ch float64) *binaryGrid {
	return &binaryGrid{nx: nx, ny: ny, minX: minX, minY: minY, cw: cw, ch: ch, v: make([]bool, nx*ny)}
}

func (g *binaryGrid) at(x, y int) bool {
	if x < 0 || y < 0 || x >= g.nx || y >= g.ny {
		return false
	}
	return g.v[y*g.nx+x]
}

func (g *binaryGrid) set(x, y int) {
	if x >= 0 && y >= 0 && x < g.nx && y < g.ny {
		g.v[y*g.nx+x] = true
	}
}

func (g *binaryGrid) markPoint(p orb.Point) {
	g.set(int(math.Round((p[0]-g.minX)/g.cw)), int(math.Round((p[1]-g.minY)/g.ch)))
}

// markSegment rasterizes the straight segment a-b by sampling at
// half-cell steps, keeping traversed corridors connected on the grid.
func (g *binaryGrid) markSegment(a, b orb.Point) {
	steps := int(math.Max(math.Abs(b[0]-a[0])/g.cw, math.Abs(b[1]-a[1])/g.ch)*2) + 1
	for i := 0; i <= steps; i++ {
		t := float64(i) / float64(steps)
		g.markPoint(orb.Point{a[0] + (b[0]-a[0])*t, a[1] + (b[1]-a[1])*t})
	}
}

// dilate grows the marked area by one cell (8-neighborhood), closing
// pixel-sized gaps between nearby corridors before contouring.
func (g *binaryGrid) dilate() {
	out := make([]bool, len(g.v))
	for y := 0; y < g.ny; y++ {
		for x := 0; x < g.nx; x++ {
			if !g.v[y*g.nx+x] {
				continue
			}
			for dy := -1; dy <= 1; dy++ {
				for dx := -1; dx <= 1; dx++ {
					xx, yy := x+dx, y+dy
					if xx >= 0 && yy >= 0 && xx < g.nx && yy < g.ny {
						out[yy*g.nx+xx] = true
					}
				}
			}
		}
	}
	g.v = out
}

// vkey identifies a contour vertex on the doubled integer lattice, so
// segment endpoints hash exactly.
type vkey struct{ x, y int }

type contourSeg struct{ a, b vkey }

// contours extracts closed rings separating marked from unmarked samples
// using marching squares (midpoint variant) and chains the segments into
// rings. Holes come out as separate rings and are nested afterwards.
func (g *binaryGrid) contours() []orb.Ring {
	var segs []contourSeg
	// Iterate 2x2 sample blocks; virtual -1/n border is all false.
	for y := -1; y < g.ny; y++ {
		for x := -1; x < g.nx; x++ {
			code := 0
			if g.at(x, y) {
				code |= 1 // top-left
			}
			if g.at(x+1, y) {
				code |= 2 // top-right
			}
			if g.at(x+1, y+1) {
				code |= 4 // bottom-right
			}
			if g.at(x, y+1) {
				code |= 8 // bottom-left
			}
			// Edge midpoints on the doubled lattice.
			T := vkey{2*x + 1, 2 * y}
			B := vkey{2*x + 1, 2*y + 2}
			L := vkey{2 * x, 2*y + 1}
			R := vkey{2*x + 2, 2*y + 1}
			switch code {
			case 1, 14:
				segs = append(segs, contourSeg{L, T})
			case 2, 13:
				segs = append(segs, contourSeg{T, R})
			case 3, 12:
				segs = append(segs, contourSeg{L, R})
			case 4, 11:
				segs = append(segs, contourSeg{R, B})
			case 5:
				segs = append(segs, contourSeg{L, T}, contourSeg{R, B})
			case 6, 9:
				segs = append(segs, contourSeg{T, B})
			case 7, 8:
				segs = append(segs, contourSeg{B, L})
			case 10:
				segs = append(segs, contourSeg{T, R}, contourSeg{B, L})
			}
		}
	}
	return g.chain(segs)
}

func (g *binaryGrid) toLonLat(v vkey) orb.Point {
	// Doubled-lattice units are half-cells; midpoints sit between samples.
	return orb.Point{
		g.minX + float64(v.x)/2*g.cw,
		g.minY + float64(v.y)/2*g.ch,
	}
}

func (g *binaryGrid) chain(segs []contourSeg) []orb.Ring {
	adj := map[vkey][]vkey{}
	for _, s := range segs {
		adj[s.a] = append(adj[s.a], s.b)
		adj[s.b] = append(adj[s.b], s.a)
	}
	used := map[[2]vkey]bool{}
	edgeUsed := func(a, b vkey) bool { return used[[2]vkey{a, b}] || used[[2]vkey{b, a}] }
	markUsed := func(a, b vkey) { used[[2]vkey{a, b}] = true }

	var rings []orb.Ring
	for _, s := range segs {
		if edgeUsed(s.a, s.b) {
			continue
		}
		ring := orb.Ring{g.toLonLat(s.a), g.toLonLat(s.b)}
		markUsed(s.a, s.b)
		prev, cur := s.a, s.b
		for cur != s.a {
			advanced := false
			for _, next := range adj[cur] {
				if next == prev || edgeUsed(cur, next) {
					continue
				}
				markUsed(cur, next)
				ring = append(ring, g.toLonLat(next))
				prev, cur = cur, next
				advanced = true
				break
			}
			if !advanced {
				break // open chain (should not happen on a valid field)
			}
		}
		if len(ring) >= 4 && cur == s.a {
			rings = append(rings, ring)
		}
	}
	return rings
}

// assemblePolygons nests rings into polygons: a ring contained in an odd
// number of larger rings is a hole of its smallest containing shell.
func assemblePolygons(rings []orb.Ring, simplifyTolDeg float64) orb.MultiPolygon {
	if simplifyTolDeg > 0 {
		ds := simplify.DouglasPeucker(simplifyTolDeg)
		kept := rings[:0]
		for _, r := range rings {
			r = ds.Ring(r)
			if len(r) >= 4 {
				kept = append(kept, r)
			}
		}
		rings = kept
	}
	sort.Slice(rings, func(i, j int) bool {
		return math.Abs(planar.Area(rings[i])) > math.Abs(planar.Area(rings[j]))
	})
	type shell struct {
		outer orb.Ring
		holes []orb.Ring
	}
	var shells []*shell
	shellIdx := map[int]int{} // ring index -> shells index
	for i, r := range rings {
		depth := 0
		parent := -1
		for j := i - 1; j >= 0; j-- {
			if ringContains(rings[j], r[0]) {
				depth++
				if parent == -1 {
					parent = j
				}
			}
		}
		if depth%2 == 0 {
			shellIdx[i] = len(shells)
			shells = append(shells, &shell{outer: r})
		} else if si, ok := shellIdx[parent]; ok {
			shells[si].holes = append(shells[si].holes, r)
		}
	}
	mp := make(orb.MultiPolygon, 0, len(shells))
	for _, s := range shells {
		poly := orb.Polygon{closeRing(s.outer)}
		for _, h := range s.holes {
			poly = append(poly, closeRing(h))
		}
		mp = append(mp, poly)
	}
	return mp
}

func closeRing(r orb.Ring) orb.Ring {
	if len(r) > 0 && r[0] != r[len(r)-1] {
		r = append(r, r[0])
	}
	return r
}

func ringContains(r orb.Ring, p orb.Point) bool {
	return planar.RingContains(r, p)
}
