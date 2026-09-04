package cmd

import (
	"github.com/jmoiron/sqlx"
	"github.com/retrofilter/rf/core"
	"github.com/retrofilter/rf/models/schema"
)

func openMainDB() (*sqlx.DB, error) {
	dbPath, err := core.DBPath()
	if err != nil {
		return nil, err
	}
	db, err := schema.OpenDB(dbPath)
	if err != nil {
		return nil, err
	}
	if err := schema.CreateTables(db); err != nil {
		db.Close()
		return nil, err
	}
	return db, nil
}
