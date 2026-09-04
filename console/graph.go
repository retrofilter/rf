package console

import (
	"encoding/json"
	"net/http"
	"sort"
	"unicode/utf8"
)

const graphNodeCap = 2000

type graphNode struct {
	ID    uint32         `json:"id"`
	Type  string         `json:"type"`
	Label string         `json:"label"`
	Props map[string]any `json:"props"`
}

type graphEdge struct {
	Source uint32 `json:"source"`
	Target uint32 `json:"target"`
	Type   string `json:"type"`
}

type graphPayload struct {
	Name      string      `json:"name"`
	Nodes     []graphNode `json:"nodes"`
	Edges     []graphEdge `json:"edges"`
	Truncated bool        `json:"truncated"`
}

func (s *Server) graphData() graphPayload {
	out := graphPayload{Name: overviewGraphName, Nodes: []graphNode{}, Edges: []graphEdge{}}
	if s.store == nil {
		return out
	}
	cg, err := s.store.GetGraph(overviewGraphName)
	if err != nil {
		return out
	}
	nodes, err := cg.GetNodesByGraph(graphNodeCap+1, 0)
	if err != nil {
		return out
	}
	if len(nodes) > graphNodeCap {
		nodes = nodes[:graphNodeCap]
		out.Truncated = true
	}
	present := make(map[uint32]bool, len(nodes))
	for _, n := range nodes {
		typ := ""
		if n.Type != nil {
			typ = *n.Type
		}
		props := n.FormattedProperties()
		out.Nodes = append(out.Nodes, graphNode{
			ID:    n.ID,
			Type:  typ,
			Label: nodeLabel(typ, props),
			Props: props,
		})
		present[n.ID] = true
	}
	edges, err := cg.GetEdgesByGraph(0, 0)
	if err != nil {
		return out
	}
	for _, e := range edges {
		// An edge into the truncated remainder has nowhere to draw to.
		if !present[e.Source] || !present[e.Target] {
			continue
		}
		typ := ""
		if e.Type != nil {
			typ = *e.Type
		}
		out.Edges = append(out.Edges, graphEdge{Source: e.Source, Target: e.Target, Type: typ})
	}
	return out
}

var labelKeys = []string{"name", "text", "title", "content", "description"}

const labelCap = 80

func nodeLabel(typ string, props map[string]any) string {
	for _, k := range labelKeys {
		if v, ok := props[k].(string); ok && v != "" {
			return truncateLabel(v)
		}
	}
	keys := make([]string, 0, len(props))
	for k := range props {
		if k == "type" {
			continue
		}
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if v, ok := props[k].(string); ok && v != "" {
			return truncateLabel(v)
		}
	}
	return typ
}

func truncateLabel(s string) string {
	if utf8.RuneCountInString(s) <= labelCap {
		return s
	}
	runes := []rune(s)
	return string(runes[:labelCap]) + "…"
}

func (s *Server) handleGraph(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(s.graphData())
}
