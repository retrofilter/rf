package console

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/retrofilter/rf/core"
	"github.com/retrofilter/rf/models"
	"github.com/retrofilter/rf/models/schema"
	"github.com/stretchr/testify/require"
)

func storeServer(t *testing.T) (*Server, *httptest.Server, *core.Graph) {
	t.Helper()
	db, err := schema.OpenDB(filepath.Join(t.TempDir(), "test.db"))
	require.NoError(t, err)
	t.Cleanup(func() { db.Close() })
	require.NoError(t, schema.CreateTables(db))
	gs := core.NewGraphStore(db)
	_, err = gs.CreateGraph(overviewGraphName)
	require.NoError(t, err)
	cg, err := gs.GetGraph(overviewGraphName)
	require.NoError(t, err)

	s := NewServer(Config{
		Token:        "sekrit-sekrit-sekrit-sekrit-sekrit",
		SpawnCommand: "cat",
		Store:        gs,
	})
	s.mgr.startGrace = 100 * time.Millisecond
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)
	return s, ts, cg
}

func seedNode(t *testing.T, cg *core.Graph, nodeType string, props map[string]interface{}) uint32 {
	t.Helper()
	props["type"] = nodeType
	raw, err := json.Marshal(props)
	require.NoError(t, err)
	id, err := cg.InsertNode(context.Background(), &models.Node{
		GraphID:    cg.ID,
		Type:       &nodeType,
		Properties: (*json.RawMessage)(&raw),
	})
	require.NoError(t, err)
	return id
}

func seedEdge(t *testing.T, cg *core.Graph, edgeType string, source, target uint32) {
	t.Helper()
	raw, err := json.Marshal(map[string]interface{}{"type": edgeType})
	require.NoError(t, err)
	_, err = cg.InsertEdge(context.Background(), &models.Edge{
		GraphID:    cg.ID,
		Source:     source,
		Target:     target,
		Type:       &edgeType,
		Properties: (*json.RawMessage)(&raw),
	})
	require.NoError(t, err)
}

func seedProject(t *testing.T, cg *core.Graph, name, path string) uint32 {
	return seedNode(t, cg, "project", map[string]interface{}{"name": name, "path": path, "kind": "dir"})
}

func seedTask(t *testing.T, cg *core.Graph, text, status string, project uint32) uint32 {
	id := seedNode(t, cg, "task", map[string]interface{}{"text": text, "status": status})
	if project != 0 {
		seedEdge(t, cg, "for", id, project)
	}
	return id
}

func startForm(t *testing.T, s *Server, ts *httptest.Server, kind string, id uint32) (*http.Response, string) {
	t.Helper()
	form := url.Values{"kind": {kind}, "id": {strconv.Itoa(int(id))}}
	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/ui/start", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Cookie", cookieName+"="+s.auth.mintSession())
	res, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer res.Body.Close()
	body, err := io.ReadAll(res.Body)
	require.NoError(t, err)
	return res, string(body)
}

func TestOverviewListsProjectsAndOpenTasks(t *testing.T) {
	s, ts, cg := storeServer(t)
	dir := t.TempDir()
	pid := seedProject(t, cg, "retro", dir)
	tid := seedTask(t, cg, "rename a session functionality", "open", pid)
	did := seedTask(t, cg, "already shipped", "done", pid)
	seedTask(t, cg, "a global errand", "open", 0)

	res, body := get(t, s, ts, "/ui/overview")
	require.Equal(t, http.StatusOK, res.StatusCode)
	require.Contains(t, body, `data-ov-kind="project" data-ov-id="`+strconv.Itoa(int(pid))+`"`)
	require.Contains(t, body, "retro")
	require.Contains(t, body, `data-ov-kind="task" data-ov-id="`+strconv.Itoa(int(tid))+`"`)
	require.Contains(t, body, "rename a session functionality")
	require.Contains(t, body, "already shipped")
	require.NotContains(t, body, `data-ov-kind="task" data-ov-id="`+strconv.Itoa(int(did))+`"`)
	require.Contains(t, body, "unfiled")
	require.Contains(t, body, "a global errand")
}

func TestOverviewRendersProjectNotesAndPathlessProjects(t *testing.T) {
	s, ts, cg := storeServer(t)
	pid := seedNode(t, cg, "project", map[string]interface{}{
		"name": "misc",
		"text": "# Misc\n\nodds and *ends* <script>alert(1)</script>",
	})
	tid := seedTask(t, cg, "sort the garage", "open", pid)

	res, body := get(t, s, ts, "/ui/overview")
	require.Equal(t, http.StatusOK, res.StatusCode)
	// A pathless project is listed, startable, and shows no path label.
	require.Contains(t, body, `data-ov-kind="project" data-ov-id="`+strconv.Itoa(int(pid))+`"`)
	require.NotContains(t, body, `ov-ppath`)
	// Notes render as markdown between the header and the tasks, raw HTML dropped.
	require.Contains(t, body, `<div class="ov-notes">`)
	require.Contains(t, body, `<h1>Misc</h1>`)
	require.Contains(t, body, `odds and <em>ends</em>`)
	require.NotContains(t, body, `<script>`)
	require.Less(t, strings.Index(body, `ov-notes`), strings.Index(body, `data-ov-id="`+strconv.Itoa(int(tid))+`"`))

	// Starting it spawns in $HOME — no directory to start in.
	res, _ = startForm(t, s, ts, "project", pid)
	require.Equal(t, http.StatusOK, res.StatusCode)
	require.Len(t, s.mgr.List(), 1)
	require.Equal(t, "misc", s.mgr.List()[0].Name)
}

func TestOverviewCapsDoneTasksPerProject(t *testing.T) {
	s, ts, cg := storeServer(t)
	pid := seedProject(t, cg, "retro", t.TempDir())
	for i := 1; i <= 5; i++ {
		seedTask(t, cg, fmt.Sprintf("finished number %d", i), "done", pid)
	}

	// Only the newest two completed tasks show; the older ones stay out.
	_, body := get(t, s, ts, "/ui/overview")
	require.Contains(t, body, "finished number 5")
	require.Contains(t, body, "finished number 4")
	require.NotContains(t, body, "finished number 3")
	require.NotContains(t, body, "finished number 2")
	require.NotContains(t, body, "finished number 1")
}

func TestOverviewCapsTasksBehindMoreRow(t *testing.T) {
	s, ts, cg := storeServer(t)
	pid := seedProject(t, cg, "retro", t.TempDir())
	ids := make([]uint32, 0, overviewTaskCap+2)
	for i := 1; i <= overviewTaskCap+2; i++ {
		ids = append(ids, seedTask(t, cg, fmt.Sprintf("open number %d", i), "open", pid))
	}

	_, body := get(t, s, ts, "/ui/overview")
	for i, id := range ids {
		row := `data-ov-kind="task" data-ov-id="` + strconv.Itoa(int(id)) + `"`
		require.Contains(t, body, row)
		hidden := strings.Contains(body, row+" hidden")
		require.Equal(t, i >= overviewTaskCap, hidden, "task %d hidden=%v", i+1, hidden)
	}
	require.Contains(t, body, "data-ov-more")
	require.Contains(t, body, `<span class="ov-text">...</span>`)

	s2, ts2, cg2 := storeServer(t)
	pid2 := seedProject(t, cg2, "retro", t.TempDir())
	for i := 1; i <= 2; i++ {
		seedTask(t, cg2, fmt.Sprintf("open number %d", i), "open", pid2)
	}
	seedTask(t, cg2, "older finished", "done", pid2)
	seedTask(t, cg2, "newer finished", "done", pid2)
	_, body = get(t, s2, ts2, "/ui/overview")
	require.Contains(t, body, "data-ov-more")
	require.Contains(t, body, `<div class="ov-task ov-done"><span class="ov-text">newer finished</span></div>`)
	require.Contains(t, body, `<div class="ov-task ov-done" hidden><span class="ov-text">older finished</span></div>`)

	// At the cap exactly, no overflow row and nothing hidden.
	s3, ts3, cg3 := storeServer(t)
	pid3 := seedProject(t, cg3, "retro", t.TempDir())
	for i := 1; i <= overviewTaskCap-1; i++ {
		seedTask(t, cg3, fmt.Sprintf("open number %d", i), "open", pid3)
	}
	seedTask(t, cg3, "finished", "done", pid3)
	_, body = get(t, s3, ts3, "/ui/overview")
	require.NotContains(t, body, "data-ov-more")
	require.NotContains(t, body, " hidden")
}

func TestOverviewEmptyWithoutProjects(t *testing.T) {
	s, ts, _ := storeServer(t)
	res, body := get(t, s, ts, "/ui/overview")
	require.Equal(t, http.StatusOK, res.StatusCode)
	require.Contains(t, body, "no projects registered")
	require.NotContains(t, body, "ov-row")
}

func TestStartTaskSpawnsSessionInProjectDir(t *testing.T) {
	s, ts, cg := storeServer(t)
	dir := t.TempDir()
	pid := seedProject(t, cg, "retro", dir)
	tid := seedTask(t, cg, "rename a session functionality", "open", pid)

	res, _ := startForm(t, s, ts, "task", tid)
	require.Equal(t, http.StatusOK, res.StatusCode)
	sel, ok := triggers(t, res)["rf:select"].(map[string]any)
	require.True(t, ok, "start answers with rf:select")
	id := sel["id"].(string)

	rows, err := s.reg.Sessions()
	require.NoError(t, err)
	require.Len(t, rows, 1)
	require.Equal(t, id, rows[0].ID)
	require.Equal(t, "rename-a-session-functionality", rows[0].Name)
	require.Equal(t, dir, rows[0].Dir)

	ls := s.mgr.Get(id)
	require.NotNil(t, ls)
	require.Eventually(t, func() bool {
		replay, _, cancel := ls.Subscribe()
		cancel()
		return strings.Contains(string(replay), "claude 'rename a session functionality'")
	}, 3*time.Second, 50*time.Millisecond)

	res, _ = startForm(t, s, ts, "task", tid)
	require.Equal(t, http.StatusOK, res.StatusCode)
	sel, _ = triggers(t, res)["rf:select"].(map[string]any)
	require.Equal(t, id, sel["id"])
	rows, err = s.reg.Sessions()
	require.NoError(t, err)
	require.Len(t, rows, 1)
}

func TestStartProjectSpawnsPlainSessionInDir(t *testing.T) {
	s, ts, cg := storeServer(t)
	dir := t.TempDir()
	pid := seedProject(t, cg, "My Project", dir)

	res, _ := startForm(t, s, ts, "project", pid)
	require.Equal(t, http.StatusOK, res.StatusCode)
	rows, err := s.reg.Sessions()
	require.NoError(t, err)
	require.Len(t, rows, 1)
	require.Equal(t, "my-project", rows[0].Name)
	require.Equal(t, dir, rows[0].Dir)

	// Wrong kinds and unknown ids answer 4xx, not a spawn.
	res, _ = startForm(t, s, ts, "task", pid)
	require.Equal(t, http.StatusBadRequest, res.StatusCode)
	res, _ = startForm(t, s, ts, "project", 99999)
	require.Equal(t, http.StatusBadRequest, res.StatusCode)
}

func TestSessionSlug(t *testing.T) {
	require.Equal(t, "rename-a-session-functionality", sessionSlug("rename a session functionality", 7))
	require.Equal(t, "fix-the-pty-resize-test", sessionSlug("Fix the (PTY) resize test!", 7))
	require.Equal(t, "task-7", sessionSlug("→ ??? ←", 7))
	require.Equal(t, "", SessionSlug("→ ??? ←"))
	require.Equal(t, "what-time-is-it", SessionSlug(`(agent "what time is it")`))
	require.Equal(t, "review", SessionSlug("claude /review"))
	require.Equal(t, "fix-the-tests", SessionSlug("claude agent 'fix the tests'"))
	require.Equal(t, "claude", SessionSlug("claude"))
	require.Equal(t, "claude-code-docs", SessionSlug("claude-code docs"))
	long := sessionSlug(strings.Repeat("word ", 20), 7)
	require.LessOrEqual(t, len(long), 40)
	require.NotEqual(t, "-", long[len(long)-1:])
}
