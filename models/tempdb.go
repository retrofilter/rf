package models

import (
	"github.com/jmoiron/sqlx"
	"github.com/retrofilter/rf/models/schema"
)

// CreateTempDB opens an in-memory SQLite database with the full schema
// applied. Used by tests.
func CreateTempDB() (*sqlx.DB, error) {
	db, err := sqlx.Open("sqlite", "file::memory:?cache=shared&_pragma=foreign_keys(1)")
	if err != nil {
		return nil, err
	}

	err = schema.CreateTables(db)
	if err != nil {
		return nil, err
	}

	return db, nil
}
