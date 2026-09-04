package models

import (
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"os"
	"strings"
	"time"
	"unicode"

	"github.com/jmoiron/sqlx"
	"github.com/retrofilter/rf/logger"
	"gonum.org/v1/gonum/graph/formats/gexf12"
)

// GraphStats represents statistics for a graph
// These are optional fields that can be populated when needed
type GraphStats struct {
	NodeCount int `json:"node_count" db:"node_count"`
	EdgeCount int `json:"edge_count" db:"edge_count"`
}

// Graph is a graph row: a numeric ID, a user-provided Name, arbitrary JSON
// Metadata, timestamps, and optional Stats.
type Graph struct {
	ID        uint32           `json:"id" db:"id"`
	Name      string           `json:"name" db:"name"`
	Metadata  *json.RawMessage `json:"metadata" db:"metadata"`
	CreatedAt time.Time        `json:"created_at" db:"created_at"`
	UpdatedAt time.Time        `json:"updated_at" db:"updated_at"`
	Stats     *GraphStats      `json:"stats,omitempty" db:"-"`
}

// Node represents a node in a graph ID and GraphID are uint32 Edges can be
// optionally loaded for in-memory use Properties are stored as JSON for
// flexibility
type Node struct {
	ID         uint32           `json:"id" db:"id"`
	GraphID    uint32           `json:"graph_id" db:"graph_id"`
	Type       *string          `json:"type,omitempty" db:"type"`
	Properties *json.RawMessage `json:"properties" db:"properties"`
	CreatedAt  time.Time        `json:"created_at" db:"created_at"`
	UpdatedAt  time.Time        `json:"updated_at" db:"updated_at"`
	// Optional: Eager loaded edges (not stored in DB)
	Edges []*Edge `json:"edges,omitempty" db:"-"`
}

// FormattedProperties returns a map of parsed properties for display
func (n *Node) FormattedProperties() map[string]interface{} {
	if n.Properties == nil {
		return make(map[string]interface{})
	}

	var props map[string]interface{}
	if err := json.Unmarshal(*n.Properties, &props); err != nil {
		return map[string]interface{}{
			"error": "Failed to parse properties",
		}
	}

	return props
}

// Edge represents a directed edge between two nodes in a graph
// ID, GraphID, Source, and Target are uint32
// Properties are stored as JSON for flexibility
type Edge struct {
	ID         uint32           `json:"id" db:"id"`
	GraphID    uint32           `json:"graph_id" db:"graph_id"`
	Source     uint32           `json:"source" db:"source"`
	Target     uint32           `json:"target" db:"target"`
	Type       *string          `json:"type,omitempty" db:"type"`
	Properties *json.RawMessage `json:"properties" db:"properties"`
	CreatedAt  time.Time        `json:"created_at" db:"created_at"`
	UpdatedAt  time.Time        `json:"updated_at" db:"updated_at"`
}

func (g *Graph) Description() string {
	if g.Metadata == nil {
		return ""
	}

	var metadata map[string]interface{}
	err := json.Unmarshal(*g.Metadata, &metadata)
	if err != nil {
		return ""
	}
	if description, ok := metadata["description"].(string); ok {
		return description
	}
	return ""
}

func (g *Graph) TitleProperty() string {
	if g.Metadata == nil {
		return ""
	}
	var metadata map[string]interface{}
	err := json.Unmarshal(*g.Metadata, &metadata)
	if err != nil {
		return ""
	}
	if titleFormat, ok := metadata["titleProperty"].(string); ok {
		return titleFormat
	}
	return ""
}

func (n *Node) Title(property string) string {
	if n.Properties == nil {
		return ""
	}
	var props map[string]interface{}
	if err := json.Unmarshal(*n.Properties, &props); err != nil {
		return ""
	}
	if title, ok := props[property].(string); ok {
		return title
	}
	return fmt.Sprintf("%d", n.ID)
}

// CreateGraph creates a new graph in the database
func CreateGraph(db *sqlx.DB, graph *Graph) (uint32, error) {
	query := `
		INSERT INTO graph (name, metadata, created_at, updated_at)
		VALUES (?, ?, ?, ?)
	`

	logger.Debug().Str("name", graph.Name).Msg("Creating graph")

	at := time.Now().UTC()
	result, err := db.Exec(query, graph.Name, graph.Metadata, at, at)
	if err != nil {
		return 0, err
	}

	id, err := result.LastInsertId()
	if err != nil {
		return 0, err
	}

	return uint32(id), nil
}

// GetGraph retrieves a graph by ID
func GetGraph(db *sqlx.DB, id uint32) (*Graph, error) {
	query := `
		SELECT id, name, metadata, created_at, updated_at
		FROM graph WHERE id = ?
	`

	var graph Graph
	err := db.Get(&graph, query, id)
	if err != nil {
		return nil, err
	}

	return &graph, nil
}

// GetGraphByName retrieves a graph by name
func GetGraphByName(db *sqlx.DB, name string) (*Graph, error) {
	query := `
		SELECT id, name, metadata, created_at, updated_at
		FROM graph WHERE name = ?
	`

	var graph Graph
	err := db.Get(&graph, query, name)
	if err != nil {
		return nil, err
	}

	return &graph, nil
}

// GetAllGraphs retrieves all graphs with optional pagination
func GetAllGraphs(db *sqlx.DB, limit, offset int) ([]*Graph, error) {
	query := `
		SELECT id, name, metadata, created_at, updated_at
		FROM graph
		ORDER BY created_at DESC
	`

	if limit > 0 {
		query += " LIMIT ? OFFSET ?"
	}

	var graphs []*Graph
	var err error

	if limit > 0 {
		err = db.Select(&graphs, query, limit, offset)
	} else {
		err = db.Select(&graphs, query)
	}

	if err != nil {
		return nil, err
	}

	return graphs, nil
}

// GetAllGraphsWithStats retrieves all graphs with their node and edge counts
// Uses LEFT JOINs to get stats in a single query, avoiding N+1 queries
func GetAllGraphsWithStats(db *sqlx.DB, limit, offset int) ([]*Graph, error) {
	query := `
		SELECT 
			g.id, g.name, g.metadata, g.created_at, g.updated_at,
			COALESCE(node_counts.count, 0) as node_count,
			COALESCE(edge_counts.count, 0) as edge_count
		FROM graph g
		LEFT JOIN (
			SELECT graph_id, COUNT(*) as count
			FROM node
			GROUP BY graph_id
		) node_counts ON g.id = node_counts.graph_id
		LEFT JOIN (
			SELECT graph_id, COUNT(*) as count
			FROM edge
			GROUP BY graph_id
		) edge_counts ON g.id = edge_counts.graph_id
		ORDER BY g.created_at DESC
	`

	if limit > 0 {
		query += " LIMIT ? OFFSET ?"
	}

	// We need to use a custom struct to capture the raw data
	type graphWithStats struct {
		ID        uint32           `db:"id"`
		Name      string           `db:"name"`
		Metadata  *json.RawMessage `db:"metadata"`
		CreatedAt time.Time        `db:"created_at"`
		UpdatedAt time.Time        `db:"updated_at"`
		NodeCount int              `db:"node_count"`
		EdgeCount int              `db:"edge_count"`
	}

	var rawGraphs []graphWithStats
	var err error

	if limit > 0 {
		err = db.Select(&rawGraphs, query, limit, offset)
	} else {
		err = db.Select(&rawGraphs, query)
	}

	if err != nil {
		return nil, err
	}

	// Convert to Graph structs with stats
	graphs := make([]*Graph, len(rawGraphs))
	for i, raw := range rawGraphs {
		graphs[i] = &Graph{
			ID:        raw.ID,
			Name:      raw.Name,
			Metadata:  raw.Metadata,
			CreatedAt: raw.CreatedAt,
			UpdatedAt: raw.UpdatedAt,
			Stats: &GraphStats{
				NodeCount: raw.NodeCount,
				EdgeCount: raw.EdgeCount,
			},
		}
	}

	return graphs, nil
}

// UpdateGraph updates an existing graph
func UpdateGraph(db *sqlx.DB, graph *Graph) error {
	query := `
		UPDATE graph
		SET name = ?, metadata = ?, updated_at = ?
		WHERE id = ?
	`

	_, err := db.Exec(query, graph.Name, graph.Metadata, time.Now().UTC(), graph.ID)
	return err
}

// DeleteGraph deletes a graph and all its nodes and edges
func DeleteGraph(db *sqlx.DB, id uint32) error {
	query := `DELETE FROM graph WHERE id = ?`
	_, err := db.Exec(query, id)
	return err
}

// CreateNode creates a new node in the database
func CreateNode(db *sqlx.DB, node *Node) (uint32, error) {
	query := `
		INSERT INTO node (graph_id, type, properties, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?)
	`

	at := time.Now().UTC()
	result, err := db.Exec(query, node.GraphID, node.Type, node.Properties, at, at)
	if err != nil {
		return 0, err
	}

	id, err := result.LastInsertId()
	if err != nil {
		return 0, err
	}

	return uint32(id), nil
}

// CreateNodesBatch creates multiple nodes in a single transaction
func CreateNodesBatch(db *sqlx.DB, nodes []*Node) error {
	tx, err := db.Beginx()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	query := `
		INSERT INTO node (graph_id, type, properties, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?)
	`

	at := time.Now().UTC()
	for _, node := range nodes {
		_, err := tx.Exec(query, node.GraphID, node.Type, node.Properties, at, at)
		if err != nil {
			return err
		}
	}

	return tx.Commit()
}

// GetNode retrieves a node by ID
func GetNode(db *sqlx.DB, id uint32) (*Node, error) {
	query := `
		SELECT id, graph_id, type, properties, created_at, updated_at
		FROM node WHERE id = ?
	`

	var node Node
	err := db.Get(&node, query, id)
	if err != nil {
		return nil, err
	}

	return &node, nil
}

// GetNodeWithEdges retrieves a node with its connected edges
func GetNodeWithEdges(db *sqlx.DB, id uint32) (*Node, error) {
	node, err := GetNode(db, id)
	if err != nil {
		return nil, err
	}

	edges, err := GetEdgesByNode(db, id)
	if err != nil {
		return nil, err
	}

	node.Edges = edges
	return node, nil
}

// GetNodesByGraph retrieves all nodes for a graph with optional pagination
func GetNodesByGraph(db *sqlx.DB, graphID uint32, limit, offset int) ([]*Node, error) {
	query := `
		SELECT id, graph_id, type, properties, created_at, updated_at
		FROM node WHERE graph_id = ?
		ORDER BY id
	`

	if limit > 0 {
		query += " LIMIT ? OFFSET ?"
	}

	var nodes []*Node
	var err error

	if limit > 0 {
		err = db.Select(&nodes, query, graphID, limit, offset)
	} else {
		err = db.Select(&nodes, query, graphID)
	}

	if err != nil {
		return nil, err
	}

	return nodes, nil
}

// GetNodesByType retrieves nodes by type within a graph
func GetNodesByType(db *sqlx.DB, graphID uint32, nodeType string, limit, offset int) ([]*Node, error) {
	query := `
		SELECT id, graph_id, type, properties, created_at, updated_at
		FROM node WHERE graph_id = ? AND type = ?
		ORDER BY id
	`

	if limit > 0 {
		query += " LIMIT ? OFFSET ?"
	}

	var nodes []*Node
	var err error

	if limit > 0 {
		err = db.Select(&nodes, query, graphID, nodeType, limit, offset)
	} else {
		err = db.Select(&nodes, query, graphID, nodeType)
	}

	if err != nil {
		return nil, err
	}

	return nodes, nil
}

// GetNodesByAttributes retrieves nodes by matching JSON properties
// attributes is a map of property names to expected values
func GetNodesByAttributes(db *sqlx.DB, graphID uint32, attributes map[string]interface{}, limit, offset int) ([]*Node, error) {
	if len(attributes) == 0 {
		return GetNodesByGraph(db, graphID, limit, offset)
	}

	// Build the WHERE clause for JSON property matching
	var conditions []string
	var args []interface{}

	args = append(args, graphID)
	conditions = append(conditions, "graph_id = ?")

	for key, value := range attributes {
		conditions = append(conditions, "JSON_EXTRACT(properties, ?) = ?")
		args = append(args, "$."+key, value)
	}

	query := fmt.Sprintf(`
		SELECT id, graph_id, type, properties, created_at, updated_at
		FROM node WHERE %s
		ORDER BY id
	`, strings.Join(conditions, " AND "))

	if limit > 0 {
		query += " LIMIT ? OFFSET ?"
		args = append(args, limit, offset)
	}

	var nodes []*Node
	err := db.Select(&nodes, query, args...)
	if err != nil {
		return nil, err
	}

	return nodes, nil
}

// SearchNodes full-text searches a graph's nodes via node_fts (every string
// value in the properties JSON plus the node type, maintained by triggers).
func SearchNodes(db *sqlx.DB, graphID uint32, query string, nodeType string, limit int) ([]*Node, []float64, error) {
	if len(query) > 2 && !strings.ContainsAny(query, ` *"()`) {
		query += "*"
	}

	sqlQuery := `
		SELECT node.id, node.graph_id, node.type, node.properties, node.created_at, node.updated_at,
		       node_fts.rank AS rank
		FROM node_fts
		JOIN node ON node.id = node_fts.rowid
		WHERE node_fts MATCH ? AND node.graph_id = ?
	`
	args := []interface{}{query, graphID}
	if nodeType != "" {
		sqlQuery += " AND node.type = ?"
		args = append(args, nodeType)
	}
	sqlQuery += " ORDER BY node_fts.rank"
	if limit > 0 {
		sqlQuery += " LIMIT ?"
		args = append(args, limit)
	}

	type nodeWithRank struct {
		Node
		Rank float64 `db:"rank"`
	}
	var rows []nodeWithRank
	if err := db.Select(&rows, sqlQuery, args...); err != nil {
		return nil, nil, err
	}

	nodes := make([]*Node, len(rows))
	ranks := make([]float64, len(rows))
	for i := range rows {
		node := rows[i].Node
		nodes[i] = &node
		ranks[i] = rows[i].Rank
	}
	return nodes, ranks, nil
}

// SearchNodesLexical is SearchNodes for natural-language queries: the raw
// FTS5 query is tried first, and a MATCH syntax error falls back to an OR of
// the query's quoted word tokens.
func SearchNodesLexical(db *sqlx.DB, graphID uint32, query string, nodeType string, limit int) ([]*Node, []float64, error) {
	nodes, ranks, err := SearchNodes(db, graphID, query, nodeType, limit)
	if err == nil {
		return nodes, ranks, nil
	}
	tokens := strings.FieldsFunc(query, func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	})
	collect := func(dropStop bool) (terms []string) {
		for _, f := range tokens {
			if dropStop && Stopwords[strings.ToLower(f)] {
				continue
			}
			terms = append(terms, `"`+f+`"`)
		}
		return terms
	}
	terms := collect(true)
	if len(terms) == 0 {
		terms = collect(false)
	}
	if len(terms) == 0 {
		return nil, nil, nil
	}
	return SearchNodes(db, graphID, strings.Join(terms, " OR "), nodeType, limit)
}

// UpdateNode updates an existing node
func UpdateNode(db *sqlx.DB, node *Node) error {
	query := `
		UPDATE node
		SET type = ?, properties = ?, updated_at = ?
		WHERE id = ?
	`

	_, err := db.Exec(query, node.Type, node.Properties, time.Now().UTC(), node.ID)
	return err
}

// DeleteNode deletes a node and all its connected edges
func DeleteNode(db *sqlx.DB, id uint32) error {
	query := `DELETE FROM node WHERE id = ?`
	_, err := db.Exec(query, id)
	return err
}

// CountNodesInGraph counts the total number of nodes in a graph
func CountNodesInGraph(db *sqlx.DB, graphID uint32) (int, error) {
	query := `SELECT COUNT(*) FROM node WHERE graph_id = ?`
	var count int
	err := db.Get(&count, query, graphID)
	return count, err
}

// CreateEdge creates a new edge in the database
func CreateEdge(db *sqlx.DB, edge *Edge) (uint32, error) {
	query := `
		INSERT INTO edge (graph_id, source, target, type, properties, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)
	`

	at := time.Now().UTC()
	result, err := db.Exec(query, edge.GraphID, edge.Source, edge.Target, edge.Type, edge.Properties, at, at)
	if err != nil {
		return 0, err
	}

	id, err := result.LastInsertId()
	if err != nil {
		return 0, err
	}

	return uint32(id), nil
}

// CreateEdgesBatch creates multiple edges in a single transaction
func CreateEdgesBatch(db *sqlx.DB, edges []*Edge) error {
	tx, err := db.Beginx()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	query := `
		INSERT INTO edge (graph_id, source, target, type, properties, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)
	`

	at := time.Now().UTC()
	for _, edge := range edges {
		_, err := tx.Exec(query, edge.GraphID, edge.Source, edge.Target, edge.Type, edge.Properties, at, at)
		if err != nil {
			return err
		}
	}

	return tx.Commit()
}

// GetEdge retrieves an edge by ID
func GetEdge(db *sqlx.DB, id uint32) (*Edge, error) {
	query := `
		SELECT id, graph_id, source, target, type, properties, created_at, updated_at
		FROM edge WHERE id = ?
	`

	var edge Edge
	err := db.Get(&edge, query, id)
	if err != nil {
		return nil, err
	}

	return &edge, nil
}

// GetEdgesByGraph retrieves all edges for a graph with optional pagination
func GetEdgesByGraph(db *sqlx.DB, graphID uint32, limit, offset int) ([]*Edge, error) {
	query := `
		SELECT id, graph_id, source, target, type, properties, created_at, updated_at
		FROM edge WHERE graph_id = ?
		ORDER BY id
	`

	if limit > 0 {
		query += " LIMIT ? OFFSET ?"
	}

	var edges []*Edge
	var err error

	if limit > 0 {
		err = db.Select(&edges, query, graphID, limit, offset)
	} else {
		err = db.Select(&edges, query, graphID)
	}

	if err != nil {
		return nil, err
	}

	return edges, nil
}

// GetEdgesByNode retrieves all edges connected to a specific node
func GetEdgesByNode(db *sqlx.DB, nodeID uint32) ([]*Edge, error) {
	query := `
		SELECT id, graph_id, source, target, type, properties, created_at, updated_at
		FROM edge WHERE source = ? OR target = ?
		ORDER BY id
	`

	var edges []*Edge
	err := db.Select(&edges, query, nodeID, nodeID)
	if err != nil {
		return nil, err
	}

	return edges, nil
}

// GetEdgesByType retrieves edges by type within a graph
func GetEdgesByType(db *sqlx.DB, graphID uint32, edgeType string, limit, offset int) ([]*Edge, error) {
	query := `
		SELECT id, graph_id, source, target, type, properties, created_at, updated_at
		FROM edge WHERE graph_id = ? AND type = ?
		ORDER BY id
	`

	if limit > 0 {
		query += " LIMIT ? OFFSET ?"
	}

	var edges []*Edge
	var err error

	if limit > 0 {
		err = db.Select(&edges, query, graphID, edgeType, limit, offset)
	} else {
		err = db.Select(&edges, query, graphID, edgeType)
	}

	if err != nil {
		return nil, err
	}

	return edges, nil
}

// GetEdgesBetween retrieves edges between two specific nodes
func GetEdgesBetween(db *sqlx.DB, source, target uint32) ([]*Edge, error) {
	query := `
		SELECT id, graph_id, source, target, type, properties, created_at, updated_at
		FROM edge WHERE source = ? AND target = ?
		ORDER BY id
	`

	var edges []*Edge
	err := db.Select(&edges, query, source, target)
	if err != nil {
		return nil, err
	}

	return edges, nil
}

// UpdateEdge updates an existing edge
func UpdateEdge(db *sqlx.DB, edge *Edge) error {
	query := `
		UPDATE edge
		SET type = ?, properties = ?, updated_at = ?
		WHERE id = ?
	`

	_, err := db.Exec(query, edge.Type, edge.Properties, time.Now().UTC(), edge.ID)
	return err
}

// DeleteEdge deletes an edge
func DeleteEdge(db *sqlx.DB, id uint32) error {
	query := `DELETE FROM edge WHERE id = ?`
	_, err := db.Exec(query, id)
	return err
}

// CountEdgesInGraph counts the total number of edges in a graph
func CountEdgesInGraph(db *sqlx.DB, graphID uint32) (int, error) {
	query := `SELECT COUNT(*) FROM edge WHERE graph_id = ?`
	var count int
	err := db.Get(&count, query, graphID)
	return count, err
}

// CountEdgesByNode counts the number of edges connected to a node
func CountEdgesByNode(db *sqlx.DB, nodeID uint32) (int, error) {
	query := `SELECT COUNT(*) FROM edge WHERE source = ? OR target = ?`
	var count int
	err := db.Get(&count, query, nodeID, nodeID)
	return count, err
}

// CountEdges counts the total number of edges across all graphs
func CountEdges(db *sqlx.DB) (int, error) {
	query := `SELECT COUNT(*) FROM edge`
	var count int
	err := db.Get(&count, query)
	return count, err
}

// CountNodes counts the total number of nodes across all graphs
func CountNodes(db *sqlx.DB) (int, error) {
	query := `SELECT COUNT(*) FROM node`
	var count int
	err := db.Get(&count, query)
	return count, err
}

// GetTopNodesByIncomingEdges retrieves the top nodes by incoming edge count in descending order
// Returns two arrays: nodes and their corresponding edge counts
// limit specifies the maximum number of nodes to return (0 for no limit)
func GetTopNodesByIncomingEdges(db *sqlx.DB, graphID uint32, limit int) ([]*Node, []int, error) {
	query := `
		SELECT 
			n.id, n.graph_id, n.type, n.properties, n.created_at, n.updated_at,
			COALESCE(edge_counts.count, 0) as edge_count
		FROM node n
		LEFT JOIN (
			SELECT target as node_id, COUNT(*) as count
			FROM edge 
			WHERE graph_id = ?
			GROUP BY target
		) edge_counts ON n.id = edge_counts.node_id
		WHERE n.graph_id = ?
		ORDER BY edge_count DESC, n.id ASC
	`

	if limit > 0 {
		query += " LIMIT ?"
	}

	var args []interface{}
	args = append(args, graphID, graphID)
	if limit > 0 {
		args = append(args, limit)
	}

	// Use a temporary struct to capture both node data and edge count
	type nodeWithCount struct {
		ID         uint32           `db:"id"`
		GraphID    uint32           `db:"graph_id"`
		Type       *string          `db:"type"`
		Properties *json.RawMessage `db:"properties"`
		CreatedAt  time.Time        `db:"created_at"`
		UpdatedAt  time.Time        `db:"updated_at"`
		EdgeCount  int              `db:"edge_count"`
	}

	var results []nodeWithCount
	err := db.Select(&results, query, args...)
	if err != nil {
		return nil, nil, err
	}

	// Separate into two arrays
	nodes := make([]*Node, len(results))
	edgeCounts := make([]int, len(results))

	for i, result := range results {
		nodes[i] = &Node{
			ID:         result.ID,
			GraphID:    result.GraphID,
			Type:       result.Type,
			Properties: result.Properties,
			CreatedAt:  result.CreatedAt,
			UpdatedAt:  result.UpdatedAt,
		}
		edgeCounts[i] = result.EdgeCount
	}

	return nodes, edgeCounts, nil
}

// GetTopNodesByOutgoingEdges retrieves the top nodes by outgoing edge count in descending order
// Returns two arrays: nodes and their corresponding edge counts
// limit specifies the maximum number of nodes to return (0 for no limit)
func GetTopNodesByOutgoingEdges(db *sqlx.DB, graphID uint32, limit int) ([]*Node, []int, error) {
	query := `
		SELECT 
			n.id, n.graph_id, n.type, n.properties, n.created_at, n.updated_at,
			COALESCE(edge_counts.count, 0) as edge_count
		FROM node n
		LEFT JOIN (
			SELECT source as node_id, COUNT(*) as count
			FROM edge 
			WHERE graph_id = ?
			GROUP BY source
		) edge_counts ON n.id = edge_counts.node_id
		WHERE n.graph_id = ?
		ORDER BY edge_count DESC, n.id ASC
	`

	if limit > 0 {
		query += " LIMIT ?"
	}

	var args []interface{}
	args = append(args, graphID, graphID)
	if limit > 0 {
		args = append(args, limit)
	}

	// Use a temporary struct to capture both node data and edge count
	type nodeWithCount struct {
		ID         uint32           `db:"id"`
		GraphID    uint32           `db:"graph_id"`
		Type       *string          `db:"type"`
		Properties *json.RawMessage `db:"properties"`
		CreatedAt  time.Time        `db:"created_at"`
		UpdatedAt  time.Time        `db:"updated_at"`
		EdgeCount  int              `db:"edge_count"`
	}

	var results []nodeWithCount
	err := db.Select(&results, query, args...)
	if err != nil {
		return nil, nil, err
	}

	// Separate into two arrays
	nodes := make([]*Node, len(results))
	edgeCounts := make([]int, len(results))

	for i, result := range results {
		nodes[i] = &Node{
			ID:         result.ID,
			GraphID:    result.GraphID,
			Type:       result.Type,
			Properties: result.Properties,
			CreatedAt:  result.CreatedAt,
			UpdatedAt:  result.UpdatedAt,
		}
		edgeCounts[i] = result.EdgeCount
	}

	return nodes, edgeCounts, nil
}

// LoadGEXF loads nodes and edges from a GEXF file into an existing graph
// The graph must already exist in the database
func LoadGEXF(db *sqlx.DB, graphID uint32, path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()

	return LoadGEXFFromReader(db, graphID, f)
}

// LoadGEXFFromReader loads nodes and edges from a GEXF reader into an existing graph
// The graph must already exist in the database
func LoadGEXFFromReader(db *sqlx.DB, graphID uint32, reader io.Reader) error {
	var gexf gexf12.Content
	if err := xml.NewDecoder(reader).Decode(&gexf); err != nil {
		return err
	}

	// Create a map to track node IDs from GEXF to our database IDs
	nodeIDMap := make(map[string]uint32)

	// First, create all nodes
	for _, n := range gexf.Graph.Nodes.Nodes {
		// Convert properties to JSON
		properties := make(map[string]interface{})
		if n.AttValues != nil {
			for _, att := range n.AttValues.AttValues {
				properties[att.For] = att.Value
			}
		}
		properties["id"] = string(n.ID)

		// Convert to JSON string
		propsJSON, err := json.Marshal(properties)
		if err != nil {
			return fmt.Errorf("failed to marshal node properties: %w", err)
		}

		// Create node
		node := &Node{
			GraphID:    graphID,
			Properties: (*json.RawMessage)(&propsJSON),
		}

		nodeID, err := CreateNode(db, node)
		if err != nil {
			return fmt.Errorf("failed to create node %s: %w", n.ID, err)
		}

		// Store the mapping from GEXF ID to our database ID
		nodeIDMap[string(n.ID)] = nodeID
	}

	// Then, create all edges
	for _, e := range gexf.Graph.Edges.Edges {
		// Get the database IDs for source and target
		sourceID, exists := nodeIDMap[e.Source]
		if !exists {
			return fmt.Errorf("edge references missing source node: %s", e.Source)
		}

		targetID, exists := nodeIDMap[e.Target]
		if !exists {
			return fmt.Errorf("edge references missing target node: %s", e.Target)
		}

		// Convert edge properties to JSON
		properties := make(map[string]interface{})
		if e.AttValues != nil {
			for _, att := range e.AttValues.AttValues {
				properties[att.For] = att.Value
			}
		}

		// Convert to JSON string
		propsJSON, err := json.Marshal(properties)
		if err != nil {
			return fmt.Errorf("failed to marshal edge properties: %w", err)
		}

		// Create edge
		edge := &Edge{
			GraphID:    graphID,
			Source:     sourceID,
			Target:     targetID,
			Properties: (*json.RawMessage)(&propsJSON),
		}

		_, err = CreateEdge(db, edge)
		if err != nil {
			return fmt.Errorf("failed to create edge %s -> %s: %w", e.Source, e.Target, err)
		}
	}

	return nil
}
