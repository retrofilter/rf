package schema

import (
	"errors"
	"testing"
	"testing/fstest"

	"github.com/jmoiron/sqlx"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLoadMigrations(t *testing.T) {
	// Create a mock embedded FS with migration files
	fs := fstest.MapFS{
		"migrations/20250306_create_tables.up.sql": &fstest.MapFile{
			Data: []byte("CREATE TABLE users (id INTEGER PRIMARY KEY);"),
		},
		"migrations/20250306_create_tables.down.sql": &fstest.MapFile{
			Data: []byte("DROP TABLE users;"),
		},
		"migrations/20250307_add_index.up.sql": &fstest.MapFile{
			Data: []byte("CREATE INDEX idx_users ON users(id);"),
		},
		"migrations/20250307_add_index.down.sql": &fstest.MapFile{
			Data: []byte("DROP INDEX idx_users;"),
		},
		"migrations/invalid.txt": &fstest.MapFile{ // Should be ignored
			Data: []byte("not a migration"),
		},
	}

	tests := []struct {
		name    string
		fs      fstest.MapFS
		want    Migrations
		wantErr bool
	}{
		{
			name: "valid migrations",
			fs:   fs,
			want: Migrations{
				{
					Version: "20250306",
					Name:    "create_tables",
					Up:      "CREATE TABLE users (id INTEGER PRIMARY KEY);",
					Down:    "DROP TABLE users;",
				},
				{
					Version: "20250307",
					Name:    "add_index",
					Up:      "CREATE INDEX idx_users ON users(id);",
					Down:    "DROP INDEX idx_users;",
				},
			},
			wantErr: false,
		},
		{
			name: "missing up migration",
			fs: fstest.MapFS{
				"migrations/20250306_create_tables.down.sql": &fstest.MapFile{
					Data: []byte("DROP TABLE users;"),
				},
			},
			want:    nil,
			wantErr: true,
		},
		{
			name: "invalid version",
			fs: fstest.MapFS{
				"migrations/abc_create_tables.up.sql": &fstest.MapFile{
					Data: []byte("CREATE TABLE users (id INTEGER);"),
				},
			},
			want:    nil,
			wantErr: true,
		},
		{
			name: "two migrations sharing a version",
			fs: fstest.MapFS{
				"migrations/20250306_create_tables.up.sql": &fstest.MapFile{
					Data: []byte("CREATE TABLE users (id INTEGER);"),
				},
				"migrations/20250306_add_index.up.sql": &fstest.MapFile{
					Data: []byte("CREATE INDEX idx_users ON users(id);"),
				},
			},
			want:    nil,
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := LoadMigrations(tt.fs)
			if tt.wantErr {
				assert.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestMigrate(t *testing.T) {
	// Setup in-memory database
	db, err := sqlx.Open("sqlite", ":memory:")
	require.NoError(t, err)
	defer db.Close()

	// Mock embedded FS with migrations
	fs := fstest.MapFS{
		"migrations/20250306_create_tables.up.sql": &fstest.MapFile{
			Data: []byte("CREATE TABLE users (id INTEGER PRIMARY KEY);"),
		},
		"migrations/20250306_create_tables.down.sql": &fstest.MapFile{
			Data: []byte("DROP TABLE users;"),
		},
	}

	t.Run("apply migrations", func(t *testing.T) {
		err := Migrate(db, fs)
		require.NoError(t, err)

		// Check that the table was created
		var count int
		err = db.Get(&count, "SELECT count(*) FROM sqlite_master WHERE type='table' AND name='users'")
		require.NoError(t, err)
		assert.Equal(t, 1, count)

		// Check that the migration was recorded
		var version string
		err = db.Get(&version, "SELECT version FROM migrations WHERE name='create_tables'")
		require.NoError(t, err)
		assert.Equal(t, "20250306", version)

		// Run again to ensure it doesn't reapply
		err = Migrate(db, fs)
		require.NoError(t, err)
		err = db.Get(&count, "SELECT count(*) FROM migrations")
		require.NoError(t, err)
		assert.Equal(t, 1, count) // Still only one migration recorded
	})
}

func TestRollback(t *testing.T) {
	// Setup in-memory database
	db, err := sqlx.Open("sqlite", ":memory:")
	require.NoError(t, err)
	defer db.Close()

	// Mock embedded FS with migrations
	fs := fstest.MapFS{
		"migrations/20250306_create_tables.up.sql": &fstest.MapFile{
			Data: []byte("CREATE TABLE users (id INTEGER PRIMARY KEY);"),
		},
		"migrations/20250306_create_tables.down.sql": &fstest.MapFile{
			Data: []byte("DROP TABLE users;"),
		},
		"migrations/20250307_add_index.up.sql": &fstest.MapFile{
			Data: []byte("CREATE INDEX idx_users ON users(id);"),
		},
		"migrations/20250307_add_index.down.sql": &fstest.MapFile{
			Data: []byte("DROP INDEX idx_users;"),
		},
	}

	t.Run("rollback last migration", func(t *testing.T) {
		// Apply migrations
		err := Migrate(db, fs)
		require.NoError(t, err)

		// Check initial state
		var count int
		err = db.Get(&count, "SELECT count(*) FROM migrations")
		require.NoError(t, err)
		assert.Equal(t, 2, count)

		// Rollback the last migration
		err = Rollback(db, fs)
		require.NoError(t, err)

		err = db.Get(&count, "SELECT count(*) FROM sqlite_master WHERE type='index' AND name='idx_users'")
		require.NoError(t, err)
		assert.Equal(t, 0, count)

		// Check that the migration was removed
		err = db.Get(&count, "SELECT count(*) FROM migrations")
		require.NoError(t, err)
		assert.Equal(t, 1, count)

		// Check that the remaining migration is the first one
		var version string
		err = db.Get(&version, "SELECT version FROM migrations")
		require.NoError(t, err)
		assert.Equal(t, "20250306", version)
	})

}

func TestMigrateAcrossBinaryVersions(t *testing.T) {
	older := fstest.MapFS{
		"migrations/20250306_create_tables.up.sql": &fstest.MapFile{
			Data: []byte("CREATE TABLE users (id INTEGER PRIMARY KEY);"),
		},
	}
	newer := fstest.MapFS{
		"migrations/20250306_create_tables.up.sql": older["migrations/20250306_create_tables.up.sql"],
		"migrations/20250307_add_index.up.sql": &fstest.MapFile{
			Data: []byte("CREATE INDEX idx_users ON users(id);"),
		},
	}

	t.Run("old db, new binary migrates forward", func(t *testing.T) {
		db, err := sqlx.Open("sqlite", ":memory:")
		require.NoError(t, err)
		defer db.Close()
		require.NoError(t, Migrate(db, older))
		require.NoError(t, Migrate(db, newer))

		var count int
		require.NoError(t, db.Get(&count, "SELECT count(*) FROM sqlite_master WHERE type='index' AND name='idx_users'"))
		assert.Equal(t, 1, count)
		require.NoError(t, db.Get(&count, "SELECT count(*) FROM migrations"))
		assert.Equal(t, 2, count)
	})

	t.Run("new db, old binary fails loudly", func(t *testing.T) {
		db, err := sqlx.Open("sqlite", ":memory:")
		require.NoError(t, err)
		defer db.Close()
		require.NoError(t, Migrate(db, newer))

		err = Migrate(db, older)
		require.Error(t, err)
		assert.True(t, errors.Is(err, ErrSchemaNewer), "got %v", err)
		assert.Contains(t, err.Error(), "20250307_add_index")
		assert.Contains(t, err.Error(), "up to 20250306_create_tables")
		assert.Contains(t, err.Error(), "upgrade rf")

		// Nothing was rolled back or re-recorded: the newer schema is intact.
		var count int
		require.NoError(t, db.Get(&count, "SELECT count(*) FROM migrations"))
		assert.Equal(t, 2, count)
	})

	t.Run("orphan from a reverted dev migration is ignored", func(t *testing.T) {
		db, err := sqlx.Open("sqlite", ":memory:")
		require.NoError(t, err)
		defer db.Close()
		require.NoError(t, Migrate(db, migrationsFS))
		_, err = db.Exec("INSERT INTO migrations (version, name, applied_at) VALUES ('20260731', 'agent_status', 0), ('20260803', 'agent_mode', 0)")
		require.NoError(t, err)
		require.NoError(t, Migrate(db, migrationsFS))
	})

	t.Run("the real migration set guards too", func(t *testing.T) {
		db, err := sqlx.Open("sqlite", ":memory:")
		require.NoError(t, err)
		defer db.Close()
		require.NoError(t, Migrate(db, migrationsFS))
		_, err = db.Exec("INSERT INTO migrations (version, name, applied_at) VALUES ('29990101', 'from_the_future', 0)")
		require.NoError(t, err)
		err = Migrate(db, migrationsFS)
		assert.True(t, errors.Is(err, ErrSchemaNewer), "got %v", err)
	})
}
