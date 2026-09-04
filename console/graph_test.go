package console

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func decodeGraph(t *testing.T, body string) graphPayload {
	t.Helper()
	var payload graphPayload
	require.NoError(t, json.Unmarshal([]byte(body), &payload))
	return payload
}

func TestGraphServesNodesAndEdges(t *testing.T) {
	s, ts, cg := storeServer(t)
	pid := seedProject(t, cg, "retro", t.TempDir())
	tid := seedTask(t, cg, "draw the knowledge base", "open", pid)

	res, body := get(t, s, ts, "/ui/graph")
	require.Equal(t, http.StatusOK, res.StatusCode)
	payload := decodeGraph(t, body)
	require.Equal(t, overviewGraphName, payload.Name)
	require.False(t, payload.Truncated)

	byID := map[uint32]graphNode{}
	for _, n := range payload.Nodes {
		byID[n.ID] = n
	}
	require.Len(t, byID, 2)
	require.Equal(t, "project", byID[pid].Type)
	require.Equal(t, "retro", byID[pid].Label)
	require.Equal(t, "task", byID[tid].Type)
	require.Equal(t, "draw the knowledge base", byID[tid].Label)
	require.Equal(t, "open", byID[tid].Props["status"])

	require.Len(t, payload.Edges, 1)
	require.Equal(t, graphEdge{Source: tid, Target: pid, Type: "for"}, payload.Edges[0])
}

func TestGraphWithoutStoreIsEmptyNotAnError(t *testing.T) {
	s, ts := testServer(t)
	res, body := get(t, s, ts, "/ui/graph")
	require.Equal(t, http.StatusOK, res.StatusCode)
	payload := decodeGraph(t, body)
	require.Empty(t, payload.Nodes)
	require.Empty(t, payload.Edges)
}

func TestGraphRequiresToken(t *testing.T) {
	_, ts, _ := storeServer(t)
	res, err := http.Get(ts.URL + "/ui/graph")
	require.NoError(t, err)
	res.Body.Close()
	require.Equal(t, http.StatusUnauthorized, res.StatusCode)
}

func TestNodeLabel(t *testing.T) {
	// Identity keys win in preference order.
	require.Equal(t, "retro", nodeLabel("project", map[string]any{"name": "retro", "path": "/x"}))
	require.Equal(t, "fix it", nodeLabel("task", map[string]any{"text": "fix it", "status": "open"}))
	// No identity key: the first string property by sorted key, stably.
	require.Equal(t, "aaa", nodeLabel("thing", map[string]any{"zebra": "zzz", "alpha": "aaa", "num": 3.0}))
	// Nothing usable falls back to the type.
	require.Equal(t, "thing", nodeLabel("thing", map[string]any{"n": 1.0, "type": "thing"}))
	// Long labels truncate with an ellipsis.
	long := strings.Repeat("x", 200)
	got := nodeLabel("note", map[string]any{"title": long})
	require.Equal(t, labelCap+1, len([]rune(got)))
	require.True(t, strings.HasSuffix(got, "…"))
}

func TestGraphDropsEdgesWithMissingEndpoints(t *testing.T) {
	s, _, cg := storeServer(t)
	pid := seedProject(t, cg, "retro", t.TempDir())
	_, err := s.store.CreateGraph("other")
	require.NoError(t, err)
	og, err := s.store.GetGraph("other")
	require.NoError(t, err)
	foreign := seedProject(t, og, "elsewhere", t.TempDir())
	seedEdge(t, cg, "for", pid, foreign)

	payload := s.graphData()
	require.Len(t, payload.Nodes, 1)
	require.Empty(t, payload.Edges)
}
