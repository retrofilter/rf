package models

import (
	"github.com/jmoiron/sqlx"
)

// TaskQuery filters SearchTasks. Zero values mean "no filter".
type TaskQuery struct {
	Project string // only tasks whose `for` edge targets the project node with this name
	Status  string // "open" or "done"
	Ready   bool   // only tasks with no open blocker
	Limit   int    // newest N (0 = all)
}

// TaskRow is a task node joined with the name of the project it is filed
// under ("" for unattached tasks).
type TaskRow struct {
	Node
	Project string `db:"project"`
}

// SearchTasks returns a graph's task nodes matching q, oldest first —
// the dependency-aware listing flat storage can't give: Ready is a single
// query over edge joined to node.
func SearchTasks(db *sqlx.DB, graphID uint32, q TaskQuery) ([]TaskRow, error) {
	query := `
		SELECT t.id, t.graph_id, t.type, t.properties, t.created_at, t.updated_at,
		       COALESCE(json_extract(p.properties, '$.name'), '') AS project
		FROM node t
		LEFT JOIN edge f ON f.source = t.id AND f.type = 'for'
		LEFT JOIN node p ON p.id = f.target
		WHERE t.graph_id = ? AND t.type = 'task'
	`
	args := []interface{}{graphID}
	if q.Status != "" {
		query += ` AND json_extract(t.properties, '$.status') = ?`
		args = append(args, q.Status)
	}
	if q.Project != "" {
		query += ` AND json_extract(p.properties, '$.name') = ?`
		args = append(args, q.Project)
	}
	if q.Ready {
		query += ` AND NOT EXISTS (
			SELECT 1 FROM edge b JOIN node s ON s.id = b.source
			WHERE b.type = 'blocks' AND b.target = t.id
			  AND s.type = 'task' AND json_extract(s.properties, '$.status') = 'open'
		)`
	}
	query += ` ORDER BY t.created_at DESC, t.id DESC`
	if q.Limit > 0 {
		query += ` LIMIT ?`
		args = append(args, q.Limit)
	}

	var rows []TaskRow
	if err := db.Select(&rows, query, args...); err != nil {
		return nil, err
	}
	for i, j := 0, len(rows)-1; i < j; i, j = i+1, j-1 {
		rows[i], rows[j] = rows[j], rows[i]
	}
	return rows, nil
}

// OpenTaskCountsByProject counts open tasks per project node id — the
// `projects` listing's task column in one query.
func OpenTaskCountsByProject(db *sqlx.DB, graphID uint32) (map[uint32]int, error) {
	query := `
		SELECT f.target AS project_id, COUNT(*) AS n
		FROM node t
		JOIN edge f ON f.source = t.id AND f.type = 'for'
		WHERE t.graph_id = ? AND t.type = 'task'
		  AND json_extract(t.properties, '$.status') = 'open'
		GROUP BY f.target
	`
	var rows []struct {
		ProjectID uint32 `db:"project_id"`
		N         int    `db:"n"`
	}
	if err := db.Select(&rows, query, graphID); err != nil {
		return nil, err
	}
	counts := make(map[uint32]int, len(rows))
	for _, r := range rows {
		counts[r.ProjectID] = r.N
	}
	return counts, nil
}
