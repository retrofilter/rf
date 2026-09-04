package integration

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/retrofilter/rf/models"
	"github.com/retrofilter/rf/models/schema"
)

func TestChatSessionPersistence(t *testing.T) {
	t.Parallel()
	url, _ := fakeOllama(t)

	dir := t.TempDir()
	modelPrelude(t, dir, url)
	s := startShell(t, dir)

	s.send("\x1b[Z") // Shift+Tab -> agent mode
	s.expect(`\(~\) ⏺ `)
	s.clear()

	s.sendLine("hi there")
	s.expect(`Hello WORLDTOKEN friend\.`)
	s.expect(`↑1 ↓3`)

	home := dir
	if resolved, err := filepath.EvalSymlinks(dir); err == nil {
		home = resolved
	}
	db, err := schema.OpenDB(filepath.Join(home, ".rf", "main.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	sessions, err := models.ListSessions(db, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(sessions) != 1 {
		t.Fatalf("found %d sessions, want 1", len(sessions))
	}
	sess := sessions[0]
	if sess.Title != "hi there" {
		t.Errorf("session title = %q, want %q", sess.Title, "hi there")
	}
	if sess.Cwd != home {
		t.Errorf("session cwd = %q, want %q", sess.Cwd, home)
	}

	transcript, err := models.SessionTranscript(db, sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(transcript) != 2 {
		t.Fatalf("transcript has %d messages, want 2\n%+v", len(transcript), transcript)
	}
	if transcript[0].Role != models.MessageRoleUser || transcript[0].Content != "hi there" {
		t.Errorf("first message = %+v, want user %q", transcript[0], "hi there")
	}
	if transcript[1].Role != models.MessageRoleAssistant || transcript[1].Content != "Hello WORLDTOKEN friend." {
		t.Errorf("second message = %+v, want assistant reply", transcript[1])
	}
}

func TestClearSession(t *testing.T) {
	t.Parallel()
	var requests atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/messages" {
			http.NotFound(w, r)
			return
		}
		body, _ := io.ReadAll(r.Body)
		msgs := strings.Count(string(body), `"role"`)
		f := w.(http.Flusher)
		w.Header().Set("Content-Type", "text/event-stream")
		event := func(data string) {
			fmt.Fprintf(w, "data: %s\n\n", data)
			f.Flush()
		}
		n := requests.Add(1)
		event(`{"type":"message_start","message":{"usage":{"input_tokens":1}}}`)
		event(`{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`)
		event(fmt.Sprintf(`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"REPLY%d with %d messages."}}`, n, msgs))
		event(`{"type":"content_block_stop","index":0}`)
		event(`{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":3}}`)
		event(`{"type":"message_stop"}`)
	}))
	t.Cleanup(srv.Close)

	dir := t.TempDir()
	modelPrelude(t, dir, srv.URL)
	s := startShell(t, dir)

	s.send("\x1b[Z") // agent mode
	s.expect(`\(~\) ⏺ `)
	s.sendLine("first session")
	s.expect(`REPLY1 with 1 messages\.`)
	s.expect(`↑1 ↓3`) // the turn's entries are recorded

	s.send("\x1b[Z")
	s.clear()
	s.sendLine("clear")

	s.waitReady()
	s.send("\x1b[Z")
	s.expect(`\(~\) ⏺ `)
	s.sendLine("second session")
	s.expect(`REPLY2 with 1 messages\.`)
	s.expect(`↑2 ↓6`) // usage totals span the process, not the session

	home := dir
	if resolved, err := filepath.EvalSymlinks(dir); err == nil {
		home = resolved
	}
	db, err := schema.OpenDB(filepath.Join(home, ".rf", "main.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	sessions, err := models.ListSessions(db, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(sessions) != 2 {
		t.Fatalf("found %d sessions, want 2", len(sessions))
	}
	titles := map[string]bool{}
	for _, sess := range sessions {
		titles[sess.Title] = true
		transcript, err := models.SessionTranscript(db, sess.ID)
		if err != nil {
			t.Fatal(err)
		}
		if len(transcript) != 2 {
			t.Errorf("session %q has %d messages, want 2", sess.Title, len(transcript))
		}
	}
	if !titles["first session"] || !titles["second session"] {
		t.Errorf("session titles = %v, want both turns", titles)
	}
}

func TestResumePicker(t *testing.T) {
	t.Parallel()
	url, _ := fakeOllama(t)
	dir := t.TempDir()

	// First shell: one completed chat turn, then gone.
	modelPrelude(t, dir, url)
	s1 := startShell(t, dir)
	s1.send("\x1b[Z")
	s1.expect(`\(~\) ⏺ `)
	s1.sendLine("hi there")
	s1.expect(`↑1 ↓3`) // prints after the turn's entries are recorded
	s1.close()

	// Second shell, same HOME: resume from command mode.
	s2 := startShell(t, dir)
	s2.sendLine("resume")
	s2.expect(`resume session`)
	s2.expect(`hi there\s+.*2 msgs`) // picker row: title + dimmed metadata
	s2.clear()
	s2.send("\r") // select the (only) session

	s2.expect(`resumed hi there — 2 messages`)
	// The tail replays: the user line and the rendered assistant reply.
	s2.expect(`» hi there`)
	s2.expect(`Hello WORLDTOKEN friend\.`)
	// Resume drops into agent mode.
	s2.expect(`\(~\) ⏺ `)
}
