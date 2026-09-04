package core

import (
	"encoding/json"
	"math"

	"github.com/retrofilter/rf/models"
	"gonum.org/v1/gonum/graph"
)

type snapshot struct {
	nodes map[int64]*snapshotNode
	out   map[int64][]*snapshotEdge
	in    map[int64][]*snapshotEdge
	pair  map[[2]int64]*snapshotEdge // first edge per (from, to)
}

func snapshotGraph(g *Graph) (*snapshot, error) {
	nodes, err := g.GetNodesByGraph(0, 0)
	if err != nil {
		return nil, err
	}
	edges, err := g.GetEdgesByGraph(0, 0)
	if err != nil {
		return nil, err
	}

	s := &snapshot{
		nodes: make(map[int64]*snapshotNode, len(nodes)),
		out:   make(map[int64][]*snapshotEdge),
		in:    make(map[int64][]*snapshotEdge),
		pair:  make(map[[2]int64]*snapshotEdge, len(edges)),
	}
	for _, node := range nodes {
		s.nodes[int64(node.ID)] = &snapshotNode{Node: node}
	}
	for _, edge := range edges {
		from, okFrom := s.nodes[int64(edge.Source)]
		to, okTo := s.nodes[int64(edge.Target)]
		if !okFrom || !okTo {
			continue // dangling edge; skip rather than fail the whole query
		}
		se := &snapshotEdge{
			Edge:   edge,
			from:   from,
			to:     to,
			weight: edgeWeight(edge),
		}
		s.out[from.ID()] = append(s.out[from.ID()], se)
		s.in[to.ID()] = append(s.in[to.ID()], se)
		key := [2]int64{from.ID(), to.ID()}
		if _, exists := s.pair[key]; !exists {
			s.pair[key] = se
		}
	}
	return s, nil
}

func edgeWeight(edge *models.Edge) float64 {
	if edge.Properties == nil {
		return 1.0
	}
	var props map[string]interface{}
	if err := json.Unmarshal(*edge.Properties, &props); err != nil {
		return 1.0
	}
	if w, ok := props["weight"].(float64); ok && w >= 0 {
		return w
	}
	return 1.0
}

type snapshotNode struct {
	*models.Node
}

func (n *snapshotNode) ID() int64 {
	return int64(n.Node.ID)
}

type snapshotEdge struct {
	*models.Edge
	from, to *snapshotNode
	weight   float64
}

func (e *snapshotEdge) From() graph.Node { return e.from }
func (e *snapshotEdge) To() graph.Node   { return e.to }
func (e *snapshotEdge) Weight() float64  { return e.weight }

func (e *snapshotEdge) ReversedEdge() graph.Edge {
	return &snapshotEdge{Edge: e.Edge, from: e.to, to: e.from, weight: e.weight}
}

func (s *snapshot) Node(id int64) graph.Node {
	if n, ok := s.nodes[id]; ok {
		return n
	}
	return nil
}

func (s *snapshot) Nodes() graph.Nodes {
	nodes := make([]graph.Node, 0, len(s.nodes))
	for _, n := range s.nodes {
		nodes = append(nodes, n)
	}
	return &nodeIterator{nodes: nodes, idx: -1}
}

func (s *snapshot) From(id int64) graph.Nodes {
	edges := s.out[id]
	nodes := make([]graph.Node, len(edges))
	for i, e := range edges {
		nodes[i] = e.to
	}
	return &nodeIterator{nodes: nodes, idx: -1}
}

func (s *snapshot) To(id int64) graph.Nodes {
	edges := s.in[id]
	nodes := make([]graph.Node, len(edges))
	for i, e := range edges {
		nodes[i] = e.from
	}
	return &nodeIterator{nodes: nodes, idx: -1}
}

func (s *snapshot) Edge(uid, vid int64) graph.Edge {
	if e, ok := s.pair[[2]int64{uid, vid}]; ok {
		return e
	}
	return nil
}

func (s *snapshot) WeightedEdge(uid, vid int64) graph.WeightedEdge {
	if e, ok := s.pair[[2]int64{uid, vid}]; ok {
		return e
	}
	return nil
}

func (s *snapshot) Weight(uid, vid int64) (float64, bool) {
	if e, ok := s.pair[[2]int64{uid, vid}]; ok {
		return e.weight, true
	}
	return math.Inf(1), false
}

func (s *snapshot) HasEdgeBetween(uid, vid int64) bool {
	_, fwd := s.pair[[2]int64{uid, vid}]
	_, rev := s.pair[[2]int64{vid, uid}]
	return fwd || rev
}

func (s *snapshot) HasEdgeFromTo(uid, vid int64) bool {
	_, ok := s.pair[[2]int64{uid, vid}]
	return ok
}

type nodeIterator struct {
	nodes []graph.Node
	idx   int
}

func (it *nodeIterator) Next() bool {
	it.idx++
	return it.idx < len(it.nodes)
}

func (it *nodeIterator) Node() graph.Node {
	if it.idx >= 0 && it.idx < len(it.nodes) {
		return it.nodes[it.idx]
	}
	return nil
}

func (it *nodeIterator) Len() int {
	return len(it.nodes)
}

func (it *nodeIterator) Reset() {
	it.idx = -1
}
