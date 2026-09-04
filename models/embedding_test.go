package models

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/jmoiron/sqlx"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func embTestGraph(t *testing.T) (db *sqlx.DB, graphID uint32) {
	t.Helper()
	d, err := CreateTempDB()
	require.NoError(t, err)
	t.Cleanup(func() { d.Close() })
	id, err := CreateGraph(d, &Graph{Name: "emb-test"})
	require.NoError(t, err)
	return d, id
}

func embTestNode(t *testing.T, db *sqlx.DB, graphID uint32, typ, text string) uint32 {
	t.Helper()
	b, err := json.Marshal(map[string]string{"text": text})
	require.NoError(t, err)
	props := json.RawMessage(b)
	var tp *string
	if typ != "" {
		tp = &typ
	}
	id, err := CreateNode(db, &Node{GraphID: graphID, Type: tp, Properties: &props})
	require.NoError(t, err)
	return id
}

func TestNodeVersionBumps(t *testing.T) {
	db, graphID := embTestGraph(t)
	v0, err := NodeVersion(db, graphID)
	require.NoError(t, err)
	assert.Equal(t, int64(0), v0, "untouched graph reads version 0")

	n1 := embTestNode(t, db, graphID, "", "alpha")
	v1, err := NodeVersion(db, graphID)
	require.NoError(t, err)
	assert.Greater(t, v1, v0, "insert must bump")

	node, err := GetNode(db, n1)
	require.NoError(t, err)
	require.NoError(t, UpdateNode(db, node))
	v2, _ := NodeVersion(db, graphID)
	assert.Greater(t, v2, v1, "update must bump")

	require.NoError(t, DeleteNode(db, n1))
	v3, _ := NodeVersion(db, graphID)
	assert.Greater(t, v3, v2, "delete must bump")

	require.NoError(t, DeleteGraph(db, graphID))
	v4, _ := NodeVersion(db, graphID)
	assert.Equal(t, int64(0), v4, "graph delete clears the counter")
}

func TestNodesUpdatedSince(t *testing.T) {
	db, graphID := embTestGraph(t)
	n1 := embTestNode(t, db, graphID, "", "one")
	first, err := GetNode(db, n1)
	require.NoError(t, err)

	all, err := NodesUpdatedSince(db, graphID, time.Unix(0, 0).UTC())
	require.NoError(t, err)
	require.Len(t, all, 1)

	// Nothing is newer than the newest row's own timestamp + 1ns.
	after, err := NodesUpdatedSince(db, graphID, first.UpdatedAt.Add(time.Nanosecond))
	require.NoError(t, err)
	assert.Empty(t, after)

	boundary, err := NodesUpdatedSince(db, graphID, first.UpdatedAt)
	require.NoError(t, err)
	require.Len(t, boundary, 1)
	assert.Equal(t, first.UpdatedAt.UnixNano(), boundary[0].UpdatedAt.UnixNano(),
		"updated_at must round-trip through the driver exactly")

	require.NoError(t, UpdateNode(db, first))
	after, err = NodesUpdatedSince(db, graphID, first.UpdatedAt.Add(time.Nanosecond))
	require.NoError(t, err)
	require.Len(t, after, 1, "an update must move the node past the old watermark")
}

func TestSearchText(t *testing.T) {
	props := json.RawMessage(`{"z": "last", "a": "first", "nested": {"b": "deep"}, "list": ["x", 2], "n": 7}`)
	typ := "memory"
	n := &Node{Type: &typ, Properties: &props}
	assert.Equal(t, "memory first x deep last", n.SearchText())

	empty := &Node{}
	assert.Equal(t, "", empty.SearchText())
}

func TestSearchNodesLexicalProse(t *testing.T) {
	db, graphID := embTestGraph(t)
	embTestNode(t, db, graphID, "", "the expense report for the business trip")
	embTestNode(t, db, graphID, "", "gardening at the weekend")

	nodes, ranks, err := SearchNodesLexical(db, graphID, "what is a business expense?", "", 10)
	require.NoError(t, err)
	require.Len(t, nodes, 1)
	assert.Len(t, ranks, len(nodes))
	assert.Contains(t, nodes[0].SearchText(), "expense")

	// An all-stopword query keeps its tokens rather than matching nothing.
	nodes, _, err = SearchNodesLexical(db, graphID, "the the?", "", 10)
	require.NoError(t, err)
	assert.Len(t, nodes, 2)

	// Pure punctuation has no tokens: empty result, no error.
	nodes, _, err = SearchNodesLexical(db, graphID, "???", "", 10)
	require.NoError(t, err)
	assert.Empty(t, nodes)
}
