package schema

import (
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/retrofilter/rf/logger"

	"github.com/jmoiron/sqlx"
)

type Migration struct {
	Version string
	Name    string
	Up      string
	Down    string
}

type Migrations []Migration

func (m Migrations) Len() int           { return len(m) }
func (m Migrations) Less(i, j int) bool { return m[i].Version < m[j].Version }
func (m Migrations) Swap(i, j int)      { m[i], m[j] = m[j], m[i] }

func LoadMigrations(dir fs.FS) (Migrations, error) {
	var migrations Migrations
	migrationMap := make(map[string]*Migration)
	versionPattern := regexp.MustCompile(`^(\d{8})$`)

	err := fs.WalkDir(dir, ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}

		filename := d.Name()
		if !strings.HasSuffix(filename, ".sql") {
			return nil
		}

		parts := strings.SplitN(filename, "_", 2)
		if len(parts) != 2 {
			return fmt.Errorf("invalid migration filename format: %s (expected YYYYMMDD_description.up/down.sql)", filename)
		}

		version := parts[0]
		if !versionPattern.MatchString(version) {
			return fmt.Errorf("invalid version in filename: %s (expected 8-digit timestamp)", filename)
		}

		rest := parts[1]
		isUp := strings.HasSuffix(rest, ".up.sql")
		isDown := strings.HasSuffix(rest, ".down.sql")
		if !isUp && !isDown {
			return fmt.Errorf("migration file must end in .up.sql or .down.sql: %s", filename)
		}

		name := strings.TrimSuffix(rest, ".up.sql")
		if isDown {
			name = strings.TrimSuffix(rest, ".down.sql")
		}

		content, err := fs.ReadFile(dir, path)
		if err != nil {
			return fmt.Errorf("failed to read %s: %v", path, err)
		}

		mig, ok := migrationMap[version]
		if !ok {
			mig = &Migration{Version: version, Name: name}
			migrationMap[version] = mig
		} else if mig.Name != name {
			return fmt.Errorf("duplicate migration version %s: %s and %s", version, mig.Name, name)
		}

		if isUp {
			if mig.Up != "" {
				return fmt.Errorf("duplicate .up.sql for migration %s_%s", version, name)
			}
			mig.Up = string(content)
		} else {
			if mig.Down != "" {
				return fmt.Errorf("duplicate .down.sql for migration %s_%s", version, name)
			}
			mig.Down = string(content)
		}

		return nil
	})

	if err != nil {
		return nil, err
	}

	for _, mig := range migrationMap {
		if mig.Up == "" {
			return nil, fmt.Errorf("missing .up.sql for migration %s_%s", mig.Version, mig.Name)
		}
		migrations = append(migrations, *mig)
	}

	sort.Sort(migrations)
	return migrations, nil
}

// Migrate applies all pending migrations to the database
func Migrate(db *sqlx.DB, migrationsDir fs.FS) error {
	// Create migrations table if it doesn’t exist
	_, err := db.Exec(`
        CREATE TABLE IF NOT EXISTS migrations (
            version TEXT PRIMARY KEY,
            name TEXT NOT NULL,
            applied_at INTEGER NOT NULL
        )
    `)
	if err != nil {
		return fmt.Errorf("failed to create migrations table: %v", err)
	}

	applied := make(map[string]string)
	rows, err := db.Queryx("SELECT version, name FROM migrations")
	if err != nil {
		return fmt.Errorf("failed to query applied migrations: %v", err)
	}
	defer rows.Close()
	for rows.Next() {
		var version, name string
		if err := rows.Scan(&version, &name); err != nil {
			return fmt.Errorf("failed to scan migration version: %v", err)
		}
		applied[version] = name
	}

	// Load migration files
	migrations, err := LoadMigrations(migrationsDir)
	if err != nil {
		return fmt.Errorf("failed to load migrations: %v", err)
	}

	if err := checkSchemaNotNewer(migrations, applied); err != nil {
		return err
	}

	// Apply pending migrations
	for _, mig := range migrations {
		if _, done := applied[mig.Version]; done {
			continue
		}

		tx, err := db.Beginx()
		if err != nil {
			return fmt.Errorf("failed to begin transaction for %s: %v", mig.Version, err)
		}

		_, err = tx.Exec(mig.Up)
		if err != nil {
			tx.Rollback()
			return fmt.Errorf("failed to apply migration %s_%s: %v", mig.Version, mig.Name, err)
		}

		now := time.Now().UnixMilli()

		_, err = tx.Exec("INSERT INTO migrations (version, name, applied_at) VALUES (?, ?, ?)", mig.Version, mig.Name, now)
		if err != nil {
			tx.Rollback()
			return fmt.Errorf("failed to record migration %s_%s: %v", mig.Version, mig.Name, err)
		}

		if err := tx.Commit(); err != nil {
			return fmt.Errorf("failed to commit migration %s_%s: %v", mig.Version, mig.Name, err)
		}

		logger.Debug().Str("migration", fmt.Sprintf("%s_%s", mig.Version, mig.Name)).Msg("Applied migration")

	}

	return nil
}

// ErrSchemaNewer is returned (wrapped) by Migrate when the database records a
// migration later than any this binary carries — it was opened by a newer rf.
var ErrSchemaNewer = errors.New("database schema is newer than this rf")

func checkSchemaNotNewer(known Migrations, applied map[string]string) error {
	newest := ""
	if len(known) > 0 {
		newest = known[len(known)-1].Version
	}
	var later []string
	for version, name := range applied {
		if version > newest {
			later = append(later, version+"_"+name)
		} else if _, ok := knownVersion(known, version); !ok {
			logger.Debug().Str("migration", version+"_"+name).Msg("Ignoring orphan migration (reverted in development)")
		}
	}
	if len(later) == 0 {
		return nil
	}
	sort.Strings(later)
	newestLabel := "none"
	if len(known) > 0 {
		last := known[len(known)-1]
		newestLabel = last.Version + "_" + last.Name
	}
	return fmt.Errorf("%w: it was migrated by a later version (%s; this binary knows up to %s) — upgrade rf (rf --version shows this build)",
		ErrSchemaNewer, strings.Join(later, ", "), newestLabel)
}

func knownVersion(known Migrations, version string) (Migration, bool) {
	for _, mig := range known {
		if mig.Version == version {
			return mig, true
		}
	}
	return Migration{}, false
}

func Rollback(db *sqlx.DB, migrationsDir fs.FS) error {
	// Load all migrations
	migrations, err := LoadMigrations(migrationsDir)
	if err != nil {
		return fmt.Errorf("failed to load migrations: %v", err)
	}

	// Find the last applied migration
	var lastVersion string
	err = db.Get(&lastVersion, "SELECT version FROM migrations ORDER BY version DESC LIMIT 1")
	if err == sql.ErrNoRows {
		return fmt.Errorf("no migrations have been applied")
	} else if err != nil {
		return fmt.Errorf("failed to find last migration: %v", err)
	}

	logger.Info().Msgf("Last version is %s, rolling back", lastVersion)

	for _, mig := range migrations {
		if mig.Version == lastVersion {
			if mig.Down == "" {
				return fmt.Errorf("no .down.sql found for migration %s_%s", mig.Version, mig.Name)
			}

			tx, err := db.Beginx()
			if err != nil {
				return fmt.Errorf("failed to begin transaction for rollback %s: %v", mig.Version, err)
			}

			_, err = tx.Exec(mig.Down)
			if err != nil {
				tx.Rollback()
				return fmt.Errorf("failed to rollback migration %s_%s: %v", mig.Version, mig.Name, err)
			}

			_, err = tx.Exec("DELETE FROM migrations WHERE version = ?", mig.Version)
			if err != nil {
				tx.Rollback()
				return fmt.Errorf("failed to remove migration record %s_%s: %v", mig.Version, mig.Name, err)
			}

			if err := tx.Commit(); err != nil {
				return fmt.Errorf("failed to commit rollback %s_%s: %v", mig.Version, mig.Name, err)
			}

			logger.Info().Str("version", mig.Version).Msgf("Rolled back migration: %s_%s\n", mig.Version, mig.Name)
			return nil
		}
	}

	return fmt.Errorf("migration %s not found in migration files", lastVersion)
}
