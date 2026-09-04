package console

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/stretchr/testify/require"
)

func TestAttachRoundTrip(t *testing.T) {
	s := NewServer(Config{Token: "sekrit-sekrit-sekrit-sekrit-sekrit", SpawnCommand: "cat"})
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)

	id, err := s.mgr.Spawn("e2e", "", "")
	require.NoError(t, err)

	// Discovery sees the session, keyed by its id.
	sessions, err := s.reg.Sessions()
	require.NoError(t, err)
	require.Len(t, sessions, 1)
	require.Equal(t, id, sessions[0].ID)
	require.Equal(t, "e2e", sessions[0].Name)

	// Attach needs the auth cookie, and a missing session 404s.
	header := http.Header{"Cookie": {cookieName + "=" + s.auth.mintSession()}}
	wsURL := strings.Replace(ts.URL, "http", "ws", 1)
	attachPath := "/api/attach/" + id + "?cols=100&rows=30"
	_, res, err := websocket.DefaultDialer.Dial(wsURL+"/api/attach/nope?cols=80&rows=24", header)
	require.Error(t, err)
	require.Equal(t, http.StatusNotFound, res.StatusCode)
	_, res, err = websocket.DefaultDialer.Dial(wsURL+attachPath, nil)
	require.Error(t, err)
	require.Equal(t, http.StatusUnauthorized, res.StatusCode)

	conn, _, err := websocket.DefaultDialer.Dial(wsURL+attachPath, header)
	require.NoError(t, err)
	defer conn.Close()

	// Type into the PTY; cat echoes it back.
	require.NoError(t, conn.WriteMessage(websocket.BinaryMessage, []byte("marco\r")))
	require.NoError(t, conn.WriteMessage(websocket.TextMessage, []byte(`{"resize":{"cols":90,"rows":25}}`)))

	readUntil := func(c *websocket.Conn, want string) string {
		deadline := time.Now().Add(10 * time.Second)
		var seen strings.Builder
		for time.Now().Before(deadline) {
			_ = c.SetReadDeadline(time.Now().Add(2 * time.Second))
			_, data, err := c.ReadMessage()
			if err != nil {
				break
			}
			seen.Write(data)
			if strings.Contains(seen.String(), want) {
				break
			}
		}
		return seen.String()
	}
	require.Contains(t, readUntil(conn, "marco"), "marco")

	// A second viewer gets the ring replayed — a refreshed tab repaints.
	conn2, _, err := websocket.DefaultDialer.Dial(wsURL+attachPath, header)
	require.NoError(t, err)
	defer conn2.Close()
	require.Contains(t, readUntil(conn2, "marco"), "marco")
}

func TestBootProbeAnsweredByFirstAttach(t *testing.T) {
	shell := `printf 'boot> \033[6n'; head -c 6 >/dev/null; printf 'ANSWERED'; sleep 60`
	s := NewServer(Config{Token: "sekrit-sekrit-sekrit-sekrit-sekrit", SpawnCommand: shell})
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)

	id, err := s.mgr.Spawn("probing", "", "")
	require.NoError(t, err)
	ls := s.mgr.Get(id)
	require.NotNil(t, ls)

	// The probe is held for the next attach, not answered by the server.
	require.Eventually(t, func() bool {
		ls.mu.Lock()
		defer ls.mu.Unlock()
		return len(ls.pending) > 0
	}, 5*time.Second, 20*time.Millisecond, "detached probe should be held in pending")
	ls.mu.Lock()
	ring := string(ls.ring)
	ls.mu.Unlock()
	require.NotContains(t, ring, "ANSWERED", "session must stay blocked while detached")

	header := http.Header{"Cookie": {cookieName + "=" + s.auth.mintSession()}}
	wsURL := strings.Replace(ts.URL, "http", "ws", 1)
	conn, _, err := websocket.DefaultDialer.Dial(wsURL+"/api/attach/"+id+"?cols=80&rows=24", header)
	require.NoError(t, err)
	defer conn.Close()

	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	_, replay, err := conn.ReadMessage()
	require.NoError(t, err)
	require.True(t, strings.HasSuffix(string(replay), "\x1b[6n"), "replay ends with the forwarded probe, got %q", replay)

	require.NoError(t, conn.WriteMessage(websocket.BinaryMessage, []byte("\x1b[9;9R\r")))
	deadline := time.Now().Add(10 * time.Second)
	var seen strings.Builder
	seen.Write(replay)
	for !strings.Contains(seen.String(), "ANSWERED") && time.Now().Before(deadline) {
		_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
		_, data, err := conn.ReadMessage()
		if err != nil {
			break
		}
		seen.Write(data)
	}
	require.Contains(t, seen.String(), "ANSWERED", "answering the forwarded probe should unblock the session")
}
