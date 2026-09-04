package core

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/jmoiron/sqlx"
	"github.com/retrofilter/rf/models"
	"github.com/retrofilter/rf/models/schema"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	_ "modernc.org/sqlite"
)

func createTestDB(t *testing.T) *sqlx.DB {
	db, err := sqlx.Open("sqlite", ":memory:")
	require.NoError(t, err)

	// Enable foreign keys
	_, err = db.Exec("PRAGMA foreign_keys = ON")
	require.NoError(t, err)

	// Create tables
	err = schema.CreateTables(db)
	require.NoError(t, err)

	return db
}

func createTestGraph(t *testing.T, db *sqlx.DB, name string) uint32 {
	graph := &models.Graph{
		Name: name,
	}
	graphID, err := models.CreateGraph(db, graph)
	require.NoError(t, err)
	return graphID
}

func createTestNode(t *testing.T, db *sqlx.DB, graphID uint32, nodeType string, properties map[string]interface{}) uint32 {
	node := &models.Node{
		GraphID: graphID,
		Type:    &nodeType,
	}
	if properties != nil {
		propsJSON, err := json.Marshal(properties)
		require.NoError(t, err)
		rawJSON := json.RawMessage(propsJSON)
		node.Properties = &rawJSON
	}
	nodeID, err := models.CreateNode(db, node)
	require.NoError(t, err)
	return nodeID
}

func createTestEdge(t *testing.T, db *sqlx.DB, graphID, source, target uint32, edgeType string, properties map[string]interface{}) uint32 {
	edge := &models.Edge{
		GraphID: graphID,
		Source:  source,
		Target:  target,
		Type:    &edgeType,
	}
	if properties != nil {
		propsJSON, err := json.Marshal(properties)
		require.NoError(t, err)
		rawJSON := json.RawMessage(propsJSON)
		edge.Properties = &rawJSON
	}
	edgeID, err := models.CreateEdge(db, edge)
	require.NoError(t, err)
	return edgeID
}

func TestNewGraphStore(t *testing.T) {
	db := createTestDB(t)
	defer db.Close()

	gs := NewGraphStore(db)
	require.NotNil(t, gs)
}

func TestGetNode(t *testing.T) {
	db := createTestDB(t)
	defer db.Close()

	gs := NewGraphStore(db)
	graphName := "test-graph"
	graphID := createTestGraph(t, db, graphName)
	nodeID := createTestNode(t, db, graphID, "Person", map[string]interface{}{
		"name": "Alice",
		"age":  30,
	})

	cg, err := gs.GetGraph(graphName)
	require.NoError(t, err)

	// Get node
	node, err := cg.GetNode(context.Background(), nodeID)
	require.NoError(t, err)
	require.NotNil(t, node)

	assert.Equal(t, nodeID, node.ID)
	assert.Equal(t, graphID, node.GraphID)
	assert.Equal(t, "Person", *node.Type)
	var props map[string]interface{}
	if node.Properties != nil {
		err := json.Unmarshal(*node.Properties, &props)
		assert.NoError(t, err)
	}
	assert.Equal(t, "Alice", props["name"])
	assert.Equal(t, float64(30), props["age"])
}

func TestNoStaleReads(t *testing.T) {
	db := createTestDB(t)
	defer db.Close()

	gs := NewGraphStore(db)
	graphName := "test-graph"
	graphID := createTestGraph(t, db, graphName)
	nodeID := createTestNode(t, db, graphID, "Person", map[string]interface{}{
		"name": "Bob",
	})

	reader, err := gs.GetGraph(graphName)
	require.NoError(t, err)
	writer, err := gs.GetGraph(graphName)
	require.NoError(t, err)

	// Read once through the reader handle
	node, err := reader.GetNode(context.Background(), nodeID)
	require.NoError(t, err)

	// Update through the writer handle
	props := json.RawMessage(`{"name":"Robert"}`)
	node.Properties = &props
	_, err = writer.UpdateNode(context.Background(), node)
	require.NoError(t, err)

	// The reader handle must see the update immediately
	fresh, err := reader.GetNode(context.Background(), nodeID)
	require.NoError(t, err)
	var got map[string]interface{}
	require.NoError(t, json.Unmarshal(*fresh.Properties, &got))
	assert.Equal(t, "Robert", got["name"])
}

func TestGetEdge(t *testing.T) {
	db := createTestDB(t)
	defer db.Close()

	gs := NewGraphStore(db)
	graphName := "test-graph"
	graphID := createTestGraph(t, db, graphName)
	sourceID := createTestNode(t, db, graphID, "Person", nil)
	targetID := createTestNode(t, db, graphID, "Person", nil)
	edgeID := createTestEdge(t, db, graphID, sourceID, targetID, "Likes", map[string]interface{}{
		"strength": 0.8,
	})

	cg, err := gs.GetGraph(graphName)
	require.NoError(t, err)

	// Get edge
	edge, err := cg.GetEdge(context.Background(), edgeID)
	require.NoError(t, err)
	require.NotNil(t, edge)

	assert.Equal(t, edgeID, edge.ID)
	assert.Equal(t, graphID, edge.GraphID)
	assert.Equal(t, sourceID, edge.Source)
	assert.Equal(t, targetID, edge.Target)
	assert.Equal(t, "Likes", *edge.Type)
	var props map[string]interface{}
	if edge.Properties != nil {
		err := json.Unmarshal(*edge.Properties, &props)
		assert.NoError(t, err)
	}
	assert.Equal(t, 0.8, props["strength"])
}

func TestGetNeighbors(t *testing.T) {
	db := createTestDB(t)
	defer db.Close()

	gs := NewGraphStore(db)
	graphName := "test-graph"
	graphID := createTestGraph(t, db, graphName)

	// Create nodes
	aliceID := createTestNode(t, db, graphID, "Person", map[string]interface{}{"name": "Alice"})
	bobID := createTestNode(t, db, graphID, "Person", map[string]interface{}{"name": "Bob"})
	charlieID := createTestNode(t, db, graphID, "Person", map[string]interface{}{"name": "Charlie"})

	// Create edges: Alice -> Bob, Alice -> Charlie
	createTestEdge(t, db, graphID, aliceID, bobID, "Likes", nil)
	createTestEdge(t, db, graphID, aliceID, charlieID, "Knows", nil)

	cg, err := gs.GetGraph(graphName)
	require.NoError(t, err)

	// Get Alice's neighbors
	neighbors, err := cg.GetNeighbors(context.Background(), aliceID)
	require.NoError(t, err)
	require.Len(t, neighbors, 2)

	// Check that we got Bob and Charlie
	names := make(map[string]bool)
	for _, neighbor := range neighbors {
		var props map[string]interface{}
		if neighbor.Properties != nil {
			err := json.Unmarshal(*neighbor.Properties, &props)
			assert.NoError(t, err)
		}
		if name, ok := props["name"].(string); ok {
			names[name] = true
		}
	}
	assert.True(t, names["Bob"])
	assert.True(t, names["Charlie"])
}

func TestGetIncomingNeighbors(t *testing.T) {
	db := createTestDB(t)
	defer db.Close()

	gs := NewGraphStore(db)
	graphName := "test-graph"
	graphID := createTestGraph(t, db, graphName)

	// Create nodes
	aliceID := createTestNode(t, db, graphID, "Person", map[string]interface{}{"name": "Alice"})
	bobID := createTestNode(t, db, graphID, "Person", map[string]interface{}{"name": "Bob"})
	charlieID := createTestNode(t, db, graphID, "Person", map[string]interface{}{"name": "Charlie"})

	// Create edges: Bob -> Alice, Charlie -> Alice
	createTestEdge(t, db, graphID, bobID, aliceID, "Likes", nil)
	createTestEdge(t, db, graphID, charlieID, aliceID, "Knows", nil)

	cg, err := gs.GetGraph(graphName)
	require.NoError(t, err)

	// Get Alice's incoming neighbors
	neighbors, err := cg.GetIncomingNeighbors(context.Background(), aliceID)
	require.NoError(t, err)
	require.Len(t, neighbors, 2)

	// Check that we got Bob and Charlie
	names := make(map[string]bool)
	for _, neighbor := range neighbors {
		var props map[string]interface{}
		if neighbor.Properties != nil {
			err := json.Unmarshal(*neighbor.Properties, &props)
			assert.NoError(t, err)
		}
		if name, ok := props["name"].(string); ok {
			names[name] = true
		}
	}
	assert.True(t, names["Bob"])
	assert.True(t, names["Charlie"])
}

func TestGetNodesByType(t *testing.T) {
	db := createTestDB(t)
	defer db.Close()

	gs := NewGraphStore(db)
	graphName := "test-graph"
	graphID := createTestGraph(t, db, graphName)

	// Create nodes of different types
	createTestNode(t, db, graphID, "Person", map[string]interface{}{"name": "Alice"})
	createTestNode(t, db, graphID, "Person", map[string]interface{}{"name": "Bob"})
	createTestNode(t, db, graphID, "Company", map[string]interface{}{"name": "Acme Corp"})

	cg, err := gs.GetGraph(graphName)
	require.NoError(t, err)

	// Get all Person nodes
	nodes, err := models.GetNodesByType(db, graphID, "Person", 0, 0)
	require.NoError(t, err)
	var persons []*models.Node
	for _, node := range nodes {
		cachedNode, err := cg.GetNode(context.Background(), node.ID)
		require.NoError(t, err)
		persons = append(persons, cachedNode)
	}
	require.Len(t, persons, 2)

	// Check that we got Alice and Bob
	names := make(map[string]bool)
	for _, person := range persons {
		var props map[string]interface{}
		if person.Properties != nil {
			err := json.Unmarshal(*person.Properties, &props)
			assert.NoError(t, err)
		}
		if name, ok := props["name"].(string); ok {
			names[name] = true
		}
	}
	assert.True(t, names["Alice"])
	assert.True(t, names["Bob"])
}

func TestGetEdgesByType(t *testing.T) {
	db := createTestDB(t)
	defer db.Close()

	gs := NewGraphStore(db)
	graphName := "test-graph"
	graphID := createTestGraph(t, db, graphName)

	// Create nodes
	aliceID := createTestNode(t, db, graphID, "Person", nil)
	bobID := createTestNode(t, db, graphID, "Person", nil)
	charlieID := createTestNode(t, db, graphID, "Person", nil)

	// Create edges of different types
	createTestEdge(t, db, graphID, aliceID, bobID, "Likes", nil)
	createTestEdge(t, db, graphID, bobID, charlieID, "Likes", nil)
	createTestEdge(t, db, graphID, aliceID, charlieID, "Knows", nil)

	cg, err := gs.GetGraph(graphName)
	require.NoError(t, err)

	edges, err := models.GetEdgesByType(db, graphID, "Likes", 0, 0)
	require.NoError(t, err)
	var likesEdges []*models.Edge
	for _, edge := range edges {
		cachedEdge, err := cg.GetEdge(context.Background(), edge.ID)
		require.NoError(t, err)
		likesEdges = append(likesEdges, cachedEdge)
	}
	require.Len(t, likesEdges, 2)

	// Check that we got the right edges
	for _, edge := range likesEdges {
		assert.Equal(t, "Likes", *edge.Type)
	}
}

func TestGetGraphStats(t *testing.T) {
	db := createTestDB(t)
	defer db.Close()

	graphName := "test-graph"
	graphID := createTestGraph(t, db, graphName)

	// Create nodes and edges
	aliceID := createTestNode(t, db, graphID, "Person", map[string]interface{}{"name": "Alice"})
	bobID := createTestNode(t, db, graphID, "Person", map[string]interface{}{"name": "Bob"})
	createTestNode(t, db, graphID, "Company", map[string]interface{}{"name": "Acme Corp"})
	createTestEdge(t, db, graphID, aliceID, bobID, "Likes", nil)

	// Get graph stats
	graph, err := models.GetGraph(db, graphID)
	require.NoError(t, err)
	nodeCount, err := models.CountNodesInGraph(db, graphID)
	require.NoError(t, err)
	edgeCount, err := models.CountEdgesInGraph(db, graphID)
	require.NoError(t, err)
	nodeTypes := make(map[string]int)
	nodes, _ := models.GetNodesByGraph(db, graphID, 0, 0)
	for _, node := range nodes {
		if node.Type != nil {
			nodeTypes[*node.Type]++
		}
	}
	edgeTypes := make(map[string]int)
	edges, _ := models.GetEdgesByGraph(db, graphID, 0, 0)
	for _, edge := range edges {
		if edge.Type != nil {
			edgeTypes[*edge.Type]++
		}
	}
	assert.Equal(t, 3, nodeCount)
	assert.Equal(t, 1, edgeCount)
	assert.Equal(t, "test-graph", graph.Name)
	assert.Equal(t, 2, nodeTypes["Person"])
	assert.Equal(t, 1, nodeTypes["Company"])
	assert.Equal(t, 1, edgeTypes["Likes"])
}

func pathNames(t *testing.T, nodes []*models.Node) []string {
	t.Helper()
	names := make([]string, len(nodes))
	for i, node := range nodes {
		var props map[string]interface{}
		require.NotNil(t, node.Properties)
		require.NoError(t, json.Unmarshal(*node.Properties, &props))
		names[i] = props["name"].(string)
	}
	return names
}

func TestShortestPath(t *testing.T) {
	db := createTestDB(t)
	defer db.Close()

	gs := NewGraphStore(db)
	graphName := "test-graph"
	graphID := createTestGraph(t, db, graphName)

	// a -> b -> d and a -> c -> d, with the b leg cheaper via weights
	person := func(name string) uint32 {
		return createTestNode(t, db, graphID, "Person", map[string]interface{}{"name": name})
	}
	a, b, c, d := person("a"), person("b"), person("c"), person("d")
	createTestEdge(t, db, graphID, a, b, "knows", map[string]interface{}{"weight": 1.0})
	createTestEdge(t, db, graphID, b, d, "knows", map[string]interface{}{"weight": 1.0})
	createTestEdge(t, db, graphID, a, c, "knows", map[string]interface{}{"weight": 5.0})
	createTestEdge(t, db, graphID, c, d, "knows", map[string]interface{}{"weight": 5.0})

	cg, err := gs.GetGraph(graphName)
	require.NoError(t, err)

	path, err := cg.ShortestPath(context.Background(),
		map[string]interface{}{"name": "a"}, map[string]interface{}{"name": "d"})
	require.NoError(t, err)
	assert.Equal(t, []string{"a", "b", "d"}, pathNames(t, path))

	// Directed: no path backwards
	_, err = cg.ShortestPath(context.Background(),
		map[string]interface{}{"name": "d"}, map[string]interface{}{"name": "a"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no path")

	// Ambiguous endpoints are an error
	person("a")
	_, err = cg.ShortestPath(context.Background(),
		map[string]interface{}{"name": "a"}, map[string]interface{}{"name": "d"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "multiple start nodes")
}

func TestShortestPathNegativeWeight(t *testing.T) {
	db := createTestDB(t)
	defer db.Close()

	gs := NewGraphStore(db)
	graphID := createTestGraph(t, db, "neg-graph")

	person := func(name string) uint32 {
		return createTestNode(t, db, graphID, "Person", map[string]interface{}{"name": name})
	}
	a, b := person("a"), person("b")
	createTestEdge(t, db, graphID, a, b, "knows", map[string]interface{}{"weight": -3.0})

	cg, err := gs.GetGraph("neg-graph")
	require.NoError(t, err)

	path, err := cg.ShortestPath(context.Background(),
		map[string]interface{}{"name": "a"}, map[string]interface{}{"name": "b"})
	require.NoError(t, err)
	assert.Equal(t, []string{"a", "b"}, pathNames(t, path))
}
