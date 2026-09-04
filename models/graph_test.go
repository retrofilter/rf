package models

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMetadataNonStringValues(t *testing.T) {
	meta := json.RawMessage(`{"description": 7, "titleProperty": ["x"]}`)
	g := &Graph{Metadata: &meta}
	assert.Equal(t, "", g.Description())
	assert.Equal(t, "", g.TitleProperty())

	props := json.RawMessage(`{"name": 42}`)
	n := &Node{ID: 9, Properties: &props}
	assert.Equal(t, "9", n.Title("name"))
}

func TestCreateGraph(t *testing.T) {
	db, err := CreateTempDB()
	require.NoError(t, err)
	defer db.Close()

	metadata := jsonPtr(`{"description": "test graph", "tags": ["test"]}`)
	graph := &Graph{
		Name:     "test-graph",
		Metadata: metadata,
	}

	id, err := CreateGraph(db, graph)
	require.NoError(t, err)
	assert.Greater(t, id, uint32(0))

	// Verify the graph was created
	retrieved, err := GetGraph(db, id)
	require.NoError(t, err)
	assert.Equal(t, "test-graph", retrieved.Name)
	assert.Equal(t, metadata, retrieved.Metadata)
}

func TestGetGraphByName(t *testing.T) {
	db, err := CreateTempDB()
	require.NoError(t, err)
	defer db.Close()

	graph := &Graph{Name: "test-graph"}
	id, err := CreateGraph(db, graph)
	require.NoError(t, err)

	retrieved, err := GetGraphByName(db, "test-graph")
	require.NoError(t, err)
	assert.Equal(t, id, retrieved.ID)
	assert.Equal(t, "test-graph", retrieved.Name)
}

func TestGetAllGraphs(t *testing.T) {
	db, err := CreateTempDB()
	require.NoError(t, err)
	defer db.Close()

	// Create multiple graphs
	graph1 := &Graph{Name: "graph-1"}
	graph2 := &Graph{Name: "graph-2"}
	graph3 := &Graph{Name: "graph-3"}

	_, err = CreateGraph(db, graph1)
	require.NoError(t, err)
	_, err = CreateGraph(db, graph2)
	require.NoError(t, err)
	_, err = CreateGraph(db, graph3)
	require.NoError(t, err)

	graphs, err := GetAllGraphs(db, 0, 0)
	require.NoError(t, err)
	assert.Len(t, graphs, 3)

	// Test pagination
	graphs, err = GetAllGraphs(db, 2, 0)
	require.NoError(t, err)
	assert.Len(t, graphs, 2)
}

func TestUpdateGraph(t *testing.T) {
	db, err := CreateTempDB()
	require.NoError(t, err)
	defer db.Close()

	graph := &Graph{Name: "original-name"}
	id, err := CreateGraph(db, graph)
	require.NoError(t, err)

	// Update the graph
	graph.ID = id
	graph.Name = "updated-name"
	metadata := jsonPtr(`{"updated": true}`)
	graph.Metadata = metadata

	err = UpdateGraph(db, graph)
	require.NoError(t, err)

	// Verify the update
	retrieved, err := GetGraph(db, id)
	require.NoError(t, err)
	assert.Equal(t, "updated-name", retrieved.Name)
	assert.Equal(t, metadata, retrieved.Metadata)
}

func TestDeleteGraph(t *testing.T) {
	db, err := CreateTempDB()
	require.NoError(t, err)
	defer db.Close()

	graph := &Graph{Name: "test-graph"}
	id, err := CreateGraph(db, graph)
	require.NoError(t, err)

	// Create a node and edge in the graph
	node := &Node{GraphID: id, Type: stringPtr("test")}
	nodeID, err := CreateNode(db, node)
	require.NoError(t, err)

	edge := &Edge{GraphID: id, Source: nodeID, Target: nodeID}
	_, err = CreateEdge(db, edge)
	require.NoError(t, err)

	// Delete the graph
	err = DeleteGraph(db, id)
	require.NoError(t, err)

	// Verify graph is deleted
	_, err = GetGraph(db, id)
	assert.Error(t, err)

	// Verify cascade deletion
	_, err = GetNode(db, nodeID)
	assert.Error(t, err)
}

func TestCreateNode(t *testing.T) {
	db, err := CreateTempDB()
	require.NoError(t, err)
	defer db.Close()

	graph := &Graph{Name: "test-graph"}
	graphID, err := CreateGraph(db, graph)
	require.NoError(t, err)

	properties := jsonPtr(`{"name": "test-node", "value": 42}`)
	node := &Node{
		GraphID:    graphID,
		Type:       stringPtr("test"),
		Properties: properties,
	}

	id, err := CreateNode(db, node)
	require.NoError(t, err)
	assert.Greater(t, id, uint32(0))

	// Verify the node was created
	retrieved, err := GetNode(db, id)
	require.NoError(t, err)
	assert.Equal(t, graphID, retrieved.GraphID)
	assert.Equal(t, "test", *retrieved.Type)
	assert.Equal(t, properties, retrieved.Properties)
}

func TestCreateNodesBatch(t *testing.T) {
	db, err := CreateTempDB()
	require.NoError(t, err)
	defer db.Close()

	graph := &Graph{Name: "test-graph"}
	graphID, err := CreateGraph(db, graph)
	require.NoError(t, err)

	nodes := []*Node{
		{GraphID: graphID, Type: stringPtr("type1"), Properties: jsonPtr(`{"id": 1}`)},
		{GraphID: graphID, Type: stringPtr("type2"), Properties: jsonPtr(`{"id": 2}`)},
		{GraphID: graphID, Type: stringPtr("type3"), Properties: jsonPtr(`{"id": 3}`)},
	}

	err = CreateNodesBatch(db, nodes)
	require.NoError(t, err)

	// Verify all nodes were created
	createdNodes, err := GetNodesByGraph(db, graphID, 0, 0)
	require.NoError(t, err)
	assert.Len(t, createdNodes, 3)
}

func TestGetNodeWithEdges(t *testing.T) {
	db, err := CreateTempDB()
	require.NoError(t, err)
	defer db.Close()

	graph := &Graph{Name: "test-graph"}
	graphID, err := CreateGraph(db, graph)
	require.NoError(t, err)

	// Create nodes
	node1 := &Node{GraphID: graphID, Type: stringPtr("node")}
	node2 := &Node{GraphID: graphID, Type: stringPtr("node")}
	node1ID, err := CreateNode(db, node1)
	require.NoError(t, err)
	node2ID, err := CreateNode(db, node2)
	require.NoError(t, err)

	// Create edges
	edge1 := &Edge{GraphID: graphID, Source: node1ID, Target: node2ID, Type: stringPtr("connects")}
	edge2 := &Edge{GraphID: graphID, Source: node2ID, Target: node1ID, Type: stringPtr("connects")}
	_, err = CreateEdge(db, edge1)
	require.NoError(t, err)
	_, err = CreateEdge(db, edge2)
	require.NoError(t, err)

	// Get node with edges
	nodeWithEdges, err := GetNodeWithEdges(db, node1ID)
	require.NoError(t, err)
	assert.Len(t, nodeWithEdges.Edges, 2)
}

func TestGetNodesByType(t *testing.T) {
	db, err := CreateTempDB()
	require.NoError(t, err)
	defer db.Close()

	graph := &Graph{Name: "test-graph"}
	graphID, err := CreateGraph(db, graph)
	require.NoError(t, err)

	// Create nodes of different types
	node1 := &Node{GraphID: graphID, Type: stringPtr("person")}
	node2 := &Node{GraphID: graphID, Type: stringPtr("person")}
	node3 := &Node{GraphID: graphID, Type: stringPtr("company")}

	_, err = CreateNode(db, node1)
	require.NoError(t, err)
	_, err = CreateNode(db, node2)
	require.NoError(t, err)
	_, err = CreateNode(db, node3)
	require.NoError(t, err)

	// Get nodes by type
	personNodes, err := GetNodesByType(db, graphID, "person", 0, 0)
	require.NoError(t, err)
	assert.Len(t, personNodes, 2)

	companyNodes, err := GetNodesByType(db, graphID, "company", 0, 0)
	require.NoError(t, err)
	assert.Len(t, companyNodes, 1)
}

func TestCreateEdge(t *testing.T) {
	db, err := CreateTempDB()
	require.NoError(t, err)
	defer db.Close()

	graph := &Graph{Name: "test-graph"}
	graphID, err := CreateGraph(db, graph)
	require.NoError(t, err)

	// Create nodes
	node1 := &Node{GraphID: graphID}
	node2 := &Node{GraphID: graphID}
	node1ID, err := CreateNode(db, node1)
	require.NoError(t, err)
	node2ID, err := CreateNode(db, node2)
	require.NoError(t, err)

	properties := jsonPtr(`{"weight": 1.5, "directed": true}`)
	edge := &Edge{
		GraphID:    graphID,
		Source:     node1ID,
		Target:     node2ID,
		Type:       stringPtr("connects"),
		Properties: properties,
	}

	id, err := CreateEdge(db, edge)
	require.NoError(t, err)
	assert.Greater(t, id, uint32(0))

	// Verify the edge was created
	retrieved, err := GetEdge(db, id)
	require.NoError(t, err)
	assert.Equal(t, graphID, retrieved.GraphID)
	assert.Equal(t, node1ID, retrieved.Source)
	assert.Equal(t, node2ID, retrieved.Target)
	assert.Equal(t, "connects", *retrieved.Type)
	assert.Equal(t, properties, retrieved.Properties)
}

func TestCreateEdgesBatch(t *testing.T) {
	db, err := CreateTempDB()
	require.NoError(t, err)
	defer db.Close()

	graph := &Graph{Name: "test-graph"}
	graphID, err := CreateGraph(db, graph)
	require.NoError(t, err)

	// Create nodes
	node1 := &Node{GraphID: graphID}
	node2 := &Node{GraphID: graphID}
	node3 := &Node{GraphID: graphID}
	node1ID, err := CreateNode(db, node1)
	require.NoError(t, err)
	node2ID, err := CreateNode(db, node2)
	require.NoError(t, err)
	node3ID, err := CreateNode(db, node3)
	require.NoError(t, err)

	edges := []*Edge{
		{GraphID: graphID, Source: node1ID, Target: node2ID, Type: stringPtr("connects")},
		{GraphID: graphID, Source: node2ID, Target: node3ID, Type: stringPtr("connects")},
		{GraphID: graphID, Source: node3ID, Target: node1ID, Type: stringPtr("connects")},
	}

	err = CreateEdgesBatch(db, edges)
	require.NoError(t, err)

	// Verify all edges were created
	createdEdges, err := GetEdgesByGraph(db, graphID, 0, 0)
	require.NoError(t, err)
	assert.Len(t, createdEdges, 3)
}

func TestGetEdgesByNode(t *testing.T) {
	db, err := CreateTempDB()
	require.NoError(t, err)
	defer db.Close()

	graph := &Graph{Name: "test-graph"}
	graphID, err := CreateGraph(db, graph)
	require.NoError(t, err)

	// Create nodes
	node1 := &Node{GraphID: graphID}
	node2 := &Node{GraphID: graphID}
	node3 := &Node{GraphID: graphID}
	node1ID, err := CreateNode(db, node1)
	require.NoError(t, err)
	node2ID, err := CreateNode(db, node2)
	require.NoError(t, err)
	node3ID, err := CreateNode(db, node3)
	require.NoError(t, err)

	// Create edges
	edge1 := &Edge{GraphID: graphID, Source: node1ID, Target: node2ID}
	edge2 := &Edge{GraphID: graphID, Source: node2ID, Target: node1ID}
	edge3 := &Edge{GraphID: graphID, Source: node1ID, Target: node3ID}
	_, err = CreateEdge(db, edge1)
	require.NoError(t, err)
	_, err = CreateEdge(db, edge2)
	require.NoError(t, err)
	_, err = CreateEdge(db, edge3)
	require.NoError(t, err)

	// Get edges for node1
	edges, err := GetEdgesByNode(db, node1ID)
	require.NoError(t, err)
	assert.Len(t, edges, 3) // 2 outgoing + 1 incoming
}

func TestGetEdgesBetween(t *testing.T) {
	db, err := CreateTempDB()
	require.NoError(t, err)
	defer db.Close()

	graph := &Graph{Name: "test-graph"}
	graphID, err := CreateGraph(db, graph)
	require.NoError(t, err)

	// Create nodes
	node1 := &Node{GraphID: graphID}
	node2 := &Node{GraphID: graphID}
	node1ID, err := CreateNode(db, node1)
	require.NoError(t, err)
	node2ID, err := CreateNode(db, node2)
	require.NoError(t, err)

	// Create edges between nodes
	edge1 := &Edge{GraphID: graphID, Source: node1ID, Target: node2ID, Type: stringPtr("type1")}
	edge2 := &Edge{GraphID: graphID, Source: node1ID, Target: node2ID, Type: stringPtr("type2")}
	_, err = CreateEdge(db, edge1)
	require.NoError(t, err)
	_, err = CreateEdge(db, edge2)
	require.NoError(t, err)

	// Get edges between node1 and node2
	edges, err := GetEdgesBetween(db, node1ID, node2ID)
	require.NoError(t, err)
	assert.Len(t, edges, 2)
}

func TestCountFunctions(t *testing.T) {
	db, err := CreateTempDB()
	require.NoError(t, err)
	defer db.Close()

	// Create graph
	graph := &Graph{Name: "test-graph"}
	graphID, err := CreateGraph(db, graph)
	require.NoError(t, err)

	// Create nodes
	node1 := &Node{GraphID: graphID}
	node2 := &Node{GraphID: graphID}
	node3 := &Node{GraphID: graphID}
	node1ID, err := CreateNode(db, node1)
	require.NoError(t, err)
	_, err = CreateNode(db, node2)
	require.NoError(t, err)
	_, err = CreateNode(db, node3)
	require.NoError(t, err)

	// Create edges
	edge1 := &Edge{GraphID: graphID, Source: node1ID, Target: node1ID}
	edge2 := &Edge{GraphID: graphID, Source: node1ID, Target: node1ID}
	_, err = CreateEdge(db, edge1)
	require.NoError(t, err)
	_, err = CreateEdge(db, edge2)
	require.NoError(t, err)

	// Test count functions
	nodeCount, err := CountNodes(db)
	require.NoError(t, err)
	assert.Equal(t, 3, nodeCount)

	edgeCount, err := CountEdges(db)
	require.NoError(t, err)
	assert.Equal(t, 2, edgeCount)

	graphNodeCount, err := CountNodesInGraph(db, graphID)
	require.NoError(t, err)
	assert.Equal(t, 3, graphNodeCount)

	graphEdgeCount, err := CountEdgesInGraph(db, graphID)
	require.NoError(t, err)
	assert.Equal(t, 2, graphEdgeCount)

	nodeEdgeCount, err := CountEdgesByNode(db, node1ID)
	require.NoError(t, err)
	assert.Equal(t, 2, nodeEdgeCount)
}

func TestUpdateOperations(t *testing.T) {
	db, err := CreateTempDB()
	require.NoError(t, err)
	defer db.Close()

	// Create graph and node
	graph := &Graph{Name: "test-graph"}
	graphID, err := CreateGraph(db, graph)
	require.NoError(t, err)

	node := &Node{GraphID: graphID, Type: stringPtr("original")}
	nodeID, err := CreateNode(db, node)
	require.NoError(t, err)

	// Update node
	node.ID = nodeID
	node.Type = stringPtr("updated")
	node.Properties = jsonPtr(`{"updated": true}`)
	err = UpdateNode(db, node)
	require.NoError(t, err)

	// Verify update
	retrieved, err := GetNode(db, nodeID)
	require.NoError(t, err)
	assert.Equal(t, "updated", *retrieved.Type)

	// Create and update edge
	edge := &Edge{GraphID: graphID, Source: nodeID, Target: nodeID}
	edgeID, err := CreateEdge(db, edge)
	require.NoError(t, err)

	edge.ID = edgeID
	edge.Type = stringPtr("updated-edge")
	edge.Properties = jsonPtr(`{"updated": true}`)
	err = UpdateEdge(db, edge)
	require.NoError(t, err)

	// Verify edge update
	retrievedEdge, err := GetEdge(db, edgeID)
	require.NoError(t, err)
	assert.Equal(t, "updated-edge", *retrievedEdge.Type)
}

func TestDeleteOperations(t *testing.T) {
	db, err := CreateTempDB()
	require.NoError(t, err)
	defer db.Close()

	// Create graph, node, and edge
	graph := &Graph{Name: "test-graph"}
	graphID, err := CreateGraph(db, graph)
	require.NoError(t, err)

	node := &Node{GraphID: graphID}
	nodeID, err := CreateNode(db, node)
	require.NoError(t, err)

	edge := &Edge{GraphID: graphID, Source: nodeID, Target: nodeID}
	edgeID, err := CreateEdge(db, edge)
	require.NoError(t, err)

	// Delete edge
	err = DeleteEdge(db, edgeID)
	require.NoError(t, err)

	// Verify edge is deleted
	_, err = GetEdge(db, edgeID)
	assert.Error(t, err)

	// Delete node
	err = DeleteNode(db, nodeID)
	require.NoError(t, err)

	// Verify node is deleted
	_, err = GetNode(db, nodeID)
	assert.Error(t, err)
}

func stringPtr(s string) *string {
	return &s
}

func jsonPtr(s string) *json.RawMessage {
	raw := json.RawMessage(s)
	return &raw
}

func TestLoadGEXF(t *testing.T) {
	db, err := CreateTempDB()
	require.NoError(t, err)
	defer db.Close()

	// Create a test graph
	graph := &Graph{Name: "test-gexf-graph"}
	graphID, err := CreateGraph(db, graph)
	require.NoError(t, err)

	// Create a simple GEXF content for testing
	gexfContent := `<?xml version="1.0" encoding="UTF-8"?>
<gexf xmlns="http://www.gexf.net/1.2draft" version="1.2">
  <graph mode="static" defaultedgetype="directed">
    <nodes>
      <node id="1" label="Node 1">
        <attvalues>
          <attvalue for="type" value="person"/>
        </attvalues>
      </node>
      <node id="2" label="Node 2">
        <attvalues>
          <attvalue for="type" value="company"/>
        </attvalues>
      </node>
    </nodes>
    <edges>
      <edge id="1" source="1" target="2">
        <attvalues>
          <attvalue for="weight" value="5"/>
        </attvalues>
      </edge>
    </edges>
  </graph>
</gexf>`

	// Load the GEXF data
	err = LoadGEXFFromReader(db, graphID, strings.NewReader(gexfContent))
	require.NoError(t, err)

	// Verify nodes were created
	nodes, err := GetNodesByGraph(db, graphID, 0, 0)
	require.NoError(t, err)
	assert.Equal(t, 2, len(nodes))

	// Verify edges were created
	edges, err := GetEdgesByGraph(db, graphID, 0, 0)
	require.NoError(t, err)
	assert.Equal(t, 1, len(edges))

	// Verify the edge connects the right nodes
	assert.Equal(t, nodes[0].ID, edges[0].Source)
	assert.Equal(t, nodes[1].ID, edges[0].Target)
}

func TestSearchNodes(t *testing.T) {
	db, err := CreateTempDB()
	require.NoError(t, err)
	defer db.Close()

	graphID, err := CreateGraph(db, &Graph{Name: "kb"})
	require.NoError(t, err)

	noteType := "note"
	mk := func(props string) uint32 {
		node := &Node{GraphID: graphID, Type: &noteType, Properties: jsonPtr(props)}
		id, err := CreateNode(db, node)
		require.NoError(t, err)
		return id
	}
	alice := mk(`{"title": "meeting with alice", "body": {"detail": "quarterly planning"}}`)
	bob := mk(`{"title": "lunch with bob"}`)
	mk(`{"count": 42}`) // no string properties — indexed but empty

	// Match on a top-level string value
	nodes, ranks, err := SearchNodes(db, graphID, "alice", "", 0)
	require.NoError(t, err)
	require.Len(t, nodes, 1)
	assert.Equal(t, alice, nodes[0].ID)
	assert.Len(t, ranks, 1)

	// Nested string values are indexed too
	nodes, _, err = SearchNodes(db, graphID, "quarterly", "", 0)
	require.NoError(t, err)
	require.Len(t, nodes, 1)
	assert.Equal(t, alice, nodes[0].ID)

	// Node type is searchable
	nodes, _, err = SearchNodes(db, graphID, "note", "", 0)
	require.NoError(t, err)
	assert.Len(t, nodes, 3)

	// The nodeType filter restricts results
	nodes, _, err = SearchNodes(db, graphID, "alice", "note", 0)
	require.NoError(t, err)
	assert.Len(t, nodes, 1)
	nodes, _, err = SearchNodes(db, graphID, "alice", "memory", 0)
	require.NoError(t, err)
	assert.Empty(t, nodes)

	// Bare terms get a prefix wildcard
	nodes, _, err = SearchNodes(db, graphID, "lun", "", 0)
	require.NoError(t, err)
	require.Len(t, nodes, 1)
	assert.Equal(t, bob, nodes[0].ID)

	// Limit applies
	nodes, _, err = SearchNodes(db, graphID, "note", "", 2)
	require.NoError(t, err)
	assert.Len(t, nodes, 2)

	// Updating a node reindexes it
	require.NoError(t, UpdateNode(db, &Node{ID: bob, Type: &noteType, Properties: jsonPtr(`{"title": "dinner with carol"}`)}))
	nodes, _, err = SearchNodes(db, graphID, "lunch", "", 0)
	require.NoError(t, err)
	assert.Empty(t, nodes)
	nodes, _, err = SearchNodes(db, graphID, "carol", "", 0)
	require.NoError(t, err)
	require.Len(t, nodes, 1)
	assert.Equal(t, bob, nodes[0].ID)

	// Deleting a node removes it from the index
	require.NoError(t, DeleteNode(db, alice))
	nodes, _, err = SearchNodes(db, graphID, "alice", "", 0)
	require.NoError(t, err)
	assert.Empty(t, nodes)

	// Search is scoped to the graph
	otherID, err := CreateGraph(db, &Graph{Name: "other"})
	require.NoError(t, err)
	nodes, _, err = SearchNodes(db, otherID, "carol", "", 0)
	require.NoError(t, err)
	assert.Empty(t, nodes)
}

func TestGetNodesByAttributesHostileKey(t *testing.T) {
	db, err := CreateTempDB()
	require.NoError(t, err)
	defer db.Close()

	id, err := CreateGraph(db, &Graph{Name: "inj"})
	require.NoError(t, err)
	_, err = CreateNode(db, &Node{GraphID: id, Type: stringPtr("note"), Properties: jsonPtr(`{"name":"x"}`)})
	require.NoError(t, err)

	hostile := `name') IS NOT NULL OR ('1'='1`
	nodes, err := GetNodesByAttributes(db, id, map[string]interface{}{hostile: "y"}, 0, 0)
	if err == nil {
		assert.Empty(t, nodes)
	}

	// Sane keys still match.
	nodes, err = GetNodesByAttributes(db, id, map[string]interface{}{"name": "x"}, 0, 0)
	require.NoError(t, err)
	assert.Len(t, nodes, 1)
}
