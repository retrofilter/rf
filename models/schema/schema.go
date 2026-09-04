package schema

import (
	"database/sql"
	"embed"
	"strings"
	"sync"

	"github.com/jmoiron/sqlx"
	"github.com/qustavo/sqlhooks/v2"
	"github.com/retrofilter/rf/logger"
	_ "modernc.org/sqlite" // pure-Go sqlite with FTS5 built in (no cgo, no build tags)
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

var registerOnce sync.Once

func registerLoggingDriver() {
	registerOnce.Do(func() {
		base, err := sql.Open("sqlite", ":memory:")
		if err != nil {
			panic(err)
		}
		drv := base.Driver()
		base.Close()
		sql.Register("sqliteWithLogging", sqlhooks.Wrap(drv, &LoggingHooks{}))
	})
}

// OpenDB opens a connection to the SQLite database with logging
func OpenDB(dbPath string) (*sqlx.DB, error) {
	return OpenDBWithLogging(dbPath)
}

// OpenDBWithLogging opens a SQLite database connection with logging hooks.
func OpenDBWithLogging(dbPath string) (*sqlx.DB, error) {
	registerLoggingDriver()

	sep := "?"
	if strings.Contains(dbPath, "?") {
		sep = "&"
	}
	dsn := dbPath + sep + "_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)&_txlock=immediate"
	db, err := sqlx.Open("sqliteWithLogging", dsn)
	if err != nil {
		return nil, err
	}

	logger.Debug().
		Str("driver", "sqlite").
		Str("path", dbPath).
		Msg("Database connection opened")

	return db, nil
}

// CreateTables creates the necessary tables in the database
func CreateTables(db *sqlx.DB) error {
	return Migrate(db, migrationsFS)
}
