package console

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func get(t *testing.T, s *Server, ts *httptest.Server, path string) (*http.Response, string) {
	t.Helper()
	req, _ := http.NewRequest(http.MethodGet, ts.URL+path, nil)
	req.Header.Set("Cookie", cookieName+"="+s.auth.mintSession())
	res, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer res.Body.Close()
	body, err := io.ReadAll(res.Body)
	require.NoError(t, err)
	return res, string(body)
}

func triggers(t *testing.T, res *http.Response) map[string]any {
	t.Helper()
	hdr := res.Header.Get("HX-Trigger")
	if hdr == "" {
		return nil
	}
	var ev map[string]any
	require.NoError(t, json.Unmarshal([]byte(hdr), &ev))
	return ev
}

func TestPollEmptyFleetOpensOverview(t *testing.T) {
	s, ts := testServer(t)
	res, body := get(t, s, ts, "/ui/poll")
	require.Equal(t, http.StatusOK, res.StatusCode)
	ev := triggers(t, res)
	require.Contains(t, ev, "rf:showoverview")
	require.NotContains(t, ev, "rf:select")
	require.NotContains(t, body, "data-session-id")
	// The oob chrome still rides along so counts read zero.
	require.Contains(t, body, `id="session-count"`)
	require.Contains(t, body, ">0 Running<")

	res, _ = get(t, s, ts, "/ui/poll?view=new")
	require.NotContains(t, triggers(t, res), "rf:showoverview")
	res, _ = get(t, s, ts, "/ui/poll?view=overview")
	require.Nil(t, triggers(t, res))
}

func TestPollSelectsFirstAndRendersSelection(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	s, ts := testServer(t)
	id, err := s.mgr.Spawn("alpha", "", "")
	require.NoError(t, err)

	// No active session yet: the server says to attach to the first.
	res, body := get(t, s, ts, "/ui/poll")
	ev := triggers(t, res)
	require.Equal(t, map[string]any{"id": id}, ev["rf:select"])
	require.Contains(t, body, `data-session-id="`+id+`"`)
	require.NotContains(t, body, "selected")

	// Echoing the selection back renders it and quiets the triggers.
	res, body = get(t, s, ts, "/ui/poll?active="+url.QueryEscape(id))
	require.Nil(t, triggers(t, res))
	require.Contains(t, body, "session-row selected")
	require.Contains(t, body, "tab active")

	// A vanished active session in the naming view just detaches.
	res, _ = get(t, s, ts, "/ui/poll?active=gone&view=new")
	ev = triggers(t, res)
	require.Contains(t, ev, "rf:detached")
	require.NotContains(t, ev, "rf:select")
}

func spawnForm(t *testing.T, s *Server, ts *httptest.Server, name string) (*http.Response, string) {
	t.Helper()
	form := url.Values{"name": {name}}
	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/ui/sessions", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Cookie", cookieName+"="+s.auth.mintSession())
	res, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer res.Body.Close()
	body, err := io.ReadAll(res.Body)
	require.NoError(t, err)
	return res, string(body)
}

func TestSpawnFormFlow(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	s, ts := testServer(t)

	// The name is required and kept tame; errors render for #nv-error.
	res, body := spawnForm(t, s, ts, "")
	require.Equal(t, http.StatusBadRequest, res.StatusCode)
	require.Contains(t, body, "must match")
	res, _ = spawnForm(t, s, ts, "bad name!")
	require.Equal(t, http.StatusBadRequest, res.StatusCode)

	// Success answers with the attach trigger and a clean error slot.
	res, body = spawnForm(t, s, ts, "probe")
	require.Equal(t, http.StatusOK, res.StatusCode)
	require.Empty(t, body)
	sel, ok := triggers(t, res)["rf:select"].(map[string]any)
	require.True(t, ok, "spawn answers with rf:select")
	id := sel["id"].(string)
	require.NotEmpty(t, id)

	// The spawn invalidated the cache so discovery sees it immediately.
	rows, err := s.reg.Sessions()
	require.NoError(t, err)
	require.Len(t, rows, 1)
	require.Equal(t, id, rows[0].ID)
	require.Equal(t, "probe", rows[0].Name)

	res, _ = spawnForm(t, s, ts, "probe")
	require.Equal(t, http.StatusOK, res.StatusCode)
	sel, _ = triggers(t, res)["rf:select"].(map[string]any)
	require.Equal(t, id, sel["id"])
	rows, err = s.reg.Sessions()
	require.NoError(t, err)
	require.Len(t, rows, 1)
}

func TestPollEchoesGraphTab(t *testing.T) {
	s, ts := testServer(t)

	// No graph flag, no graph tab.
	_, body := get(t, s, ts, "/ui/poll?view=overview")
	require.NotContains(t, body, "data-graph")

	_, body = get(t, s, ts, "/ui/poll?view=overview&graph=1")
	require.Contains(t, body, "data-graph")
	require.NotContains(t, body, "tab-graph active")
	res, body := get(t, s, ts, "/ui/poll?view=graph&graph=1")
	require.Contains(t, body, "tab-graph active")
	require.NotContains(t, triggers(t, res), "rf:showoverview")
}

func closeForm(t *testing.T, s *Server, ts *httptest.Server, id string) *http.Response {
	t.Helper()
	form := url.Values{"id": {id}}
	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/ui/close", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Cookie", cookieName+"="+s.auth.mintSession())
	res, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	res.Body.Close()
	return res
}

func TestCloseEndsSession(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	s, ts := testServer(t)
	id, err := s.mgr.Spawn("doomed", "", "")
	require.NoError(t, err)

	res := closeForm(t, s, ts, id)
	require.Equal(t, http.StatusNoContent, res.StatusCode)
	// The hangup funnels through the reader's EOF cleanup asynchronously.
	require.Eventually(t, func() bool { return s.mgr.Get(id) == nil }, 3*time.Second, 20*time.Millisecond)

	require.Equal(t, http.StatusBadRequest, closeForm(t, s, ts, id).StatusCode)
}

func TestIndexServesConsolePage(t *testing.T) {
	s, ts := testServer(t)
	res, body := get(t, s, ts, "/")
	require.Equal(t, http.StatusOK, res.StatusCode)
	require.Contains(t, res.Header.Get("Content-Type"), "text/html")
	// The tab title matches the header's user@host block.
	require.Contains(t, body, "<title>"+whoami()+"</title>")
	require.Contains(t, body, `hx-get="/ui/poll"`)
}
