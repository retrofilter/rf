package core

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/jmoiron/sqlx"
	"github.com/retrofilter/rf/logger"
	"github.com/retrofilter/rf/models"
	"gonum.org/v1/gonum/graph/path"
)

// Graph is the handle all node/edge access goes through.
type Graph struct {
	ID        uint32
	Name      string
	Metadata  *json.RawMessage
	CreatedAt time.Time
	UpdatedAt time.Time

	db  *sqlx.DB
	emb *Embeddings
}

// GetNode returns a node by ID
func (g *Graph) GetNode(ctx context.Context, nodeID uint32) (*models.Node, error) {
	node, err := models.GetNode(g.db, nodeID)
	if err != nil {
		return nil, fmt.Errorf("node not found: %w", err)
	}
	return node, nil
}

func (g *Graph) InsertNode(ctx context.Context, node *models.Node) (uint32, error) {
	return models.CreateNode(g.db, node)
}

func (g *Graph) UpdateNode(ctx context.Context, node *models.Node) (bool, error) {
	if err := models.UpdateNode(g.db, node); err != nil {
		return false, err
	}
	return true, nil
}

// DeleteNode removes a node; its edges cascade via foreign keys.
func (g *Graph) DeleteNode(ctx context.Context, nodeID uint32) error {
	return models.DeleteNode(g.db, nodeID)
}

func (g *Graph) InsertEdge(ctx context.Context, edge *models.Edge) (uint32, error) {
	return models.CreateEdge(g.db, edge)
}

// DeleteEdge removes an edge by id.
func (g *Graph) DeleteEdge(ctx context.Context, edgeID uint32) error {
	return models.DeleteEdge(g.db, edgeID)
}

// SearchTasks lists this graph's task nodes matching q, oldest first.
func (g *Graph) SearchTasks(q models.TaskQuery) ([]models.TaskRow, error) {
	return models.SearchTasks(g.db, g.ID, q)
}

// OpenTaskCounts counts open tasks per project node id.
func (g *Graph) OpenTaskCounts() (map[uint32]int, error) {
	return models.OpenTaskCountsByProject(g.db, g.ID)
}

// GetEdge returns an edge by ID
func (g *Graph) GetEdge(ctx context.Context, edgeID uint32) (*models.Edge, error) {
	edge, err := models.GetEdge(g.db, edgeID)
	if err != nil {
		return nil, fmt.Errorf("edge not found: %w", err)
	}
	return edge, nil
}

// GetNodeEdges returns every edge touching the node, either direction.
func (g *Graph) GetNodeEdges(ctx context.Context, nodeID uint32) ([]*models.Edge, error) {
	return models.GetEdgesByNode(g.db, nodeID)
}

// GetNeighbors returns all neighboring nodes for a given node
func (g *Graph) GetNeighbors(ctx context.Context, nodeID uint32) ([]*models.Node, error) {
	edges, err := models.GetEdgesByNode(g.db, nodeID)
	if err != nil {
		return nil, err
	}
	neighbors := make([]*models.Node, 0, len(edges))
	for _, edge := range edges {
		if edge.Source != nodeID {
			continue
		}
		neighbor, err := g.GetNode(ctx, edge.Target)
		if err != nil {
			logger.Warn().Uint32("node_id", edge.Target).Err(err).Msg("Failed to load neighbor node")
			continue
		}
		neighbors = append(neighbors, neighbor)
	}
	return neighbors, nil
}

// GetIncomingNeighbors returns all nodes that have edges pointing to the given node
func (g *Graph) GetIncomingNeighbors(ctx context.Context, nodeID uint32) ([]*models.Node, error) {
	edges, err := models.GetEdgesByNode(g.db, nodeID)
	if err != nil {
		return nil, err
	}
	neighbors := make([]*models.Node, 0, len(edges))
	for _, edge := range edges {
		if edge.Target != nodeID {
			continue
		}
		neighbor, err := g.GetNode(ctx, edge.Source)
		if err != nil {
			logger.Warn().Uint32("node_id", edge.Source).Err(err).Msg("Failed to load incoming neighbor node")
			continue
		}
		neighbors = append(neighbors, neighbor)
	}
	return neighbors, nil
}

func (g *Graph) GetNodesByType(nodeType string, limit, offset int) ([]*models.Node, error) {
	return models.GetNodesByType(g.db, g.ID, nodeType, limit, offset)
}

func (g *Graph) GetEdgesByType(edgeType string, limit, offset int) ([]*models.Edge, error) {
	return models.GetEdgesByType(g.db, g.ID, edgeType, limit, offset)
}

func (g *Graph) GetNodesByGraph(limit, offset int) ([]*models.Node, error) {
	return models.GetNodesByGraph(g.db, g.ID, limit, offset)
}

func (g *Graph) GetEdgesByGraph(limit, offset int) ([]*models.Edge, error) {
	return models.GetEdgesByGraph(g.db, g.ID, limit, offset)
}

func (g *Graph) CountNodes() (int, error) {
	return models.CountNodesInGraph(g.db, g.ID)
}

func (g *Graph) CountEdges() (int, error) {
	return models.CountEdgesInGraph(g.db, g.ID)
}

func (g *Graph) GetGraphMeta() (*models.Graph, error) {
	return models.GetGraph(g.db, g.ID)
}

func (g *Graph) GetTopNodesByIncomingEdges(limit int) ([]*models.Node, []int, error) {
	return models.GetTopNodesByIncomingEdges(g.db, g.ID, limit)
}

func (g *Graph) GetTopNodesByOutgoingEdges(limit int) ([]*models.Node, []int, error) {
	return models.GetTopNodesByOutgoingEdges(g.db, g.ID, limit)
}

// SearchNodes full-text searches this graph's nodes via the FTS index.
// A non-empty nodeType restricts results to that type.
func (g *Graph) SearchNodes(query string, nodeType string, limit int) ([]*models.Node, []float64, error) {
	return models.SearchNodes(g.db, g.ID, query, nodeType, limit)
}

// SearchNodesLexical is SearchNodes with the prose fallback: a query FTS5
// rejects as syntax retries as an OR of its word tokens.
func (g *Graph) SearchNodesLexical(query string, nodeType string, limit int) ([]*models.Node, []float64, error) {
	return models.SearchNodesLexical(g.db, g.ID, query, nodeType, limit)
}

// NodesByType lists this graph's nodes of one type, oldest first.
func (g *Graph) NodesByType(nodeType string, limit int) ([]*models.Node, error) {
	return models.GetNodesByType(g.db, g.ID, nodeType, limit, 0)
}

// GetNodesByAttributes returns nodes whose properties match all given attributes
func (g *Graph) GetNodesByAttributes(ctx context.Context, attributes map[string]interface{}) ([]*models.Node, error) {
	return models.GetNodesByAttributes(g.db, g.ID, attributes, 0, 0)
}

func (g *Graph) uniqueNodeByAttributes(ctx context.Context, role string, attrs map[string]interface{}) (*models.Node, error) {
	nodes, err := g.GetNodesByAttributes(ctx, attrs)
	if err != nil {
		return nil, fmt.Errorf("failed to find %s node: %w", role, err)
	}
	if len(nodes) == 0 {
		return nil, fmt.Errorf("no %s node found matching attributes: %v", role, attrs)
	}
	if len(nodes) > 1 {
		return nil, fmt.Errorf("multiple %s nodes found matching attributes: %v", role, attrs)
	}
	return nodes[0], nil
}

// ShortestPath finds the shortest path between two nodes identified by their
// attributes. It snapshots the whole graph into memory for the query —
// consistent by construction, and cheap at personal scale.
func (g *Graph) ShortestPath(ctx context.Context, startAttrs, endAttrs map[string]interface{}) ([]*models.Node, error) {
	startNode, err := g.uniqueNodeByAttributes(ctx, "start", startAttrs)
	if err != nil {
		return nil, err
	}
	endNode, err := g.uniqueNodeByAttributes(ctx, "end", endAttrs)
	if err != nil {
		return nil, err
	}

	snap, err := snapshotGraph(g)
	if err != nil {
		return nil, err
	}
	start := snap.Node(int64(startNode.ID))
	if start == nil {
		return nil, fmt.Errorf("start node %d not in graph", startNode.ID)
	}

	sp := path.DijkstraFrom(start, snap)
	pathNodes, _ := sp.To(int64(endNode.ID))
	if len(pathNodes) == 0 {
		return nil, fmt.Errorf("no path found between nodes")
	}

	result := make([]*models.Node, len(pathNodes))
	for i, node := range pathNodes {
		result[i] = node.(*snapshotNode).Node
	}
	return result, nil
}
