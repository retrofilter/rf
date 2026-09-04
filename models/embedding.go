package models

import (
	"database/sql"
	"encoding/json"
	"sort"
	"strings"
	"time"

	"github.com/jmoiron/sqlx"
)

// NodeVersion is the per-search freshness probe: one point read of the
// graph's change counter, bumped by every node insert/update/delete via the
// node_version triggers. 0 for a graph nothing ever touched.
func NodeVersion(db *sqlx.DB, graphID uint32) (int64, error) {
	var v int64
	err := db.Get(&v, `SELECT version FROM node_version WHERE graph_id = ?`, graphID)
	if err == sql.ErrNoRows {
		return 0, nil
	}
	return v, err
}

// NodesUpdatedSince returns the graph's nodes created or updated at or after
// the watermark — the cache's delta read, ranged over idx_node_graph_updated.
func NodesUpdatedSince(db *sqlx.DB, graphID uint32, since time.Time) ([]*Node, error) {
	var nodes []*Node
	err := db.Select(&nodes, `
		SELECT id, graph_id, type, properties, created_at, updated_at
		FROM node
		WHERE graph_id = ? AND updated_at >= ?
		ORDER BY updated_at, id
	`, graphID, since)
	return nodes, err
}

// NodeIDs returns every node id in the graph — the delete-reconciliation
// read (covered by idx_node_graph_updated's rowid).
func NodeIDs(db *sqlx.DB, graphID uint32) ([]uint32, error) {
	var ids []uint32
	err := db.Select(&ids, `SELECT id FROM node WHERE graph_id = ?`, graphID)
	return ids, err
}

// GetNodeIDsByType returns the id set of a graph's nodes of one type — the
// embedding scan's native filter.
func GetNodeIDsByType(db *sqlx.DB, graphID uint32, nodeType string) (map[uint32]struct{}, error) {
	var ids []uint32
	if err := db.Select(&ids, `SELECT id FROM node WHERE graph_id = ? AND type = ?`, graphID, nodeType); err != nil {
		return nil, err
	}
	set := make(map[uint32]struct{}, len(ids))
	for _, id := range ids {
		set[id] = struct{}{}
	}
	return set, nil
}

// GetNodesByIDs fetches nodes by id, returned as a map. Chunked to stay far
// under SQLite's bind-parameter limit.
func GetNodesByIDs(db *sqlx.DB, ids []uint32) (map[uint32]*Node, error) {
	out := make(map[uint32]*Node, len(ids))
	const chunk = 500
	for start := 0; start < len(ids); start += chunk {
		end := min(start+chunk, len(ids))
		query, args, err := sqlx.In(`
			SELECT id, graph_id, type, properties, created_at, updated_at
			FROM node WHERE id IN (?)
		`, ids[start:end])
		if err != nil {
			return nil, err
		}
		var nodes []*Node
		if err := db.Select(&nodes, db.Rebind(query), args...); err != nil {
			return nil, err
		}
		for _, n := range nodes {
			out[n.ID] = n
		}
	}
	return out, nil
}

// SearchText returns the text the retrieval arms rank this node by: the type
// plus every string value in the properties JSON at any depth — the same
// population node_fts indexes via json_tree.
func (n *Node) SearchText() string {
	var parts []string
	if n.Type != nil && *n.Type != "" {
		parts = append(parts, *n.Type)
	}
	if n.Properties != nil {
		var v interface{}
		if err := json.Unmarshal(*n.Properties, &v); err == nil {
			collectStrings(v, &parts)
		}
	}
	return strings.Join(parts, " ")
}

func collectStrings(v interface{}, out *[]string) {
	switch t := v.(type) {
	case string:
		*out = append(*out, t)
	case []interface{}:
		for _, e := range t {
			collectStrings(e, out)
		}
	case map[string]interface{}:
		keys := make([]string, 0, len(t))
		for k := range t {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			collectStrings(t[k], out)
		}
	}
}
