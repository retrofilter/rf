package llm

import (
	"context"
	"strings"
	"testing"

	"github.com/jmoiron/sqlx"
	"github.com/retrofilter/rf/core"
	"github.com/retrofilter/rf/eval"
	"github.com/retrofilter/rf/models"
	_ "modernc.org/sqlite"
)

func newHookChat(t *testing.T, prelude string, responses ...*Response) *Chat {
	t.Helper()
	db, err := sqlx.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	gs := core.NewGraphStore(db)
	chat := newChat(gs, Config{Provider: models.ProviderOllama, Model: "fake"}, &fakeModel{responses: responses})
	if prelude != "" {
		exprs, err := eval.ParseAll(prelude)
		if err != nil {
			t.Fatalf("parse prelude: %v", err)
		}
		if _, err := chat.Eval.EvalAll(exprs, chat.Env); err != nil {
			t.Fatalf("eval prelude: %v", err)
		}
	}
	return chat
}

func lookupString(t *testing.T, chat *Chat, name string) string {
	t.Helper()
	v, err := chat.Env.Lookup(name)
	if err != nil {
		t.Fatalf("lookup %s: %v", name, err)
	}
	s, ok := v.(eval.String)
	if !ok {
		t.Fatalf("%s: expected string, got %T (%v)", name, v, v)
	}
	return string(s)
}

func toolResultSentToModel(t *testing.T, chat *Chat) ContentBlock {
	t.Helper()
	fake := chat.model.(*fakeModel)
	if len(fake.calls) < 2 {
		t.Fatalf("expected a second model call carrying the tool result, got %d", len(fake.calls))
	}
	for _, msg := range fake.calls[1].Messages {
		for _, block := range msg.Content {
			if block.Type == "tool_result" {
				return block
			}
		}
	}
	t.Fatal("no tool_result block in second model call")
	return ContentBlock{}
}

func TestBeforeToolHookBlocks(t *testing.T) {
	chat := newHookChat(t, `
		(define probe "untouched")
		(define (before-tool-hook call) false)`,
		toolCallResponse("toolu_1", `(set! probe "mutated")`),
		textResponse("ok"))

	if _, err := chat.Message(context.Background(), "go", nil, ""); err != nil {
		t.Fatalf("Message: %v", err)
	}
	if got := lookupString(t, chat, "probe"); got != "untouched" {
		t.Errorf("blocked tool call still ran: probe=%q", got)
	}
	block := toolResultSentToModel(t, chat)
	if !block.IsError || !strings.Contains(block.Content, "blocked by before-tool-hook") {
		t.Errorf("model should see the block as a tool error, got %+v", block)
	}
}

func TestBeforeToolHookReason(t *testing.T) {
	chat := newHookChat(t,
		`(define (before-tool-hook call) "network is off limits")`,
		toolCallResponse("toolu_1", `(+ 1 2)`),
		textResponse("ok"))

	if _, err := chat.Message(context.Background(), "go", nil, ""); err != nil {
		t.Fatalf("Message: %v", err)
	}
	block := toolResultSentToModel(t, chat)
	if !block.IsError || !strings.Contains(block.Content, "network is off limits") {
		t.Errorf("expected the hook's reason in the tool error, got %+v", block)
	}
}

func TestBeforeToolHookAllows(t *testing.T) {
	chat := newHookChat(t, `
		(define seen-tool "")
		(define seen-code "")
		(define (before-tool-hook call)
		  (set! seen-tool (get "tool" call))
		  (set! seen-code (get "code" (get "input" call)))
		  true)`,
		toolCallResponse("toolu_1", `(+ 40 2)`),
		textResponse("ok"))

	if _, err := chat.Message(context.Background(), "go", nil, ""); err != nil {
		t.Fatalf("Message: %v", err)
	}
	block := toolResultSentToModel(t, chat)
	if block.IsError || !strings.Contains(block.Content, "42") {
		t.Errorf("allowed call should have run: %+v", block)
	}
	if got := lookupString(t, chat, "seen-tool"); got != "scheme" {
		t.Errorf("hook saw tool %q, want scheme", got)
	}
	if got := lookupString(t, chat, "seen-code"); got != "(+ 40 2)" {
		t.Errorf("hook saw code %q", got)
	}
}

func TestAfterToolHookObservesAndRewrites(t *testing.T) {
	chat := newHookChat(t, `
		(define seen-result "")
		(define (after-tool-hook call)
		  (set! seen-result (get "result" call))
		  "REDACTED")`,
		toolCallResponse("toolu_1", `(+ 40 2)`),
		textResponse("ok"))

	if _, err := chat.Message(context.Background(), "go", nil, ""); err != nil {
		t.Fatalf("Message: %v", err)
	}
	if got := lookupString(t, chat, "seen-result"); !strings.Contains(got, "42") {
		t.Errorf("hook should see the raw result, got %q", got)
	}
	block := toolResultSentToModel(t, chat)
	if block.Content != "REDACTED" {
		t.Errorf("model should see the rewritten result, got %q", block.Content)
	}
}

func TestAfterToolHookSeesError(t *testing.T) {
	chat := newHookChat(t, `
		(define seen-error "")
		(define (after-tool-hook call)
		  (set! seen-error (get "error" call))
		  "REPLACEMENT")`,
		toolCallResponse("toolu_1", `(no-such-function)`),
		textResponse("ok"))

	if _, err := chat.Message(context.Background(), "go", nil, ""); err != nil {
		t.Fatalf("Message: %v", err)
	}
	if got := lookupString(t, chat, "seen-error"); got == "" {
		t.Error("hook should see the tool error")
	}
	block := toolResultSentToModel(t, chat)
	if !block.IsError || block.Content == "REPLACEMENT" {
		t.Errorf("error results must not be rewritten, got %+v", block)
	}
}

func TestAfterTurnHook(t *testing.T) {
	chat := newHookChat(t, `
		(define ended "")
		(define (after-turn-hook turn)
		  (set! ended (get "text" turn)))`,
		textResponse("all done"))

	if _, err := chat.Message(context.Background(), "go", nil, ""); err != nil {
		t.Fatalf("Message: %v", err)
	}
	if got := lookupString(t, chat, "ended"); got != "all done" {
		t.Errorf("after-turn-hook saw %q, want %q", got, "all done")
	}
}

func TestHookErrorNeverBreaksTurn(t *testing.T) {
	chat := newHookChat(t,
		`(define (before-tool-hook call) (this-does-not-exist))`,
		toolCallResponse("toolu_1", `(+ 40 2)`),
		textResponse("ok"))

	var notices []string
	chat.OnEvent = func(e Event, msg *models.Message) {
		if e == EventNotice {
			notices = append(notices, msg.Content)
		}
	}

	if _, err := chat.Message(context.Background(), "go", nil, ""); err != nil {
		t.Fatalf("Message: %v", err)
	}
	block := toolResultSentToModel(t, chat)
	if block.IsError || !strings.Contains(block.Content, "42") {
		t.Errorf("tool call should run despite the broken hook: %+v", block)
	}
	if len(notices) == 0 || !strings.Contains(notices[0], "before-tool-hook error") {
		t.Errorf("expected a hook-error notice, got %v", notices)
	}
}

func TestHooksRunAsAssistant(t *testing.T) {
	chat := newHookChat(t, "",
		toolCallResponse("toolu_1", `(+ 1 1)`),
		textResponse("ok"))

	var caller string
	chat.Env.Set("before-tool-hook", eval.BuiltinFunc(func(args []eval.Value, env *eval.Environment) (eval.Value, error) {
		caller = chat.Eval.Caller()
		return true, nil
	}))

	if _, err := chat.Message(context.Background(), "go", nil, ""); err != nil {
		t.Fatalf("Message: %v", err)
	}
	if caller != eval.CallerAssistant {
		t.Errorf("hook ran as %q, want %q", caller, eval.CallerAssistant)
	}
}

func TestFileToolHookPayload(t *testing.T) {
	chat := newHookChat(t, `
		(define (before-tool-hook call)
		  (if (equal? (get "tool" call) "write")
		      "writes are disabled"
		      true))`,
		&Response{
			StopReason: "tool_use",
			Content: []ContentBlock{{
				Type: "tool_use", ID: "toolu_1", Name: "write",
				Input: []byte(`{"path":"/tmp/x","content":"hi"}`),
			}},
		},
		textResponse("ok"))

	if _, err := chat.Message(context.Background(), "go", nil, ""); err != nil {
		t.Fatalf("Message: %v", err)
	}
	block := toolResultSentToModel(t, chat)
	if !block.IsError || !strings.Contains(block.Content, "writes are disabled") {
		t.Errorf("write should have been vetoed: %+v", block)
	}
}
