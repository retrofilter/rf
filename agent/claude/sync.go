// Package claude imports Claude Code's ~/.claude/projects/**/*.jsonl
// transcripts into rf's session/message tables (source 'claude'): incremental
// by byte offset, idempotent by line uuid, conversational text only.
package claude

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/jmoiron/sqlx"
	"github.com/retrofilter/rf/agent"
	"github.com/retrofilter/rf/models"
)

func init() {
	agent.Register(Source{})
}

// Source is the Claude Code harness. The zero value reads the standard
// transcript tree; Root overrides it (tests).
type Source struct {
	Root string
}

// Name tags the sessions this source writes.
func (Source) Name() string { return models.SessionSourceClaude }

// Sync sweeps every transcript under the root. A file that fails to sync
// doesn't stop the sweep; the errors come back joined.
func (s Source) Sync(db *sqlx.DB, opts agent.Options) (agent.Result, error) {
	root := s.Root
	if root == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return agent.Result{}, err
		}
		root = filepath.Join(home, ".claude", "projects")
	}
	var paths []string
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil // unreadable subtree: skip, not fatal
		}
		if !d.IsDir() && strings.HasSuffix(path, ".jsonl") {
			paths = append(paths, path)
		}
		return nil
	})
	if err != nil {
		if os.IsNotExist(err) {
			return agent.Result{}, nil // no Claude Code installation: nothing to do
		}
		return agent.Result{}, err
	}
	sort.Strings(paths)

	var total agent.Result
	var errs []error
	for _, path := range paths {
		res, err := s.SyncFile(db, path, opts)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", path, err))
			continue
		}
		total.Add(res)
	}
	return total, errors.Join(errs...)
}

// SyncFile ingests one transcript file from its checkpoint onward — the Stop-
// hook fast path (`rf hook --fire`).
func (s Source) SyncFile(db *sqlx.DB, path string, opts agent.Options) (agent.Result, error) {
	var res agent.Result
	info, err := os.Stat(path)
	if err != nil {
		return res, err
	}
	offset, err := models.SyncOffset(db, path)
	if err != nil {
		return res, err
	}
	if offset > info.Size() {
		offset = 0 // file replaced or truncated; uuids absorb the replay
	}
	if offset == info.Size() {
		return res, nil
	}

	f, err := os.Open(path)
	if err != nil {
		return res, err
	}
	defer f.Close()
	if _, err := f.Seek(offset, io.SeekStart); err != nil {
		return res, err
	}
	data, err := io.ReadAll(f)
	if err != nil {
		return res, err
	}
	end := bytes.LastIndexByte(data, '\n')
	if end < 0 {
		return res, nil // no complete line yet
	}
	data = data[:end+1]

	// The file's basename is the session id when a line doesn't say.
	fileSession := strings.TrimSuffix(filepath.Base(path), ".jsonl")

	tx, err := db.Beginx()
	if err != nil {
		return res, err
	}
	defer tx.Rollback()

	sessions := map[string]*sessionAcc{}
	var order []string
	var msgs []message
	for raw := range bytes.SplitSeq(data[:end], []byte("\n")) {
		msg, ok := parseLine(raw)
		if !ok {
			continue
		}
		if msg.session == "" {
			msg.session = fileSession
		}
		acc := sessions[msg.session]
		if acc == nil {
			acc = &sessionAcc{first: msg.ts, last: msg.ts}
			sessions[msg.session] = acc
			order = append(order, msg.session)
		}
		if msg.ts.Before(acc.first) {
			acc.first = msg.ts
		}
		if msg.ts.After(acc.last) {
			acc.last = msg.ts
		}
		if msg.cwd != "" {
			acc.cwd = msg.cwd
		}
		if acc.title == "" && msg.kind == models.SessionKindUser {
			acc.title = sessionTitle(msg.text)
		}
		msgs = append(msgs, msg)
	}
	for _, id := range order {
		acc := sessions[id]
		project := ""
		if opts.Project != nil && acc.cwd != "" {
			project = opts.Project(acc.cwd)
		}
		if err := models.EnsureClaudeSession(tx, id, acc.cwd, project, acc.title, acc.first, acc.last); err != nil {
			return res, err
		}
		res.Sessions++
	}
	for _, msg := range msgs {
		added, err := models.AppendClaudeMessage(tx, msg.session, msg.kind, msg.text, msg.uuid, msg.ts)
		if err != nil {
			return res, err
		}
		if added {
			res.Messages++
		}
	}
	if err := models.SetSyncOffset(tx, path, offset+int64(len(data))); err != nil {
		return res, err
	}
	if err := tx.Commit(); err != nil {
		return res, err
	}
	res.Files = 1
	return res, nil
}

type sessionAcc struct {
	cwd, title  string
	first, last time.Time
}

type message struct {
	session, kind, text, uuid, cwd string
	ts                             time.Time
}

type transcriptLine struct {
	Type        string    `json:"type"`
	UUID        string    `json:"uuid"`
	SessionID   string    `json:"sessionId"`
	Timestamp   time.Time `json:"timestamp"`
	Cwd         string    `json:"cwd"`
	IsSidechain bool      `json:"isSidechain"`
	IsMeta      bool      `json:"isMeta"`
	Message     struct {
		Content json.RawMessage `json:"content"`
	} `json:"message"`
}

func parseLine(raw []byte) (message, bool) {
	var l transcriptLine
	if len(bytes.TrimSpace(raw)) == 0 || json.Unmarshal(raw, &l) != nil {
		return message{}, false
	}
	if l.Type != "user" && l.Type != "assistant" {
		return message{}, false
	}
	if l.IsSidechain || l.IsMeta || l.UUID == "" || l.Timestamp.IsZero() {
		return message{}, false
	}
	text := extractText(l.Message.Content)
	if text == "" {
		return message{}, false
	}
	return message{
		session: l.SessionID,
		kind:    l.Type, // user | assistant, same vocabulary as SessionKind*
		text:    text,
		uuid:    l.UUID,
		cwd:     l.Cwd,
		ts:      l.Timestamp,
	}, true
}

func extractText(raw json.RawMessage) string {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		if boilerplate(s) {
			return ""
		}
		return strings.TrimSpace(s)
	}
	var blocks []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if json.Unmarshal(raw, &blocks) != nil {
		return ""
	}
	var parts []string
	for _, b := range blocks {
		t := strings.TrimSpace(b.Text)
		if b.Type != "text" || t == "" || boilerplate(t) {
			continue
		}
		parts = append(parts, t)
	}
	return strings.Join(parts, "\n\n")
}

func boilerplate(s string) bool {
	t := strings.TrimSpace(s)
	return strings.HasPrefix(t, "<command-") ||
		strings.HasPrefix(t, "<local-command") ||
		strings.HasPrefix(t, "<system-reminder>") ||
		strings.HasPrefix(t, "Caveat: The messages below")
}

func sessionTitle(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > 80 {
		s = s[:80]
	}
	return s
}
