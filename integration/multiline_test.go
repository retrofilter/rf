package integration

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestMultilineScheme(t *testing.T) {
	t.Parallel()
	s := startShell(t, t.TempDir())

	s.sendLine(`(string-append "multi"`)
	s.sendLine(`"line-ok")`)
	s.expect(`multiline-ok`)

	// Pasted multi-line define, then call it.
	s.clear()
	s.send("\x1b[200~(define (paste-dbl x)\r  (* x 2))\x1b[201~\r")
	s.sendLine("(paste-dbl 23456)")
	s.expect(`46912`)

	// A paste holding several forms: all evaluate (ParseAll/EvalAll).
	s.clear()
	s.send("\x1b[200~(define paste-a 40000)\r(define paste-b 6912)\x1b[201~\r")
	s.sendLine("(+ paste-a paste-b)")
	s.expect(`46912`)
}

func TestMultilineCommandPaste(t *testing.T) {
	t.Parallel()
	s := startShell(t, t.TempDir())
	s.send("\x1b[200~echo first-paste-line\recho second-paste-line\x1b[201~\r")
	s.expect(`first-paste-line`)
	s.expect(`second-paste-line`)
}

func TestMultilineChatPaste(t *testing.T) {
	t.Parallel()

	var requests atomic.Int32
	var lastBody atomic.Value
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/messages" {
			http.NotFound(w, r)
			return
		}
		body, _ := io.ReadAll(r.Body)
		lastBody.Store(string(body))
		requests.Add(1)
		f := w.(http.Flusher)
		w.Header().Set("Content-Type", "text/event-stream")
		event := func(data string) {
			fmt.Fprintf(w, "data: %s\n\n", data)
			f.Flush()
		}
		event(`{"type":"message_start","message":{"usage":{"input_tokens":1}}}`)
		event(`{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`)
		event(`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"PASTED-OK"}}`)
		event(`{"type":"content_block_stop","index":0}`)
		event(`{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":2}}`)
		event(`{"type":"message_stop"}`)
	}))
	t.Cleanup(srv.Close)

	dir := t.TempDir()
	modelPrelude(t, dir, srv.URL)
	s := startShell(t, dir)
	s.send("\x1b[Z") // Shift+Tab -> agent mode
	s.expect(`\(~\) ⏺ `)
	s.clear()

	s.send("\x1b[200~first paste line\rsecond paste line\x1b[201~\r")
	s.expect(`PASTED-OK`)

	if n := requests.Load(); n != 1 {
		t.Errorf("paste drove %d requests, want 1 turn", n)
	}
	body, _ := lastBody.Load().(string)
	if !strings.Contains(body, `first paste line\nsecond paste line`) {
		t.Errorf("request body missing the joined paste: %s", body)
	}
}
