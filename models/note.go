package models

import (
	"github.com/jmoiron/sqlx"
)

// NoteQuery filters SearchNotes. Zero values mean "no filter".
type NoteQuery struct {
	Project string // only notes whose `for` edge targets the project node with this name
	Limit   int    // newest N (0 = all)
}

// NoteRow is a note node joined with the name of the project it is filed
// under ("" for unattached notes).
type NoteRow struct {
	Node
	Project string `db:"project"`
}

// SearchNotes returns a graph's note nodes matching q, most recently
// updated first.
func SearchNotes(db *sqlx.DB, graphID uint32, q NoteQuery) ([]NoteRow, error) {
	query := `
		SELECT n.id, n.graph_id, n.type, n.properties, n.created_at, n.updated_at,
		       COALESCE(json_extract(p.properties, '$.name'), '') AS project
		FROM node n
		LEFT JOIN edge f ON f.source = n.id AND f.type = 'for'
		LEFT JOIN node p ON p.id = f.target
		WHERE n.graph_id = ? AND n.type = 'note'
	`
	args := []interface{}{graphID}
	if q.Project != "" {
		query += ` AND json_extract(p.properties, '$.name') = ?`
		args = append(args, q.Project)
	}
	query += ` ORDER BY n.updated_at DESC, n.id DESC`
	if q.Limit > 0 {
		query += ` LIMIT ?`
		args = append(args, q.Limit)
	}
	var rows []NoteRow
	if err := db.Select(&rows, query, args...); err != nil {
		return nil, err
	}
	return rows, nil
}
