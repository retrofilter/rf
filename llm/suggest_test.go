package llm

import (
	"context"
	"strings"
	"testing"

	"github.com/jmoiron/sqlx"
	"github.com/retrofilter/rf/core"
	"github.com/retrofilter/rf/models"
)

func newFakeChat(t *testing.T, responses ...*Response) (*Chat, *fakeModel) {
	t.Helper()
	db, err := sqlx.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	fake := &fakeModel{responses: responses}
	return newChat(core.NewGraphStore(db), Config{Provider: models.ProviderOllama, Model: "fake"}, fake), fake
}

func TestSuggest(t *testing.T) {
	chat, fake := newFakeChat(t, textResponse("```sh\nfind . -name '*.go' | head\n```"))

	got, err := chat.Suggest(context.Background(), "find go files here", "/tmp/proj")
	if err != nil {
		t.Fatalf("Suggest: %v", err)
	}
	if got != "find . -name '*.go' | head" {
		t.Fatalf("Suggest returned %q", got)
	}

	if len(fake.calls) != 1 {
		t.Fatalf("expected 1 model call, got %d", len(fake.calls))
	}
	req := fake.calls[0]
	if req.System != suggestSystemPrompt {
		t.Errorf("system prompt: got %q", req.System)
	}
	if req.MaxTokens != suggestMaxTokens {
		t.Errorf("max tokens: got %d, want %d", req.MaxTokens, suggestMaxTokens)
	}
	if len(req.Messages) != 1 {
		t.Fatalf("expected a single message, got %d", len(req.Messages))
	}
	body := callText(req)
	if !strings.Contains(body, "find go files here") || !strings.Contains(body, "/tmp/proj") {
		t.Errorf("request missing buffer or cwd: %q", body)
	}
}

func TestSuggestCapsBuffer(t *testing.T) {
	chat, fake := newFakeChat(t, textResponse("ls"))

	buffer := strings.Repeat("a", 4*suggestBufferCap) + " TAIL-MARKER"
	if _, err := chat.Suggest(context.Background(), buffer, "/"); err != nil {
		t.Fatalf("Suggest: %v", err)
	}

	body := callText(fake.calls[0])
	if len(body) > suggestBufferCap+256 {
		t.Errorf("request body is %d bytes — the buffer cap did not apply", len(body))
	}
	if !strings.Contains(body, "TAIL-MARKER") {
		t.Errorf("capping should keep the buffer tail")
	}
}

func TestFirstCommandLine(t *testing.T) {
	cases := []struct{ in, want string }{
		{"ls -la", "ls -la"},
		{"```sh\ngit log --oneline\n```", "git log --oneline"},
		{"\n\n  du -sh *  \n", "du -sh *"},
		{"grep -r foo .\nThis searches recursively.", "grep -r foo ."},
		{"```\n```", ""},
		{"", ""},
	}
	for _, c := range cases {
		if got := firstCommandLine(c.in); got != c.want {
			t.Errorf("firstCommandLine(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}
