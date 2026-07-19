package network

import (
	"math"
	"sort"

	"github.com/paulmach/orb"
)

const gridCells = 128 // cells per axis for the spatial indexes

// metersPerDegree returns approximate meter lengths of one degree of
// longitude and latitude at latitude lat.
func metersPerDegree(lat float64) (lonM, latM float64) {
	latM = 111132.0
	lonM = 111320.0 * math.Cos(lat*math.Pi/180)
	if lonM < 1 {
		lonM = 1
	}
	return
}

type gridExtent struct {
	minX, minY float64
	cw, ch     float64 // cell size in degrees
	nx, ny     int
}

func newGridExtent(min, max orb.Point) gridExtent {
	e := gridExtent{minX: min[0], minY: min[1], nx: gridCells, ny: gridCells}
	w := max[0] - min[0]
	h := max[1] - min[1]
	if w <= 0 {
		w = 1e-6
	}
	if h <= 0 {
		h = 1e-6
	}
	e.cw = w / float64(e.nx)
	e.ch = h / float64(e.ny)
	return e
}

func (e *gridExtent) cell(x, y float64) (int, int) {
	cx := int((x - e.minX) / e.cw)
	cy := int((y - e.minY) / e.ch)
	return clamp(cx, 0, e.nx-1), clamp(cy, 0, e.ny-1)
}

func clamp(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

// pointGrid indexes graph nodes for nearest-node queries.
type pointGrid struct {
	g *Graph
	e gridExtent
	c [][]int32
}

func graphBounds(g *Graph) (orb.Point, orb.Point) {
	min := orb.Point{math.Inf(1), math.Inf(1)}
	max := orb.Point{math.Inf(-1), math.Inf(-1)}
	for i := range g.Nodes {
		p := g.Nodes[i].Pt
		min[0] = math.Min(min[0], p[0])
		min[1] = math.Min(min[1], p[1])
		max[0] = math.Max(max[0], p[0])
		max[1] = math.Max(max[1], p[1])
	}
	return min, max
}

func newPointGrid(g *Graph) *pointGrid {
	min, max := graphBounds(g)
	pg := &pointGrid{g: g, e: newGridExtent(min, max)}
	pg.c = make([][]int32, pg.e.nx*pg.e.ny)
	for i := range g.Nodes {
		cx, cy := pg.e.cell(g.Nodes[i].Pt[0], g.Nodes[i].Pt[1])
		idx := cy*pg.e.nx + cx
		pg.c[idx] = append(pg.c[idx], int32(i))
	}
	return pg
}

func (pg *pointGrid) nearest(p orb.Point, level int16, maxDistM float64) (NodeID, float64, bool) {
	lonM, latM := metersPerDegree(p[1])
	dx := maxDistM / lonM
	dy := maxDistM / latM
	x0, y0 := pg.e.cell(p[0]-dx, p[1]-dy)
	x1, y1 := pg.e.cell(p[0]+dx, p[1]+dy)
	best := NodeID(-1)
	bestD := maxDistM
	for cy := y0; cy <= y1; cy++ {
		for cx := x0; cx <= x1; cx++ {
			for _, id := range pg.c[cy*pg.e.nx+cx] {
				n := &pg.g.Nodes[id]
				if n.Level != level {
					continue
				}
				d := Haversine(p, n.Pt)
				if d <= bestD {
					best, bestD = id, d
				}
			}
		}
	}
	return best, bestD, best >= 0
}

// segGrid indexes undirected segments for nearest-edge queries.
type segGrid struct {
	g *Graph
	e gridExtent
	c [][]int32 // edge indices (one direction per segment)
}

func newSegGrid(g *Graph) *segGrid {
	min, max := graphBounds(g)
	sg := &segGrid{g: g, e: newGridExtent(min, max)}
	sg.c = make([][]int32, sg.e.nx*sg.e.ny)
	for i := range g.Edges {
		e := &g.Edges[i]
		// Index each undirected segment once (keep the direction with
		// From < To; one-way edges have no twin and are always kept).
		if e.From > e.To && hasReverse(g, e) {
			continue
		}
		a, b := g.Nodes[e.From].Pt, g.Nodes[e.To].Pt
		x0, y0 := sg.e.cell(math.Min(a[0], b[0]), math.Min(a[1], b[1]))
		x1, y1 := sg.e.cell(math.Max(a[0], b[0]), math.Max(a[1], b[1]))
		for cy := y0; cy <= y1; cy++ {
			for cx := x0; cx <= x1; cx++ {
				sg.c[cy*sg.e.nx+cx] = append(sg.c[cy*sg.e.nx+cx], int32(i))
			}
		}
	}
	return sg
}

func hasReverse(g *Graph, e *Edge) bool {
	for _, ei := range g.Adj[e.To] {
		r := &g.Edges[ei]
		if r.To == e.From && r.FeatureIdx == e.FeatureIdx {
			return true
		}
	}
	return false
}

// projectOnSegment projects p onto segment ab using a local
// equirectangular approximation. Returns the projection and fraction.
func projectOnSegment(p, a, b orb.Point) (orb.Point, float64) {
	lonM, latM := metersPerDegree(p[1])
	ax, ay := (a[0]-p[0])*lonM, (a[1]-p[1])*latM
	bx, by := (b[0]-p[0])*lonM, (b[1]-p[1])*latM
	dx, dy := bx-ax, by-ay
	den := dx*dx + dy*dy
	t := 0.0
	if den > 0 {
		t = -(ax*dx + ay*dy) / den
	}
	t = math.Max(0, math.Min(1, t))
	return orb.Point{a[0] + (b[0]-a[0])*t, a[1] + (b[1]-a[1])*t}, t
}

func (sg *segGrid) nearby(p orb.Point, level int16, radiusM float64, k int) []EdgeCandidate {
	lonM, latM := metersPerDegree(p[1])
	dx := radiusM / lonM
	dy := radiusM / latM
	x0, y0 := sg.e.cell(p[0]-dx, p[1]-dy)
	x1, y1 := sg.e.cell(p[0]+dx, p[1]+dy)
	seen := map[int32]bool{}
	var out []EdgeCandidate
	for cy := y0; cy <= y1; cy++ {
		for cx := x0; cx <= x1; cx++ {
			for _, ei := range sg.c[cy*sg.e.nx+cx] {
				if seen[ei] {
					continue
				}
				seen[ei] = true
				e := &sg.g.Edges[ei]
				if sg.g.Nodes[e.From].Level != level || sg.g.Nodes[e.To].Level != level {
					continue
				}
				proj, frac := projectOnSegment(p, sg.g.Nodes[e.From].Pt, sg.g.Nodes[e.To].Pt)
				d := Haversine(p, proj)
				if d <= radiusM {
					out = append(out, EdgeCandidate{EdgeIdx: ei, Point: proj, Frac: frac, DistM: d})
				}
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].DistM < out[j].DistM })
	if k > 0 && len(out) > k {
		out = out[:k]
	}
	return out
}
