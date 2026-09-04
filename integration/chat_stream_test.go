package integration

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func fakeOllama(t *testing.T) (url string, canceled chan struct{}) {
	t.Helper()
	canceled = make(chan struct{})
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
		event(`{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`)

		if requests.Add(1) == 1 {
			for _, word := range []string{"Hello ", "WORLDTOKEN ", "friend."} {
				event(`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"` + word + `"}}`)
				time.Sleep(50 * time.Millisecond)
			}
			event(`{"type":"content_block_stop","index":0}`)
			event(`{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":3}}`)
			event(`{"type":"message_stop"}`)
			return
		}

		for i := 0; ; i++ {
			select {
			case <-r.Context().Done():
				close(canceled)
				return
			case <-time.After(100 * time.Millisecond):
			}
			event(fmt.Sprintf(`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"STREAMCHUNK%d "}}`, i))
		}
	}))
	t.Cleanup(srv.Close)
	return srv.URL, canceled
}

func TestChatStreamSteerAndCancel(t *testing.T) {
	t.Parallel()
	url, canceled := fakeOllama(t)

	dir := t.TempDir()
	modelPrelude(t, dir, url)
	s := startShell(t, dir)

	s.send("\x1b[Z") // Shift+Tab -> agent mode
	s.expect(`\(~\) ⏺ `)
	s.clear()

	s.sendLine("hi there")
	s.expect(`working\.\. \(ctrl\+g to cancel\)`)
	s.send("shorter please\r")
	s.expect(`WORLDTOKEN`)
	s.expect(`» shorter please`)
	s.expect(`STREAMCHUNK3`)

	s.send("\x07") // ctrl+g cancels the running turn
	s.expect(`interrupted\r\n\r\n↑1 ↓3\r\n\r\n\(~\) ⏺ `)
	select {
	case <-canceled:
	case <-time.After(5 * time.Second):
		t.Fatal("the in-flight provider request was not canceled")
	}

	// The turn ended: the prompt is back and the evaluator answers.
	s.clear()
	s.sendLine("(+ 20 22)")
	s.expect(`42`)
}
