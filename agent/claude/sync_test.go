package claude

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/retrofilter/rf/agent"
	"github.com/retrofilter/rf/models"
	"github.com/stretchr/testify/require"
)

const fixture = `{"type":"user","uuid":"u1","sessionId":"s-abcdef12","timestamp":"2026-08-01T10:00:00.000Z","cwd":"/home/u/src/foo","message":{"role":"user","content":"fix the retry backoff bug"}}
{"type":"assistant","uuid":"a1","sessionId":"s-abcdef12","timestamp":"2026-08-01T10:00:30.000Z","cwd":"/home/u/src/foo","message":{"role":"assistant","content":[{"type":"thinking","thinking":"hmm"},{"type":"text","text":"The bug is in retry.go"},{"type":"tool_use","id":"t1","name":"Bash","input":{}}]}}
{"type":"user","uuid":"u2","sessionId":"s-abcdef12","timestamp":"2026-08-01T10:00:40.000Z","cwd":"/home/u/src/foo","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":"ok"}]}}
{"type":"user","uuid":"u3","sessionId":"s-abcdef12","timestamp":"2026-08-01T10:01:00.000Z","cwd":"/home/u/src/foo","message":{"role":"user","content":"<command-name>/clear</command-name>"}}
{"type":"user","uuid":"u4","sessionId":"s-abcdef12","timestamp":"2026-08-01T10:01:10.000Z","cwd":"/home/u/src/foo","isMeta":true,"message":{"role":"user","content":"Caveat: The messages below were generated"}}
{"type":"user","uuid":"u5","sessionId":"s-abcdef12","timestamp":"2026-08-01T10:01:20.000Z","cwd":"/home/u/src/foo","isSidechain":true,"message":{"role":"user","content":"sidechain prompt"}}
{"type":"summary","summary":"a summary line","leafUuid":"x"}
{"type":"user","uuid":"u6","sessionId":"s-abcdef12","timestamp":"2026-08-01T10:02:00.000Z","cwd":"/home/u/src/foo","message":{"role":"user","content":[{"type":"text","text":"<system-reminder>noise</system-reminder>"},{"type":"text","text":"what about jitter"}]}}
`

func TestSyncFile(t *testing.T) {
	db, err := models.CreateTempDB()
	require.NoError(t, err)
	defer db.Close()

	dir := t.TempDir()
	path := filepath.Join(dir, "s-abcdef12.jsonl")
	require.NoError(t, os.WriteFile(path, []byte(fixture), 0644))

	res, err := Source{}.SyncFile(db, path, agent.Options{Project: func(cwd string) string { return "foo" }})
	require.NoError(t, err)
	require.Equal(t, 1, res.Files)
	require.Equal(t, 1, res.Sessions)
	require.Equal(t, 3, res.Messages) // u1, a1 (text block only), u6

	rows, err := models.SearchMessages(db, models.MessageQuery{All: true})
	require.NoError(t, err)
	require.Len(t, rows, 3)
	require.Equal(t, "fix the retry backoff bug", rows[0].Text)
	require.Equal(t, "user", rows[0].Kind)
	require.Equal(t, "The bug is in retry.go", rows[1].Text)
	require.Equal(t, "assistant", rows[1].Kind)
	require.Equal(t, "what about jitter", rows[2].Text) // reminder block dropped
	for _, r := range rows {
		require.Equal(t, "s-abcdef12", r.Session)
		require.Equal(t, models.SessionSourceClaude, r.Source)
		require.Equal(t, "/home/u/src/foo", r.Cwd)
		require.Equal(t, "foo", r.Project)
	}

	var title, source string
	require.NoError(t, db.Get(&title, `SELECT title FROM session WHERE id = 's-abcdef12'`))
	require.NoError(t, db.Get(&source, `SELECT source FROM session WHERE id = 's-abcdef12'`))
	require.Equal(t, "fix the retry backoff bug", title)
	require.Equal(t, "claude", source)
	sessions, err := models.ListSessions(db, 0)
	require.NoError(t, err)
	require.Empty(t, sessions)

	// Re-sync with no new bytes is a no-op.
	res, err = Source{}.SyncFile(db, path, agent.Options{})
	require.NoError(t, err)
	require.Equal(t, agent.Result{}, res)

	// A checkpoint reset replays the file; uuids absorb the duplicates.
	require.NoError(t, models.SetSyncOffset(db, path, 0))
	res, err = Source{}.SyncFile(db, path, agent.Options{})
	require.NoError(t, err)
	require.Equal(t, 0, res.Messages)

	appendFile(t, path, `{"type":"assistant","uuid":"a2","sessionId":"s-abcdef12","timestamp":"2026-08-01T10:03:00.000Z","cwd":"/home/u/src/foo","message":{"role":"assistant","content":[{"type":"text","text":"add jitter to the backoff"}]}}`+"\n")
	appendFile(t, path, `{"type":"user","uuid":"u7","sessionId":"s-abcdef12",`) // partial
	res, err = Source{}.SyncFile(db, path, agent.Options{})
	require.NoError(t, err)
	require.Equal(t, 1, res.Messages)
	appendFile(t, path, `"timestamp":"2026-08-01T10:04:00.000Z","message":{"role":"user","content":"ship it"}}`+"\n")
	res, err = Source{}.SyncFile(db, path, agent.Options{})
	require.NoError(t, err)
	require.Equal(t, 1, res.Messages)

	rows, err = models.SearchMessages(db, models.MessageQuery{All: true})
	require.NoError(t, err)
	require.Len(t, rows, 5)
	require.Equal(t, "ship it", rows[4].Text)
}

func TestSyncTree(t *testing.T) {
	db, err := models.CreateTempDB()
	require.NoError(t, err)
	defer db.Close()

	root := t.TempDir()
	for _, p := range []string{"-home-u-src-foo/s-1.jsonl", "-home-u-src-bar/s-2.jsonl"} {
		path := filepath.Join(root, p)
		require.NoError(t, os.MkdirAll(filepath.Dir(path), 0755))
		session := filepath.Base(p[:len(p)-len(".jsonl")])
		line := `{"type":"user","uuid":"` + session + `-u1","sessionId":"` + session + `","timestamp":"2026-08-02T09:00:00.000Z","cwd":"/x","message":{"role":"user","content":"hello from ` + session + `"}}` + "\n"
		require.NoError(t, os.WriteFile(path, []byte(line), 0644))
	}

	res, err := Source{Root: root}.Sync(db, agent.Options{})
	require.NoError(t, err)
	require.Equal(t, agent.Result{Files: 2, Sessions: 2, Messages: 2}, res)

	// A missing root is not an error — machines without Claude Code.
	res, err = Source{Root: filepath.Join(root, "nope")}.Sync(db, agent.Options{})
	require.NoError(t, err)
	require.Equal(t, agent.Result{}, res)
}

func appendFile(t *testing.T, path, s string) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0644)
	require.NoError(t, err)
	defer f.Close()
	_, err = f.WriteString(s)
	require.NoError(t, err)
}
