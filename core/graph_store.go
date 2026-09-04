package core

import (
	"fmt"

	"github.com/jmoiron/sqlx"
	"github.com/retrofilter/rf/models"
)

// GraphStore hands out Graph handles.
type GraphStore struct {
	db  *sqlx.DB
	emb *Embeddings
}

func NewGraphStore(db *sqlx.DB) *GraphStore {
	return &GraphStore{db: db, emb: newEmbeddings(db)}
}

// Embeddings is the store's quantized-embedding cache — inert until
// Configure enables it.
func (gs *GraphStore) Embeddings() *Embeddings { return gs.emb }

// DB returns the database connection used by the GraphStore
func (gs *GraphStore) DB() *sqlx.DB {
	return gs.db
}

func (gs *GraphStore) CreateGraph(name string) (uint32, error) {
	graph := &models.Graph{Name: name}
	return models.CreateGraph(gs.db, graph)
}

func (gs *GraphStore) DeleteGraph(name string) error {
	graph, err := models.GetGraphByName(gs.db, name)
	if err != nil {
		return fmt.Errorf("graph not found: %w", err)
	}
	return models.DeleteGraph(gs.db, graph.ID)
}

func (gs *GraphStore) GetGraph(name string) (*Graph, error) {
	graph, err := models.GetGraphByName(gs.db, name)
	if err != nil {
		return nil, fmt.Errorf("graph not found: %w", err)
	}
	return &Graph{
		ID:        graph.ID,
		Name:      graph.Name,
		Metadata:  graph.Metadata,
		CreatedAt: graph.CreatedAt,
		UpdatedAt: graph.UpdatedAt,
		db:        gs.db,
		emb:       gs.emb,
	}, nil
}

// ListGraphs returns a list of all graph names
func (gs *GraphStore) ListGraphs() ([]string, error) {
	graphs, err := models.GetAllGraphs(gs.db, 0, 0)
	if err != nil {
		return nil, fmt.Errorf("failed to list graphs: %w", err)
	}

	names := make([]string, len(graphs))
	for i, graph := range graphs {
		names[i] = graph.Name
	}

	return names, nil
}
