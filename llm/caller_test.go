package llm

import (
	"context"
	"strings"
	"testing"

	"github.com/jmoiron/sqlx"
	"github.com/retrofilter/rf/core"
	"github.com/retrofilter/rf/eval"
	"github.com/retrofilter/rf/models"
)

func TestToolCallsRunAsAssistant(t *testing.T) {
	db, err := sqlx.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	fake := &fakeModel{responses: []*Response{
		toolCallResponse("t1", "(caller-check)"),
		textResponse("done"),
	}}
	chat := newChat(core.NewGraphStore(db), Config{Model: "fake"}, fake)
	chat.Env.Set("caller-check", eval.BuiltinFunc(func(args []eval.Value, env *eval.Environment) (eval.Value, error) {
		if err := chat.Eval.RequireUser("caller-check"); err != nil {
			return nil, err
		}
		return eval.String("ok"), nil
	}))

	msgs, err := chat.Message(context.Background(), "try it", nil, "")
	if err != nil {
		t.Fatal(err)
	}
	var toolMsg *models.Message
	for _, m := range msgs {
		if m.Type == models.MessageTypeTool {
			toolMsg = m
		}
	}
	if toolMsg == nil {
		t.Fatal("no tool call in the transcript")
	}
	if !strings.Contains(toolMsg.ErrorMessage, "only be run by the user") {
		t.Errorf("tool call error = %q, want a RequireUser rejection", toolMsg.ErrorMessage)
	}

	if got := chat.Eval.Caller(); got != eval.CallerUser {
		t.Errorf("caller after the turn = %q, want %q", got, eval.CallerUser)
	}
	expr, err := eval.Parse("(caller-check)")
	if err != nil {
		t.Fatal(err)
	}
	v, err := chat.Eval.Eval(expr, chat.Env)
	if err != nil {
		t.Fatalf("user-typed call rejected: %v", err)
	}
	if v != eval.String("ok") {
		t.Errorf("user-typed call = %v, want ok", v)
	}
}

func TestNestedToolCallRestoresCallerAndApprover(t *testing.T) {
	db, err := sqlx.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	chat := newChat(core.NewGraphStore(db), Config{Model: "fake"}, &fakeModel{})

	// Simulate being inside an outer tool call: assistant caller, gate up.
	outerGate := func(string) bool { return true }
	chat.Approve = outerGate
	chat.Eval.SetApprover(outerGate)
	chat.Eval.SetCaller(eval.CallerAssistant)
	defer chat.Eval.SetCaller(eval.CallerUser)

	msg := &models.Message{Type: models.MessageTypeTool, FunctionName: "scheme", Code: "(+ 1 2)"}
	chat.executeToolCall(msg)
	if msg.Result != "3" {
		t.Fatalf("inner tool call result = %q (err %q), want 3", msg.Result, msg.ErrorMessage)
	}

	if got := chat.Eval.Caller(); got != eval.CallerAssistant {
		t.Errorf("caller after nested tool call = %q, want assistant (outer call still running)", got)
	}
	if chat.Eval.Approver() == nil {
		t.Error("approver was stripped by the nested tool call finishing")
	}
}
