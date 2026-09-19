package eval

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/retrofilter/rf/core"
	"github.com/retrofilter/rf/models"
)

const linkEdgeType = "links"

// Ref names one node in the knowledge-base graph: a kind plus a key —
// "task:42", "note:slug", "project:name", or "#42" for any node by id.
type Ref struct {
	Kind string // "task", "note", "project", or "" for a bare "#id"
	Key  string
}

// ParseRef reads a reference string. A bare word with no prefix is a note
// slug — inside [[...]] the common case — and "#N" is any node by id.
func ParseRef(s string) (Ref, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return Ref{}, errors.New("empty reference")
	}
	if strings.HasPrefix(s, "#") {
		if _, err := strconv.ParseUint(s[1:], 10, 32); err != nil {
			return Ref{}, fmt.Errorf("bad reference %q: # must be followed by a node id", s)
		}
		return Ref{Key: s[1:]}, nil
	}
	kind, key, ok := strings.Cut(s, ":")
	if !ok {
		return Ref{Kind: "note", Key: noteSlug(s)}, nil
	}
	key = strings.TrimSpace(key)
	switch kind {
	case "task":
		if _, err := strconv.ParseUint(key, 10, 32); err != nil {
			return Ref{}, fmt.Errorf("bad reference %q: task takes a numeric id", s)
		}
	case "note":
		key = noteSlug(key)
	case "project":
	default:
		return Ref{}, fmt.Errorf("bad reference %q: kinds are task:ID, note:SLUG, project:NAME, or #ID", s)
	}
	if key == "" {
		return Ref{}, fmt.Errorf("bad reference %q: missing key", s)
	}
	return Ref{Kind: kind, Key: key}, nil
}

func (r Ref) String() string {
	if r.Kind == "" {
		return "#" + r.Key
	}
	return r.Kind + ":" + r.Key
}

func resolveRef(cg *core.Graph, r Ref) (*models.Node, error) {
	byID := func() (*models.Node, error) {
		id, _ := strconv.ParseUint(r.Key, 10, 32)
		node, err := cg.GetNode(context.Background(), uint32(id))
		if err != nil {
			return nil, fmt.Errorf("no node %s", r)
		}
		return node, nil
	}
	switch r.Kind {
	case "":
		return byID()
	case "task":
		node, err := byID()
		if err != nil {
			return nil, err
		}
		if node.Type == nil || *node.Type != "task" {
			return nil, fmt.Errorf("%s is not a task", r)
		}
		return node, nil
	case "note", "project":
		nodes, err := cg.GetNodesByAttributes(context.Background(), map[string]interface{}{"type": r.Kind, "name": r.Key})
		if err != nil {
			return nil, err
		}
		if len(nodes) == 0 {
			return nil, fmt.Errorf("no %s", r)
		}
		return nodes[0], nil
	}
	return nil, fmt.Errorf("bad reference %s", r)
}

// RefString is a node's canonical reference: project:name, note:slug,
// task:ID, or #ID for anything else.
func RefString(node *models.Node) string {
	typ := ""
	if node.Type != nil {
		typ = *node.Type
	}
	switch typ {
	case "task":
		return fmt.Sprintf("task:%d", node.ID)
	case "note", "project":
		if name, _ := node.FormattedProperties()["name"].(string); name != "" {
			return typ + ":" + name
		}
	}
	return fmt.Sprintf("#%d", node.ID)
}

var wikilinkRE = regexp.MustCompile(`\[\[([^\]|]+)(?:\|[^\]]*)?\]\]`)

// ExtractRefs returns the [[...]] references in text, in order, deduplicated.
func ExtractRefs(text string) []Ref {
	var refs []Ref
	seen := map[Ref]bool{}
	for _, m := range wikilinkRE.FindAllStringSubmatch(text, -1) {
		r, err := ParseRef(m[1])
		if err != nil || seen[r] {
			continue
		}
		seen[r] = true
		refs = append(refs, r)
	}
	return refs
}

func insertTypedEdge(cg *core.Graph, edgeType string, source, target uint32, extra map[string]interface{}) error {
	props := map[string]interface{}{"type": edgeType}
	for k, v := range extra {
		props[k] = v
	}
	propsJSON, err := json.Marshal(props)
	if err != nil {
		return err
	}
	_, err = cg.InsertEdge(context.Background(), &models.Edge{
		GraphID:    cg.ID,
		Source:     source,
		Target:     target,
		Type:       &edgeType,
		Properties: (*json.RawMessage)(&propsJSON),
	})
	return err
}

func syncLinks(cg *core.Graph, id uint32, text string) ([]Ref, error) {
	edges, err := cg.GetNodeEdges(context.Background(), id)
	if err != nil {
		return nil, err
	}
	for _, e := range edges {
		if e.Source == id && e.Type != nil && *e.Type == linkEdgeType {
			if err := cg.DeleteEdge(context.Background(), e.ID); err != nil {
				return nil, err
			}
		}
	}
	var unresolved []Ref
	linked := map[uint32]bool{}
	for _, r := range ExtractRefs(text) {
		target, err := resolveRef(cg, r)
		if err != nil {
			unresolved = append(unresolved, r)
			continue
		}
		if target.ID == id || linked[target.ID] {
			continue
		}
		linked[target.ID] = true
		if err := insertTypedEdge(cg, linkEdgeType, id, target.ID, map[string]interface{}{"ref": r.String()}); err != nil {
			return nil, err
		}
	}
	return unresolved, nil
}

func relinkMentions(cg *core.Graph, ref Ref) error {
	for _, typ := range []string{"note", "task"} {
		nodes, err := cg.NodesByType(typ, 0)
		if err != nil {
			return err
		}
		for _, node := range nodes {
			text, _ := node.FormattedProperties()["text"].(string)
			for _, r := range ExtractRefs(text) {
				if r == ref {
					if _, err := syncLinks(cg, node.ID, text); err != nil {
						return err
					}
					break
				}
			}
		}
	}
	return nil
}
