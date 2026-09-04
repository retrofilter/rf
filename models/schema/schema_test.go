package schema

import (
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"
)

func TestSchema(t *testing.T) {
	// Open an in-memory SQLite database
	db, err := OpenDB("file::memory:?cache=shared")
	if err != nil {
		t.Fatalf("failed to open database: %v", err)
	}
	defer db.Close()

	// Test creating tables
	err = CreateTables(db)
	if err != nil {
		t.Fatalf("failed to create tables: %v", err)
	}

	// Test if the tables exist (node_fts is the FTS5 virtual table)
	tables := []string{"graph", "node", "edge", "history", "node_fts"}
	for _, table := range tables {
		row := db.QueryRow("SELECT name FROM sqlite_master WHERE type='table' AND name=?", table)
		var name string
		err := row.Scan(&name)
		if err != nil {
			t.Errorf("table %s does not exist", table)
		}
	}
}

func TestForeignKeysEnforced(t *testing.T) {
	db, err := OpenDB(filepath.Join(t.TempDir(), "fk.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := CreateTables(db); err != nil {
		t.Fatal(err)
	}

	var fk int
	if err := db.Get(&fk, "PRAGMA foreign_keys"); err != nil || fk != 1 {
		t.Fatalf("PRAGMA foreign_keys = %d (err %v), want 1", fk, err)
	}

	res, err := db.Exec(`INSERT INTO graph (name) VALUES ('fk-test')`)
	if err != nil {
		t.Fatal(err)
	}
	gid, _ := res.LastInsertId()
	res, err = db.Exec(`INSERT INTO node (graph_id, type, properties) VALUES (?, 'note', '{"name":"a"}')`, gid)
	if err != nil {
		t.Fatal(err)
	}
	nid, _ := res.LastInsertId()
	if _, err := db.Exec(`INSERT INTO edge (graph_id, source, target) VALUES (?, ?, ?)`, gid, nid, nid); err != nil {
		t.Fatal(err)
	}

	// A dangling reference is rejected outright.
	if _, err := db.Exec(`INSERT INTO edge (graph_id, source, target) VALUES (?, 999999, 999999)`, gid); err == nil {
		t.Error("edge with dangling endpoints should violate the foreign key")
	}

	if _, err := db.Exec(`DELETE FROM graph WHERE id = ?`, gid); err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"node", "edge", "node_fts"} {
		var n int
		if err := db.Get(&n, "SELECT count(*) FROM "+table); err != nil {
			t.Fatal(err)
		}
		if n != 0 {
			t.Errorf("%s has %d orphan rows after graph delete", table, n)
		}
	}
}
