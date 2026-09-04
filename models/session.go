package models

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jmoiron/sqlx"
)

// Session log entry kinds.
const (
	SessionKindUser       = "user"       // user text (typed line or steer)
	SessionKindAssistant  = "assistant"  // assistant text reply
	SessionKindTool       = "tool"       // executed scheme call, with its result
	SessionKindCompaction = "compaction" // summary checkpoint; see Compaction
)

// Session sources: which harness's transcript a session row holds.
const (
	SessionSourceRF     = "rf"
	SessionSourceClaude = "claude"
)

const tsFormat = "2006-01-02 15:04:05"

// Session is one shell process's chat transcript metadata, created lazily on
// the first logged entry. Its id is the same random id the history table
// records, so a chat line's history row and its session entries compose.
type Session struct {
	ID      string `db:"id"`
	Cwd     string `db:"cwd"`
	Project string `db:"project"`
	Title   string `db:"title"`
	// Parent is the spawning shell session's id for sub-agent sessions
	// ((agent ...) turns); empty for sessions the user drove directly.
	Parent string `db:"parent"`
	// Source is the harness the transcript came from: rf | claude.
	Source    string    `db:"source"`
	CreatedAt time.Time `db:"created_at"`
	UpdatedAt time.Time `db:"updated_at"`
	// Entries is the session's log-row count; populated by ListSessions.
	Entries int64 `db:"entries"`
}

// SessionEntry is one appended row of the session log.
type SessionEntry struct {
	ID       int64         `db:"id"`
	Session  string        `db:"session"`
	ParentID sql.NullInt64 `db:"parent_id"`
	Kind     string        `db:"kind"`
	Payload  string        `db:"payload"`
	// UUID is the origin transcript line id for synced rows (dedupe key);
	// NULL for rf's own entries.
	UUID      sql.NullString `db:"uuid"`
	CreatedAt time.Time      `db:"created_at"`
}

type textPayload struct {
	Content string `json:"content"`
}

type toolPayload struct {
	Name       string `json:"name"`
	Code       string `json:"code"`
	Result     string `json:"result,omitempty"`
	Error      string `json:"error,omitempty"`
	ToolCallID string `json:"tool_call_id,omitempty"`
}

// Compaction is the payload of a compaction entry.
type Compaction struct {
	Summary          string `json:"summary"`
	FirstKeptEntryID int64  `json:"first_kept_entry_id"`
	TokensBefore     int    `json:"tokens_before"`
}

// EnsureSession creates the session metadata row if it does not exist yet.
// parent links a sub-agent session to the shell session that spawned it;
// empty for ordinary sessions.
func EnsureSession(db *sqlx.DB, id, cwd, project, parent string) error {
	_, err := db.Exec(
		`INSERT INTO session (id, cwd, project, parent) VALUES (?, ?, ?, ?) ON CONFLICT (id) DO NOTHING`,
		id, cwd, project, parent,
	)
	return err
}

// AppendSessionMessage logs one transcript step and returns its entry id.
// The session row must already exist (EnsureSession).
func AppendSessionMessage(db *sqlx.DB, session string, m *Message) (int64, error) {
	var kind string
	var payload any
	switch {
	case m.Type == MessageTypeTool:
		kind = SessionKindTool
		payload = toolPayload{
			Name:       m.FunctionName,
			Code:       m.Code,
			Result:     m.Result,
			Error:      m.ErrorMessage,
			ToolCallID: m.ToolCallID,
		}
	case m.Role == MessageRoleUser:
		kind = SessionKindUser
		payload = textPayload{Content: m.Content}
	case m.Role == MessageRoleAssistant:
		kind = SessionKindAssistant
		payload = textPayload{Content: m.Content}
	default:
		return 0, fmt.Errorf("session: cannot log message role %q", m.Role)
	}
	return appendSessionEntry(db, session, kind, payload)
}

// AppendSessionCompaction logs a compaction checkpoint.
func AppendSessionCompaction(db *sqlx.DB, session string, c Compaction) (int64, error) {
	return appendSessionEntry(db, session, SessionKindCompaction, c)
}

func appendSessionEntry(db *sqlx.DB, session, kind string, payload any) (int64, error) {
	data, err := json.Marshal(payload)
	if err != nil {
		return 0, err
	}
	tx, err := db.Beginx()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()

	var parent sql.NullInt64
	err = tx.Get(&parent.Int64, `SELECT id FROM message WHERE session = ? ORDER BY id DESC LIMIT 1`, session)
	if err != nil && err != sql.ErrNoRows {
		return 0, err
	}
	parent.Valid = err == nil

	res, err := tx.Exec(
		`INSERT INTO message (session, parent_id, kind, payload) VALUES (?, ?, ?, ?)`,
		session, parent, kind, string(data),
	)
	if err != nil {
		return 0, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, err
	}
	if _, err := tx.Exec(`UPDATE session SET updated_at = CURRENT_TIMESTAMP WHERE id = ?`, session); err != nil {
		return 0, err
	}
	if kind == SessionKindUser {
		var p textPayload
		_ = json.Unmarshal(data, &p)
		if _, err := tx.Exec(`UPDATE session SET title = ? WHERE id = ? AND title = ''`,
			sessionTitle(p.Content), session); err != nil {
			return 0, err
		}
	}
	return id, tx.Commit()
}

func sessionTitle(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > 80 {
		cut := 80
		for cut > 0 && !utf8.RuneStart(s[cut]) {
			cut--
		}
		s = s[:cut]
	}
	return s
}

// SessionEntries returns a session's full log, oldest first.
func SessionEntries(db *sqlx.DB, session string) ([]SessionEntry, error) {
	var entries []SessionEntry
	err := db.Select(&entries, `SELECT * FROM message WHERE session = ? ORDER BY id`, session)
	return entries, err
}

// ListSessions returns rf session metadata, most recently active first.
// Synced foreign sessions (source != 'rf') are excluded: this feeds the
// resume picker, and only rf's own transcripts can be resumed.
func ListSessions(db *sqlx.DB, limit int) ([]Session, error) {
	if limit <= 0 {
		limit = 25
	}
	var sessions []Session
	err := db.Select(&sessions, `
		SELECT s.*, (SELECT COUNT(*) FROM message l WHERE l.session = s.id) AS entries
		FROM session s WHERE s.source = 'rf' ORDER BY s.updated_at DESC, s.id DESC LIMIT ?`, limit)
	return sessions, err
}

const (
	compactionSummaryPrefix = "The conversation history before this point was compacted into the following summary:\n\n<summary>\n"
	compactionSummarySuffix = "\n</summary>"
)

// SessionTranscript rebuilds a session's in-memory transcript from its log.
func SessionTranscript(db *sqlx.DB, session string) ([]*Message, error) {
	entries, err := SessionEntries(db, session)
	if err != nil {
		return nil, err
	}

	var compaction *Compaction
	for i := len(entries) - 1; i >= 0; i-- {
		if entries[i].Kind == SessionKindCompaction {
			var c Compaction
			if err := json.Unmarshal([]byte(entries[i].Payload), &c); err != nil {
				return nil, fmt.Errorf("session: bad compaction payload (entry %d): %w", entries[i].ID, err)
			}
			compaction = &c
			break
		}
	}

	var messages []*Message
	if compaction != nil {
		messages = append(messages, &Message{
			Role:    MessageRoleUser,
			Type:    MessageTypeText,
			Content: compactionSummaryPrefix + compaction.Summary + compactionSummarySuffix,
		})
	}
	for _, e := range entries {
		if compaction != nil && e.ID < compaction.FirstKeptEntryID {
			continue
		}
		m, err := EntryMessage(e)
		if err != nil {
			return nil, err
		}
		if m != nil {
			messages = append(messages, m)
		}
	}
	return messages, nil
}

// EntryMessage converts one log row back to a transcript message.
// Compaction entries return nil: SessionTranscript renders them itself.
func EntryMessage(e SessionEntry) (*Message, error) {
	switch e.Kind {
	case SessionKindUser, SessionKindAssistant:
		var p textPayload
		if err := json.Unmarshal([]byte(e.Payload), &p); err != nil {
			return nil, fmt.Errorf("session: bad %s payload (entry %d): %w", e.Kind, e.ID, err)
		}
		role := MessageRoleUser
		if e.Kind == SessionKindAssistant {
			role = MessageRoleAssistant
		}
		return &Message{Role: role, Type: MessageTypeText, Content: p.Content}, nil
	case SessionKindTool:
		var p toolPayload
		if err := json.Unmarshal([]byte(e.Payload), &p); err != nil {
			return nil, fmt.Errorf("session: bad tool payload (entry %d): %w", e.ID, err)
		}
		return &Message{
			Role:         MessageRoleAssistant,
			Type:         MessageTypeTool,
			Content:      p.Code,
			FunctionName: p.Name,
			Code:         p.Code,
			Result:       p.Result,
			ErrorMessage: p.Error,
			ToolCallID:   p.ToolCallID,
		}, nil
	case SessionKindCompaction:
		return nil, nil
	default:
		return nil, fmt.Errorf("session: unknown entry kind %q (entry %d)", e.Kind, e.ID)
	}
}

// Execer is the slice of sqlx that the synced-transcript writers need, so
// a per-file sync (package claude) can batch its inserts in one
// transaction while callers with a plain handle pass the db directly.
type Execer interface {
	Exec(query string, args ...any) (sql.Result, error)
}

// EnsureClaudeSession creates or refreshes the metadata row for a synced
// Claude Code session.
func EnsureClaudeSession(x Execer, id, cwd, project, title string, first, last time.Time) error {
	_, err := x.Exec(`
		INSERT INTO session (id, cwd, project, title, source, created_at, updated_at)
		VALUES (?, ?, ?, ?, 'claude', ?, ?)
		ON CONFLICT (id) DO UPDATE SET
			cwd = excluded.cwd,
			project = CASE WHEN excluded.project != '' THEN excluded.project ELSE session.project END,
			title = CASE WHEN session.title = '' THEN excluded.title ELSE session.title END,
			updated_at = MAX(session.updated_at, excluded.updated_at)`,
		id, cwd, project, title, first.UTC().Format(tsFormat), last.UTC().Format(tsFormat))
	return err
}

// AppendClaudeMessage mirrors one Claude Code transcript line into the
// message table.
func AppendClaudeMessage(x Execer, session, kind, content, uuid string, ts time.Time) (bool, error) {
	data, err := json.Marshal(textPayload{Content: content})
	if err != nil {
		return false, err
	}
	res, err := x.Exec(`
		INSERT INTO message (session, kind, payload, uuid, created_at) VALUES (?, ?, ?, ?, ?)
		ON CONFLICT (uuid) WHERE uuid IS NOT NULL DO NOTHING`,
		session, kind, string(data), uuid, ts.UTC().Format(tsFormat))
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

// SyncOffset returns how many bytes of a transcript file have been
// consumed; 0 for a file never synced.
func SyncOffset(db *sqlx.DB, path string) (int64, error) {
	var offset int64
	err := db.Get(&offset, `SELECT offset FROM message_sync WHERE path = ?`, path)
	if err == sql.ErrNoRows {
		return 0, nil
	}
	return offset, err
}

// SetSyncOffset checkpoints a transcript file's consumed byte count.
func SetSyncOffset(x Execer, path string, offset int64) error {
	_, err := x.Exec(`
		INSERT INTO message_sync (path, offset) VALUES (?, ?)
		ON CONFLICT (path) DO UPDATE SET offset = excluded.offset, updated_at = CURRENT_TIMESTAMP`,
		path, offset)
	return err
}

// MessageQuery filters SearchMessages. Zero values mean "no filter";
// Dir matches the session's directory and everything beneath it, Session
// matches an id prefix.
type MessageQuery struct {
	Pattern string // substring of the message text
	Dir     string // absolute path; matches the session cwd and its subtree
	Project string // project name
	Role    string // user | assistant
	Source  string // rf | claude
	Session string // session id (or unique prefix)
	Limit   int    // defaults to 50
	All     bool   // no row limit
}

// MessageRow is one conversational message joined with its session: the
// row shape behind the `messages` builtin. Only user and assistant text
// qualifies — tool and compaction entries stay transcript-internal.
type MessageRow struct {
	ID        int64     `db:"id"`
	Session   string    `db:"session"`
	Kind      string    `db:"kind"`
	Source    string    `db:"source"`
	Cwd       string    `db:"cwd"`
	Project   string    `db:"project"`
	Text      string    `db:"text"`
	CreatedAt time.Time `db:"created_at"`
}

// SearchMessages returns the most recent matching messages, oldest first
// (like history: the latest reads last). Ordered by timestamp, not row id —
// synced backfill inserts old messages with new ids.
func SearchMessages(db *sqlx.DB, q MessageQuery) ([]MessageRow, error) {
	where := []string{`m.kind IN ('user', 'assistant')`}
	args := []any{}
	if q.Pattern != "" {
		where = append(where, `json_extract(m.payload, '$.content') LIKE ? ESCAPE '\'`)
		args = append(args, "%"+escapeLike(q.Pattern)+"%")
	}
	if q.Dir != "" {
		dir := strings.TrimRight(q.Dir, "/")
		where = append(where, `(s.cwd = ? OR s.cwd LIKE ? ESCAPE '\')`)
		args = append(args, dir, escapeLike(dir)+"/%")
	}
	if q.Project != "" {
		where = append(where, `s.project = ?`)
		args = append(args, q.Project)
	}
	if q.Role != "" {
		where = append(where, `m.kind = ?`)
		args = append(args, q.Role)
	}
	if q.Source != "" {
		where = append(where, `s.source = ?`)
		args = append(args, q.Source)
	}
	if q.Session != "" {
		where = append(where, `m.session LIKE ? ESCAPE '\'`)
		args = append(args, escapeLike(q.Session)+"%")
	}
	limit := ""
	if !q.All {
		n := q.Limit
		if n <= 0 {
			n = 50
		}
		limit = " LIMIT ?"
		args = append(args, n)
	}

	var rows []MessageRow
	err := db.Select(&rows, `
		SELECT m.id, m.session, m.kind, s.source, s.cwd, s.project,
		       COALESCE(json_extract(m.payload, '$.content'), '') AS text, m.created_at
		FROM message m JOIN session s ON s.id = m.session
		WHERE `+strings.Join(where, " AND ")+`
		ORDER BY m.created_at DESC, m.id DESC`+limit,
		args...)
	if err != nil {
		return nil, err
	}
	for i, j := 0, len(rows)-1; i < j; i, j = i+1, j-1 {
		rows[i], rows[j] = rows[j], rows[i]
	}
	return rows, nil
}
