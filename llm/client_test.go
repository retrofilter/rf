package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func sseHandler(t *testing.T, events []string, requests *[]Request) http.HandlerFunc {
	t.Helper()
	return func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/messages" {
			t.Errorf("unexpected path %s", r.URL.Path)
		}
		if v := r.Header.Get("anthropic-version"); v == "" {
			t.Error("missing anthropic-version header")
		}
		var req Request
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("decode request: %v", err)
		}
		if requests != nil {
			*requests = append(*requests, req)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		for _, ev := range events {
			fmt.Fprint(w, ev)
		}
	}
}

func TestClientStreaming(t *testing.T) {
	events := []string{
		"event: message_start\n",
		`data: {"type":"message_start","message":{"id":"msg_1","usage":{"input_tokens":25,"cache_creation_input_tokens":300,"cache_read_input_tokens":4000}}}` + "\n\n",
		"event: content_block_start\n",
		`data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}` + "\n\n",
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Let me "}}` + "\n\n",
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"compute that."}}` + "\n\n",
		`data: {"type":"content_block_stop","index":0}` + "\n\n",
		`data: {"type":"content_block_start","index":1,"content_block":{"type":"tool_use","id":"toolu_9","name":"scheme","input":{}}}` + "\n\n",
		`data: {"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"{\"code\":"}}` + "\n\n",
		`data: {"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"\"(+ 1 2)\"}"}}` + "\n\n",
		`data: {"type":"content_block_stop","index":1}` + "\n\n",
		"event: ping\n",
		`data: {"type":"ping"}` + "\n\n",
		`data: {"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":50}}` + "\n\n",
		`data: {"type":"message_stop"}` + "\n\n",
	}
	var requests []Request
	srv := httptest.NewServer(sseHandler(t, events, &requests))
	defer srv.Close()

	client := &Client{BaseURL: srv.URL, APIKey: "test-key"}
	var deltas []string
	resp, err := client.Messages(context.Background(), &Request{
		Model:     "granite4:3b",
		MaxTokens: 100,
		Messages:  []Message{TextMessage("user", "1+2?")},
	}, func(_ DeltaKind, d string) { deltas = append(deltas, d) })
	if err != nil {
		t.Fatalf("Messages: %v", err)
	}

	if len(requests) != 1 || !requests[0].Stream {
		t.Errorf("expected one streaming request, got %+v", requests)
	}
	if got := strings.Join(deltas, ""); got != "Let me compute that." {
		t.Errorf("deltas = %q", got)
	}
	if len(resp.Content) != 2 {
		t.Fatalf("expected 2 content blocks, got %+v", resp.Content)
	}
	if resp.Content[0].Type != "text" || resp.Content[0].Text != "Let me compute that." {
		t.Errorf("text block = %+v", resp.Content[0])
	}
	tool := resp.Content[1]
	if tool.Type != "tool_use" || tool.ID != "toolu_9" || tool.Name != "scheme" {
		t.Errorf("tool block = %+v", tool)
	}
	var input struct {
		Code string `json:"code"`
	}
	if err := json.Unmarshal(tool.Input, &input); err != nil || input.Code != "(+ 1 2)" {
		t.Errorf("tool input = %s (err %v)", tool.Input, err)
	}
	if resp.StopReason != "tool_use" {
		t.Errorf("stop_reason = %q", resp.StopReason)
	}
	if resp.Usage.InputTokens != 25 || resp.Usage.OutputTokens != 50 {
		t.Errorf("usage = %+v", resp.Usage)
	}
	if resp.Usage.CacheCreationInputTokens != 300 || resp.Usage.CacheReadInputTokens != 4000 {
		t.Errorf("cache usage = %+v, want 300 created / 4000 read", resp.Usage)
	}
}

func TestClientStreamingThinking(t *testing.T) {
	events := []string{
		`data: {"type":"message_start","message":{"usage":{"input_tokens":10}}}` + "\n\n",
		`data: {"type":"content_block_start","index":0,"content_block":{"type":"thinking","thinking":"","signature":""}}` + "\n\n",
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"Consider"}}` + "\n\n",
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":" carefully."}}` + "\n\n",
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"signature_delta","signature":"sig123"}}` + "\n\n",
		`data: {"type":"content_block_stop","index":0}` + "\n\n",
		`data: {"type":"content_block_start","index":1,"content_block":{"type":"text","text":""}}` + "\n\n",
		`data: {"type":"content_block_delta","index":1,"delta":{"type":"text_delta","text":"Done."}}` + "\n\n",
		`data: {"type":"content_block_stop","index":1}` + "\n\n",
		`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":9}}` + "\n\n",
		`data: {"type":"message_stop"}` + "\n\n",
	}
	var requests []Request
	srv := httptest.NewServer(sseHandler(t, events, &requests))
	defer srv.Close()

	client := &Client{BaseURL: srv.URL, APIKey: "test-key"}
	var thinking, text strings.Builder
	resp, err := client.Messages(context.Background(), &Request{
		Model:     "claude-sonnet-4-6",
		MaxTokens: 100,
		Thinking:  &Thinking{Type: "adaptive", Display: "summarized"},
		Messages:  []Message{TextMessage("user", "ponder")},
	}, func(kind DeltaKind, d string) {
		if kind == DeltaThinking {
			thinking.WriteString(d)
		} else {
			text.WriteString(d)
		}
	})
	if err != nil {
		t.Fatalf("Messages: %v", err)
	}

	// The thinking param survives the wire (sseHandler decoded it back).
	if len(requests) != 1 || requests[0].Thinking == nil ||
		requests[0].Thinking.Type != "adaptive" || requests[0].Thinking.Display != "summarized" {
		t.Errorf("request thinking = %+v", requests[0].Thinking)
	}
	if thinking.String() != "Consider carefully." || text.String() != "Done." {
		t.Errorf("deltas: thinking=%q text=%q", thinking.String(), text.String())
	}
	if len(resp.Content) != 2 {
		t.Fatalf("expected 2 content blocks, got %+v", resp.Content)
	}
	tb := resp.Content[0]
	if tb.Type != "thinking" || tb.Thinking == nil || *tb.Thinking != "Consider carefully." || tb.Signature != "sig123" {
		t.Errorf("thinking block = %+v", tb)
	}

	empty := ""
	out, _ := json.Marshal(ContentBlock{Type: "thinking", Thinking: &empty, Signature: "s"})
	if !strings.Contains(string(out), `"thinking":""`) {
		t.Errorf("empty thinking block dropped its field: %s", out)
	}
	out, _ = json.Marshal(ContentBlock{Type: "text", Text: "hi"})
	if strings.Contains(string(out), "thinking") || strings.Contains(string(out), "signature") {
		t.Errorf("text block grew thinking fields: %s", out)
	}
}

func TestClientNonStreaming(t *testing.T) {
	var gotKey string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotKey = r.Header.Get("x-api-key")
		var req Request
		_ = json.NewDecoder(r.Body).Decode(&req)
		if req.Stream {
			t.Error("expected a non-streaming request")
		}
		_ = json.NewEncoder(w).Encode(Response{
			Content:    []ContentBlock{{Type: "text", Text: "3"}},
			StopReason: "end_turn",
			Usage:      Usage{InputTokens: 10, OutputTokens: 1},
		})
	}))
	defer srv.Close()

	client := &Client{BaseURL: srv.URL, APIKey: "sk-test"}
	resp, err := client.Messages(context.Background(), &Request{
		Model:     "claude-sonnet-4-6",
		MaxTokens: 100,
		Messages:  []Message{TextMessage("user", "1+2?")},
	}, nil)
	if err != nil {
		t.Fatalf("Messages: %v", err)
	}
	if gotKey != "sk-test" {
		t.Errorf("x-api-key = %q", gotKey)
	}
	if len(resp.Content) != 1 || resp.Content[0].Text != "3" {
		t.Errorf("content = %+v", resp.Content)
	}
}

func TestClientAPIError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		fmt.Fprint(w, `{"type":"error","error":{"type":"authentication_error","message":"invalid x-api-key"}}`)
	}))
	defer srv.Close()

	client := &Client{BaseURL: srv.URL}
	_, err := client.Messages(context.Background(), &Request{Model: "m", MaxTokens: 1}, nil)
	if err == nil || !strings.Contains(err.Error(), "invalid x-api-key") {
		t.Errorf("expected authentication error, got %v", err)
	}
}

func TestClientStreamError(t *testing.T) {
	events := []string{
		`data: {"type":"message_start","message":{"usage":{"input_tokens":1}}}` + "\n\n",
		`data: {"type":"error","error":{"type":"overloaded_error","message":"Overloaded"}}` + "\n\n",
	}
	srv := httptest.NewServer(sseHandler(t, events, nil))
	defer srv.Close()

	client := &Client{BaseURL: srv.URL}
	_, err := client.Messages(context.Background(), &Request{Model: "m", MaxTokens: 1}, func(DeltaKind, string) {})
	if err == nil || !strings.Contains(err.Error(), "Overloaded") {
		t.Errorf("expected overloaded error, got %v", err)
	}
}

func TestNewClientForConfig(t *testing.T) {
	t.Setenv("RF_ANTHROPIC_API_KEY", "sk-host")
	t.Setenv("RF_OLLAMA_API_KEY", "")
	c := NewClientForConfig(Config{Provider: "anthropic", APIKey: "sk-x"})
	if c.BaseURL != "https://api.anthropic.com" || c.APIKey != "sk-x" {
		t.Errorf("anthropic client = %+v", c)
	}
	c = NewClientForConfig(Config{Provider: "ollama", BaseURL: "http://box:11434"})
	if c.BaseURL != "http://box:11434" || c.APIKey != "" {
		t.Errorf("ollama client = %+v", c)
	}
	c = NewClientForConfig(Config{Provider: "ollama"})
	if c.BaseURL != "http://localhost:11434" {
		t.Errorf("default ollama url = %q", c.BaseURL)
	}
	c = NewClientForConfig(Config{Provider: "deepseek", BaseURL: "https://api.deepseek.com/anthropic", APIKey: "sk-d"})
	if c.BaseURL != "https://api.deepseek.com/anthropic" || c.APIKey != "sk-d" {
		t.Errorf("custom provider client = %+v", c)
	}
}

func TestClientStreamTruncated(t *testing.T) {
	events := []string{
		`data: {"type":"message_start","message":{"usage":{"input_tokens":10}}}` + "\n\n",
		`data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}` + "\n\n",
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"half an ans"}}` + "\n\n",
		// connection closes cleanly here — no message_delta, no message_stop
	}
	srv := httptest.NewServer(sseHandler(t, events, nil))
	defer srv.Close()

	client := &Client{BaseURL: srv.URL, APIKey: "test-key"}
	_, err := client.Messages(context.Background(), &Request{
		Model:     "claude-sonnet-4-6",
		MaxTokens: 100,
		Messages:  []Message{TextMessage("user", "hi")},
	}, func(DeltaKind, string) {})
	if err == nil || !strings.Contains(err.Error(), "message_stop") {
		t.Fatalf("truncated stream should error, got %v", err)
	}
	if !isRetryableError(err) {
		t.Errorf("truncation should classify as retryable: %v", err)
	}
}
