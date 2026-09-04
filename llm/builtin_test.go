package llm

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/jmoiron/sqlx"
	"github.com/retrofilter/rf/core"
	"github.com/retrofilter/rf/eval"
	"github.com/retrofilter/rf/models"
	_ "modernc.org/sqlite"
)

func evalLLM(t *testing.T, chat *Chat, code string) (eval.Value, error) {
	t.Helper()
	exprs, err := eval.ParseAll(code)
	if err != nil {
		t.Fatalf("parse %q: %v", code, err)
	}
	return chat.Eval.EvalAll(exprs, chat.Env)
}

func TestLLMBuiltin(t *testing.T) {
	db, err := sqlx.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	gs := core.NewGraphStore(db)

	fake := &fakeModel{responses: []*Response{
		textResponse("a summary\n"),
	}}
	chat := newChat(gs, Config{Provider: models.ProviderOllama, Model: "fake"}, fake)

	result, err := evalLLM(t, chat, `(llm "one-line summary" "file contents here" {:size 5})`)
	if err != nil {
		t.Fatalf("(llm ...) failed: %v", err)
	}
	if got, ok := result.(eval.String); !ok || string(got) != "a summary" {
		t.Errorf("expected trimmed string \"a summary\", got %s", eval.PrintValue(result))
	}

	if len(fake.calls) != 1 {
		t.Fatalf("expected 1 model call, got %d", len(fake.calls))
	}
	call := fake.calls[0]
	if call.System != llmSystemPrompt {
		t.Errorf("expected the function-style system prompt, got %q", call.System)
	}
	if len(call.Tools) != 0 {
		t.Errorf("(llm ...) must not expose tools, got %+v", call.Tools)
	}
	if len(call.Messages) != 1 || call.Messages[0].Role != "user" {
		t.Fatalf("expected a single user message, got %+v", call.Messages)
	}
	human := callText(call)
	for _, want := range []string{"one-line summary", "file contents here", `{:size 5}`} {
		if !strings.Contains(human, want) {
			t.Errorf("user message missing %q:\n%s", want, human)
		}
	}
}

func TestLLMBuiltinComposes(t *testing.T) {
	db, err := sqlx.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	gs := core.NewGraphStore(db)

	fake := &fakeModel{responses: []*Response{
		textResponse("A"), textResponse("B"),
	}}
	chat := newChat(gs, Config{Provider: models.ProviderOllama, Model: "fake"}, fake)

	result, err := evalLLM(t, chat, `(map (lambda (s) (llm "classify" s)) (list "x" "y"))`)
	if err != nil {
		t.Fatalf("map over llm failed: %v", err)
	}
	if got := eval.PrintValue(result); got != `("A" "B")` {
		t.Errorf(`expected ("A" "B"), got %s`, got)
	}
	if len(fake.calls) != 2 {
		t.Errorf("expected 2 model calls, got %d", len(fake.calls))
	}
}

func TestLLMBuiltinArgErrors(t *testing.T) {
	db, err := sqlx.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	gs := core.NewGraphStore(db)
	chat := newChat(gs, Config{Provider: models.ProviderOllama, Model: "fake"}, &fakeModel{})

	if _, err := evalLLM(t, chat, `(llm)`); err == nil {
		t.Error("expected error for (llm) with no arguments")
	}
	if _, err := evalLLM(t, chat, `(llm 42)`); err == nil {
		t.Error("expected error for non-string prompt")
	}
}

type blockingModel struct{}

func (m *blockingModel) Messages(ctx context.Context, req *Request, onDelta func(DeltaKind, string)) (*Response, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}

func TestLLMBuiltinInterruptible(t *testing.T) {
	eval.ClearInterrupt()
	defer eval.ClearInterrupt()

	db, err := sqlx.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	chat := newChat(core.NewGraphStore(db), Config{Provider: models.ProviderOllama, Model: "fake"}, &blockingModel{})

	done := make(chan error, 1)
	go func() {
		_, err := chat.plainCompletion("system", "hang forever")
		done <- err
	}()
	time.AfterFunc(50*time.Millisecond, eval.Interrupt)

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("expected a cancellation error, got nil")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("interrupt did not cancel the in-flight completion")
	}
}
