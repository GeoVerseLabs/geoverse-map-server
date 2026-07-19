package network

import (
	"container/heap"
	"math"
)

// CostFunc returns the traversal cost of an edge (seconds or meters).
type CostFunc func(e *Edge) float64

// TimeCost prices edges in seconds.
func TimeCost(e *Edge) float64 { return e.TravelSeconds() }

// DistanceCost prices edges in meters.
func DistanceCost(e *Edge) float64 { return e.LengthM }

type pqItem struct {
	node NodeID
	prio float64
}

type pq []pqItem

func (q pq) Len() int            { return len(q) }
func (q pq) Less(i, j int) bool  { return q[i].prio < q[j].prio }
func (q pq) Swap(i, j int)       { q[i], q[j] = q[j], q[i] }
func (q *pq) Push(x interface{}) { *q = append(*q, x.(pqItem)) }
func (q *pq) Pop() interface{} {
	old := *q
	n := len(old)
	it := old[n-1]
	*q = old[:n-1]
	return it
}

// SearchResult holds per-node best costs and predecessor edges after a
// Dijkstra / A* run. Cost is +Inf for unreached nodes.
type SearchResult struct {
	Cost     []float64
	PrevEdge []int32 // incoming edge index on the best path; -1 = none
}

// PathTo reconstructs the edge sequence from the search origin to node t.
func (r *SearchResult) PathTo(g *Graph, t NodeID) []int32 {
	if math.IsInf(r.Cost[t], 1) {
		return nil
	}
	var rev []int32
	for at := t; r.PrevEdge[at] >= 0; {
		ei := r.PrevEdge[at]
		rev = append(rev, ei)
		at = g.Edges[ei].From
	}
	for i, j := 0, len(rev)-1; i < j; i, j = i+1, j-1 {
		rev[i], rev[j] = rev[j], rev[i]
	}
	return rev
}

func newSearchResult(n int) *SearchResult {
	r := &SearchResult{
		Cost:     make([]float64, n),
		PrevEdge: make([]int32, n),
	}
	for i := range r.Cost {
		r.Cost[i] = math.Inf(1)
		r.PrevEdge[i] = -1
	}
	return r
}

// Seed is a search start node with an initial cost, letting callers
// start a search "in the middle of an edge" by seeding both endpoints
// with their partial offsets.
type Seed struct {
	Node NodeID
	Cost float64
}

// Dijkstra explores the graph from `from` until every node cheaper than
// cutoff is settled (cutoff <= 0 means no bound). If target >= 0 the
// search stops as soon as the target is settled.
func (g *Graph) Dijkstra(from NodeID, cost CostFunc, cutoff float64, target NodeID) *SearchResult {
	return g.DijkstraSeeded([]Seed{{Node: from}}, cost, cutoff, target)
}

// DijkstraSeeded is Dijkstra with multiple weighted start nodes.
func (g *Graph) DijkstraSeeded(seeds []Seed, cost CostFunc, cutoff float64, target NodeID) *SearchResult {
	res := newSearchResult(len(g.Nodes))
	q := &pq{}
	for _, s := range seeds {
		if s.Cost < res.Cost[s.Node] {
			res.Cost[s.Node] = s.Cost
			heap.Push(q, pqItem{node: s.Node, prio: s.Cost})
		}
	}
	for q.Len() > 0 {
		it := heap.Pop(q).(pqItem)
		u := it.node
		if it.prio > res.Cost[u] {
			continue // stale entry
		}
		if u == target {
			return res
		}
		for _, ei := range g.Adj[u] {
			e := &g.Edges[ei]
			c := res.Cost[u] + cost(e)
			if cutoff > 0 && c > cutoff {
				continue
			}
			if c < res.Cost[e.To] {
				res.Cost[e.To] = c
				res.PrevEdge[e.To] = ei
				heap.Push(q, pqItem{node: e.To, prio: c})
			}
		}
	}
	return res
}

// AStar finds the least-cost path from `from` to `to` using an admissible
// great-circle heuristic (straight-line distance, divided by the graph's
// maximum speed for time costs), typically settling far fewer nodes than
// Dijkstra on road networks.
func (g *Graph) AStar(from, to NodeID, cost CostFunc, timeBased bool) *SearchResult {
	h := func(n NodeID) float64 {
		d := Haversine(g.Nodes[n].Pt, g.Nodes[to].Pt)
		if timeBased {
			return d / g.MaxSpeedMS
		}
		return d
	}
	res := newSearchResult(len(g.Nodes))
	res.Cost[from] = 0
	q := &pq{{node: from, prio: h(from)}}
	settled := make([]bool, len(g.Nodes))
	for q.Len() > 0 {
		it := heap.Pop(q).(pqItem)
		u := it.node
		if settled[u] {
			continue
		}
		settled[u] = true
		if u == to {
			return res
		}
		for _, ei := range g.Adj[u] {
			e := &g.Edges[ei]
			c := res.Cost[u] + cost(e)
			if c < res.Cost[e.To] {
				res.Cost[e.To] = c
				res.PrevEdge[e.To] = ei
				heap.Push(q, pqItem{node: e.To, prio: c + h(e.To)})
			}
		}
	}
	return res
}
