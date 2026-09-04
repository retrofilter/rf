package integration

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/retrofilter/rf/models"
	"github.com/retrofilter/rf/models/schema"
)

func rootSessionID(sessions []models.Session) string {
	for i := range sessions {
		if sessions[i].Parent == "" {
			return sessions[i].ID
		}
	}
	return ""
}

func fakeReplyOllama(t *testing.T, reply string) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/messages" {
			http.NotFound(w, r)
			return
		}
		body, _ := io.ReadAll(r.Body)
		if !strings.Contains(string(body), `"stream":true`) {
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprintf(w, `{"content":[{"type":"text","text":"%s"}],"usage":{"input_tokens":1,"output_tokens":2}}`, reply)
			return
		}
		f := w.(http.Flusher)
		w.Header().Set("Content-Type", "text/event-stream")
		event := func(data string) {
			fmt.Fprintf(w, "data: %s\n\n", data)
			f.Flush()
		}
		event(`{"type":"message_start","message":{"usage":{"input_tokens":1}}}`)
		event(`{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`)
		event(`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"` + reply + `"}}`)
		event(`{"type":"content_block_stop","index":0}`)
		event(`{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":2}}`)
		event(`{"type":"message_stop"}`)
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

func TestAgentBuiltinTopLevel(t *testing.T) {
	t.Parallel()
	url := fakeReplyOllama(t, "SUBAGENT-REPLY done.")

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("piped context\n"), 0644); err != nil {
		t.Fatal(err)
	}
	modelPrelude(t, dir, url)
	s := startShell(t, dir)

	s.sendLine(`(agent "say the magic word")`)
	s.expect(`Agent say the magic word`)
	s.expect(`SUBAGENT-REPLY done\.`)
	s.expectNot(`"SUBAGENT-REPLY done\."`)

	s.clear()
	s.sendLine(`(string-upcase *1)`)
	s.expect(`"SUBAGENT-REPLY DONE\."`)

	s.clear()
	s.sendLine("agent say the magic word")
	s.expect(`Agent say the magic word`)
	s.expect(`SUBAGENT-REPLY done\.`)

	s.clear()
	s.sendLine(`cat notes.txt | llm what does this say`)
	s.expect(`SUBAGENT-REPLY done\.`)
	s.expectNot(`command not found`)

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
		t.Fatalf("found %d sessions, want one per agent call", len(sessions))
	}
	for _, sess := range sessions {
		if sess.Title != "say the magic word" {
			t.Errorf("sub-session title = %q, want the instruction", sess.Title)
		}
		if sess.Parent == "" {
			t.Error("sub-session parent is empty, want the shell session id")
		}
	}

	transcript, err := models.SessionTranscript(db, sessions[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(transcript) != 2 {
		t.Fatalf("sub-session transcript has %d messages, want instruction + reply", len(transcript))
	}
	if transcript[1].Content != "SUBAGENT-REPLY done." {
		t.Errorf("recorded reply = %q", transcript[1].Content)
	}
}

func TestAgentBuiltinNested(t *testing.T) {
	t.Parallel()

	var requests atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/messages" {
			http.NotFound(w, r)
			return
		}
		f := w.(http.Flusher)
		w.Header().Set("Content-Type", "text/event-stream")
		event := func(data string) {
			fmt.Fprintf(w, "data: %s\n\n", data)
			f.Flush()
		}
		event(`{"type":"message_start","message":{"usage":{"input_tokens":1}}}`)
		switch requests.Add(1) {
		case 1:
			event(`{"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"toolu_1","name":"scheme","input":{}}}`)
			event(`{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"code\":\"(agent \\\"inner task\\\")\"}"}}`)
			event(`{"type":"content_block_stop","index":0}`)
			event(`{"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":2}}`)
		default:
			text := "OUTER-DONE"
			if requests.Load() == 2 {
				text = "INNER-DONE"
			}
			event(`{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`)
			event(`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"` + text + `"}}`)
			event(`{"type":"content_block_stop","index":0}`)
			event(`{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":2}}`)
		}
		event(`{"type":"message_stop"}`)
	}))
	t.Cleanup(srv.Close)

	dir := t.TempDir()
	modelPrelude(t, dir, srv.URL)
	s := startShell(t, dir)

	s.send("\x1b[Z") // Shift+Tab -> agent mode
	s.expect(`\(~\) ⏺ `)
	s.clear()

	s.sendLine("delegate something")
	s.expect(`Scheme`)                 // the parent's tool call renders
	s.expect(`\(agent "inner task"\)`) // with its code
	s.expect(`Agent inner task`)       // the sub-turn's header
	s.expect(`INNER-DONE`)             // sub-agent reply (block + tool result)
	s.expect(`OUTER-DONE`)             // parent's final answer
	s.expect(`\(~\) ⏺ `)               // turn over, prompt back

	home := dir
	if resolved, err := filepath.EvalSymlinks(dir); err == nil {
		home = resolved
	}
	db, err := schema.OpenDB(filepath.Join(home, ".rf", "main.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	deadline := time.Now().Add(5 * time.Second)
	var sessions []models.Session
	for {
		var err error
		sessions, err = models.ListSessions(db, 10)
		if err != nil {
			t.Fatal(err)
		}
		if len(sessions) == 2 {
			if tr, err := models.SessionTranscript(db, rootSessionID(sessions)); err == nil && len(tr) >= 3 {
				break
			}
		}
		if time.Now().After(deadline) {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if len(sessions) != 2 {
		t.Fatalf("found %d sessions, want the shell's and the sub-agent's", len(sessions))
	}
	var shellSess, agentSess *models.Session
	for i := range sessions {
		if sessions[i].Parent == "" {
			shellSess = &sessions[i]
		} else {
			agentSess = &sessions[i]
		}
	}
	if shellSess == nil || agentSess == nil {
		t.Fatalf("want one root and one parent-linked session, got %+v", sessions)
	}
	if agentSess.Parent != shellSess.ID {
		t.Errorf("sub-session parent = %q, want %q", agentSess.Parent, shellSess.ID)
	}
	if agentSess.Title != "inner task" {
		t.Errorf("sub-session title = %q, want the instruction", agentSess.Title)
	}

	transcript, err := models.SessionTranscript(db, shellSess.ID)
	if err != nil {
		t.Fatal(err)
	}
	var toolMsg *models.Message
	for _, m := range transcript {
		if m.Type == models.MessageTypeTool {
			toolMsg = m
		}
	}
	if toolMsg == nil {
		t.Fatal("no tool entry in the parent transcript")
	}
	if toolMsg.Result != `"INNER-DONE"` {
		t.Errorf("parent tool result = %q, want the sub-agent's reply only", toolMsg.Result)
	}
}
