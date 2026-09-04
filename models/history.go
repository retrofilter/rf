package models

import (
	"database/sql"
	"strings"
	"time"

	"github.com/jmoiron/sqlx"
)

// HistoryEntry is one line accepted at the shell prompt: what ran, where,
// in which mode, and how it exited.
type HistoryEntry struct {
	ID        int64         `db:"id"`
	Line      string        `db:"line"`
	Mode      string        `db:"mode"`
	Cwd       string        `db:"cwd"`
	Project   string        `db:"project"`
	ExitCode  sql.NullInt64 `db:"exit_code"`
	Session   string        `db:"session"`
	CreatedAt time.Time     `db:"created_at"`
}

// AddHistory inserts a history entry and sets its ID.
func AddHistory(db *sqlx.DB, e *HistoryEntry) error {
	res, err := db.Exec(
		`INSERT INTO history (line, mode, cwd, project, session) VALUES (?, ?, ?, ?, ?)`,
		e.Line, e.Mode, e.Cwd, e.Project, e.Session,
	)
	if err != nil {
		return err
	}
	e.ID, err = res.LastInsertId()
	return err
}

// UpdateHistoryLine rewrites an entry's line and mode — bash's own
// history-expansion behavior: a `!!` line is recorded as what it expanded
// to, so recall replays the runnable command, never the designator.
func UpdateHistoryLine(db *sqlx.DB, id int64, line, mode string) error {
	_, err := db.Exec(`UPDATE history SET line = ?, mode = ? WHERE id = ?`, line, mode, id)
	return err
}

// SetHistoryExit records the exit code of an entry once its line has run.
func SetHistoryExit(db *sqlx.DB, id int64, code int) error {
	_, err := db.Exec(`UPDATE history SET exit_code = ? WHERE id = ?`, code, id)
	return err
}

// HistoryQuery filters SearchHistory. Zero values mean "no filter";
// Dir matches the directory itself and everything beneath it.
type HistoryQuery struct {
	Pattern   string // substring of the line
	Dir       string // absolute path; matches the dir and its subtree
	Mode      string // command | chat | scheme
	Project   string // project name
	Limit     int    // defaults to 25
	ExcludeID int64  // omit this row — the line currently executing
}

// SearchHistory returns the most recent matching entries, oldest first
// (so the latest command reads last, like shell history).
func SearchHistory(db *sqlx.DB, q HistoryQuery) ([]HistoryEntry, error) {
	where := []string{"1=1"}
	args := []any{}
	if q.Pattern != "" {
		where = append(where, "line LIKE ? ESCAPE '\\'")
		args = append(args, "%"+escapeLike(q.Pattern)+"%")
	}
	if q.Dir != "" {
		dir := strings.TrimRight(q.Dir, "/")
		where = append(where, "(cwd = ? OR cwd LIKE ? ESCAPE '\\')")
		args = append(args, dir, escapeLike(dir)+"/%")
	}
	if q.Mode != "" {
		where = append(where, "mode = ?")
		args = append(args, q.Mode)
	}
	if q.Project != "" {
		where = append(where, "project = ?")
		args = append(args, q.Project)
	}
	if q.ExcludeID != 0 {
		where = append(where, "id != ?")
		args = append(args, q.ExcludeID)
	}
	limit := q.Limit
	if limit <= 0 {
		limit = 25
	}
	args = append(args, limit)

	var entries []HistoryEntry
	err := db.Select(&entries,
		`SELECT * FROM history WHERE `+strings.Join(where, " AND ")+` ORDER BY id DESC LIMIT ?`,
		args...)
	if err != nil {
		return nil, err
	}
	for i, j := 0, len(entries)-1; i < j; i, j = i+1, j-1 {
		entries[i], entries[j] = entries[j], entries[i]
	}
	return entries, nil
}

// HistorySeed is the slice of a history entry the readline source keeps in
// memory: the line, plus the directory and mode that rank autosuggestions.
type HistorySeed struct {
	Line string `db:"line"`
	Cwd  string `db:"cwd"`
	Mode string `db:"mode"`
}

// RecentHistorySeeds returns the last n entries, oldest first, for seeding
// the readline history source (and its autosuggestions) at startup.
func RecentHistorySeeds(db *sqlx.DB, n int) ([]HistorySeed, error) {
	var seeds []HistorySeed
	err := db.Select(&seeds,
		`SELECT line, cwd, mode FROM (SELECT id, line, cwd, mode FROM history ORDER BY id DESC LIMIT ?) ORDER BY id`, n)
	return seeds, err
}

func escapeLike(s string) string {
	r := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)
	return r.Replace(s)
}
