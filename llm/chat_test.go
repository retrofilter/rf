package llm

import (
	"context"
	"os"
	"strconv"
	"strings"
	"testing"

	"github.com/jmoiron/sqlx"
	"github.com/retrofilter/rf/core"
	"github.com/retrofilter/rf/eval"
	"github.com/retrofilter/rf/models"
	_ "modernc.org/sqlite"
)

func TestNewChat(t *testing.T) {
	// Create a simple test database
	db, err := sqlx.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("Failed to create test database: %v", err)
	}
	defer db.Close()

	// Create GraphStore
	gs := core.NewGraphStore(db)

	chat, err := NewChat(gs)
	if err != nil {
		t.Fatalf("Failed to create chat: %v", err)
	}

	// Test that the chat instance was created properly
	if chat == nil {
		t.Fatal("Expected non-nil chat instance")
	}

	if chat.Eval == nil {
		t.Error("Expected non-nil evaluator")
	}

	if chat.Env == nil {
		t.Error("Expected non-nil environment")
	}

	tools := chat.Tools()
	if len(tools) == 0 {
		t.Error("Expected at least one tool")
	}

	// Test that the scheme tool is present
	foundEvalScheme := false
	for _, tool := range tools {
		if tool.Name == "scheme" {
			foundEvalScheme = true
			break
		}
	}

	if !foundEvalScheme {
		t.Error("Expected scheme tool to be present")
	}
}

type fakeModel struct {
	responses []*Response
	calls     []*Request
}

func (f *fakeModel) Messages(ctx context.Context, req *Request, onDelta func(DeltaKind, string)) (*Response, error) {
	f.calls = append(f.calls, req)
	if len(f.responses) == 0 {
		return textResponse("no more scripted responses"), nil
	}
	resp := f.responses[0]
	f.responses = f.responses[1:]
	if onDelta != nil {
		for _, block := range resp.Content {
			switch block.Type {
			case "text":
				half := len(block.Text) / 2
				onDelta(DeltaText, block.Text[:half])
				onDelta(DeltaText, block.Text[half:])
			case "thinking":
				if block.Thinking != nil && *block.Thinking != "" {
					onDelta(DeltaThinking, *block.Thinking)
				}
			}
		}
	}
	return resp, nil
}

func toolCallResponse(id, code string) *Response {
	return &Response{
		StopReason: "tool_use",
		Content: []ContentBlock{{
			Type:  "tool_use",
			ID:    id,
			Name:  "scheme",
			Input: []byte(`{"code":` + strconv.Quote(code) + `}`),
		}},
	}
}

func textResponse(text string) *Response {
	return &Response{
		StopReason: "end_turn",
		Content:    []ContentBlock{{Type: "text", Text: text}},
	}
}

func callText(req *Request) string {
	var sb strings.Builder
	for _, msg := range req.Messages {
		for _, block := range msg.Content {
			sb.WriteString(block.Text)
			sb.WriteString(block.Content)
			sb.Write(block.Input)
			sb.WriteString("\n")
		}
	}
	return sb.String()
}

func TestMessageThinkingReplay(t *testing.T) {
	db, err := sqlx.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	gs := core.NewGraphStore(db)

	think := "Addition is required; scheme can do that."
	fake := &fakeModel{responses: []*Response{
		{
			StopReason: "tool_use",
			Content: []ContentBlock{
				{Type: "thinking", Thinking: &think, Signature: "sig-abc"},
				{Type: "tool_use", ID: "toolu_1", Name: "scheme", Input: []byte(`{"code":"(+ 1 2)"}`)},
			},
		},
		textResponse("It is 3."),
	}}
	chat := newChat(gs, Config{Provider: models.ProviderAnthropic, Model: "claude-sonnet-4-6"}, fake)
	chat.Env.Set("default-model", eval.Dictionary{"thinking": true})

	var thinkingEvents []string
	chat.OnEvent = func(e Event, msg *models.Message) {
		if e == EventThinking {
			thinkingEvents = append(thinkingEvents, msg.Content)
		}
	}

	replies, err := chat.Message(context.Background(), "1+2?", nil, "")
	if err != nil {
		t.Fatalf("Message: %v", err)
	}
	if len(fake.calls) != 2 {
		t.Fatalf("expected 2 model calls, got %d", len(fake.calls))
	}

	// :thinking #t in default-model: adaptive with summarized display.
	if th := fake.calls[0].Thinking; th == nil || th.Type != "adaptive" || th.Display != "summarized" {
		t.Errorf("request thinking = %+v, want adaptive/summarized", fake.calls[0].Thinking)
	}

	if len(thinkingEvents) != 1 || thinkingEvents[0] != think {
		t.Errorf("thinking events = %q, want one carrying the block text", thinkingEvents)
	}
	for _, msg := range replies {
		if msg.Type == models.MessageTypeText && msg.Content == think {
			t.Errorf("thinking leaked into the transcript: %+v", msg)
		}
	}

	var assistant *Message
	for i := range fake.calls[1].Messages {
		if fake.calls[1].Messages[i].Role == "assistant" {
			assistant = &fake.calls[1].Messages[i]
			break
		}
	}
	if assistant == nil || len(assistant.Content) < 2 {
		t.Fatalf("missing assistant turn in continuation: %+v", fake.calls[1].Messages)
	}
	tb := assistant.Content[0]
	if tb.Type != "thinking" || tb.Thinking == nil || *tb.Thinking != think || tb.Signature != "sig-abc" {
		t.Errorf("thinking block should replay unmodified, got %+v", tb)
	}
	if assistant.Content[1].Type != "tool_use" {
		t.Errorf("tool_use should follow the thinking block, got %+v", assistant.Content[1])
	}
}

func TestThinkingAndEffortBindings(t *testing.T) {
	db, err := sqlx.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	gs := core.NewGraphStore(db)

	chat := newChat(gs, Config{Provider: models.ProviderAnthropic, Model: "claude-sonnet-4-6"}, &fakeModel{})

	// Unset: off — thinking shifts answer style, silence must not opt in.
	chat.Config()
	if th := chat.thinkingConfig(); th != nil {
		t.Errorf("default thinking = %+v, want nil (off)", th)
	}
	if oc := chat.outputConfig(); oc != nil {
		t.Errorf("unset effort should send no output_config, got %+v", oc)
	}

	// #f: off. Integer: manual budget for older models. #t: adaptive.
	chat.Env.Set("default-model", eval.Dictionary{"thinking": false})
	chat.Config()
	if th := chat.thinkingConfig(); th != nil {
		t.Errorf(":thinking #f should omit the field, got %+v", th)
	}
	chat.Env.Set("default-model", eval.Dictionary{"thinking": eval.Integer(12000)})
	chat.Config()
	if th := chat.thinkingConfig(); th == nil || th.Type != "enabled" || th.BudgetTokens != 12000 {
		t.Errorf("integer thinking = %+v, want enabled/12000", th)
	}
	chat.Env.Set("default-model", eval.Dictionary{"thinking": true, "effort": eval.String("medium")})
	chat.Config()
	if th := chat.thinkingConfig(); th == nil || th.Type != "adaptive" {
		t.Errorf(":thinking #t = %+v, want adaptive", th)
	}
	if oc := chat.outputConfig(); oc == nil || oc.Effort != "medium" {
		t.Errorf("effort = %+v, want medium", oc)
	}

	ollama := newChat(gs, Config{Provider: models.ProviderOllama, Model: "fake"}, &fakeModel{})
	ollama.Env.Set("default-model", eval.Dictionary{"thinking": true})
	ollama.Config()
	if th := ollama.thinkingConfig(); th == nil || th.Type != "adaptive" {
		t.Errorf("explicit :thinking on a compat provider = %+v, want adaptive", th)
	}
}

func TestMessageAgenticLoop(t *testing.T) {
	db, err := sqlx.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	gs := core.NewGraphStore(db)

	fake := &fakeModel{responses: []*Response{
		toolCallResponse("toolu_1", "(+ 1412 923)"),
		textResponse("The answer is 2335."),
	}}
	chat := newChat(gs, Config{Provider: models.ProviderOllama, Model: "fake"}, fake)

	replies, err := chat.Message(context.Background(), "What is 1412+923?", nil, "")
	if err != nil {
		t.Fatalf("Message: %v", err)
	}

	if len(fake.calls) != 2 {
		t.Fatalf("expected 2 model calls (tool round + answer), got %d", len(fake.calls))
	}

	var toolMsg, textMsg *models.Message
	for _, msg := range replies {
		switch msg.Type {
		case models.MessageTypeTool:
			toolMsg = msg
		case models.MessageTypeText:
			textMsg = msg
		}
	}
	if toolMsg == nil || toolMsg.Code != "(+ 1412 923)" {
		t.Fatalf("missing/wrong tool message: %+v", toolMsg)
	}
	if !strings.Contains(toolMsg.Result, "2335") {
		t.Errorf("tool result should contain 2335, got %q", toolMsg.Result)
	}
	if toolMsg.ErrorMessage != "" {
		t.Errorf("tool call errored: %s", toolMsg.ErrorMessage)
	}
	if textMsg == nil || !strings.Contains(textMsg.Content, "2335") {
		t.Errorf("missing final text message: %+v", textMsg)
	}

	var sawToolUse, sawToolResult bool
	for _, msg := range fake.calls[1].Messages {
		for _, block := range msg.Content {
			switch block.Type {
			case "tool_use":
				if msg.Role == "assistant" && block.ID == "toolu_1" {
					sawToolUse = true
				}
			case "tool_result":
				if msg.Role == "user" && block.ToolUseID == "toolu_1" && strings.Contains(block.Content, "2335") {
					sawToolResult = true
				}
			}
		}
	}
	if !sawToolUse || !sawToolResult {
		t.Errorf("tool exchange should replay as structured blocks: toolUse=%v toolResult=%v", sawToolUse, sawToolResult)
	}

	// The first call carries the tool definitions and the system prompt
	first := fake.calls[0]
	if len(first.Tools) != 4 || first.Tools[0].Name != "scheme" {
		t.Errorf("expected scheme + file tools on the request, got %d tools", len(first.Tools))
	}
	if first.System == "" {
		t.Error("expected a system prompt on the request")
	}
}

func TestMessageLoopCap(t *testing.T) {
	db, err := sqlx.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	gs := core.NewGraphStore(db)

	always := &fakeModel{}
	for range maxToolIterations + 2 {
		always.responses = append(always.responses, toolCallResponse("t", "(+ 1 1)"))
	}
	chat := newChat(gs, Config{Provider: models.ProviderOllama, Model: "fake"}, always)
	if _, err := chat.Message(context.Background(), "loop forever", nil, ""); err != nil {
		t.Fatalf("Message: %v", err)
	}
	if len(always.calls) != maxToolIterations {
		t.Errorf("expected the loop to stop at %d iterations, got %d", maxToolIterations, len(always.calls))
	}
}

func TestMessageHistoryAlternates(t *testing.T) {
	db, err := sqlx.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	gs := core.NewGraphStore(db)

	fake := &fakeModel{responses: []*Response{textResponse("ok")}}
	chat := newChat(gs, Config{Provider: models.ProviderOllama, Model: "fake"}, fake)

	history := []*models.Message{
		{Role: models.MessageRoleUser, Type: models.MessageTypeText, Content: "count files"},
		{Role: models.MessageRoleAssistant, Type: models.MessageTypeTool, Code: "(ls)", Result: "(3 files)"},
		{Role: models.MessageRoleAssistant, Type: models.MessageTypeText, Content: "There are 3 files."},
	}
	if _, err := chat.Message(context.Background(), "thanks", history, ""); err != nil {
		t.Fatalf("Message: %v", err)
	}

	msgs := fake.calls[0].Messages
	if len(msgs) == 0 || msgs[0].Role != "user" {
		t.Fatalf("conversation must start with a user message: %+v", msgs)
	}
	for i := 1; i < len(msgs); i++ {
		if msgs[i].Role == msgs[i-1].Role {
			t.Fatalf("messages %d and %d share role %q — history must alternate", i-1, i, msgs[i].Role)
		}
	}
	if !strings.Contains(callText(fake.calls[0]), "(ls)") {
		t.Error("history tool exchange should be visible to the model")
	}
}

func TestMessageHistoryStructuredToolReplay(t *testing.T) {
	db, err := sqlx.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	gs := core.NewGraphStore(db)

	fake := &fakeModel{responses: []*Response{textResponse("ok")}}
	chat := newChat(gs, Config{Provider: models.ProviderOllama, Model: "fake"}, fake)

	history := []*models.Message{
		{Role: models.MessageRoleUser, Type: models.MessageTypeText, Content: "count files"},
		{Role: models.MessageRoleAssistant, Type: models.MessageTypeTool, FunctionName: "scheme",
			Code: "(ls)", Result: "(3 files)", ToolCallID: "toolu_abc"},
		{Role: models.MessageRoleAssistant, Type: models.MessageTypeTool, FunctionName: "read",
			Code: `{"path": "f.txt"}`, ErrorMessage: "no such file"}, // no persisted id
		{Role: models.MessageRoleAssistant, Type: models.MessageTypeText, Content: "There are 3 files."},
	}
	if _, err := chat.Message(context.Background(), "thanks", history, ""); err != nil {
		t.Fatalf("Message: %v", err)
	}

	msgs := fake.calls[0].Messages
	type pair struct{ use, result *ContentBlock }
	pairs := map[string]*pair{}
	for i := range msgs {
		for j := range msgs[i].Content {
			b := &msgs[i].Content[j]
			switch b.Type {
			case "tool_use":
				if msgs[i].Role != "assistant" {
					t.Errorf("tool_use in %s message", msgs[i].Role)
				}
				if pairs[b.ID] == nil {
					pairs[b.ID] = &pair{}
				}
				pairs[b.ID].use = b
			case "tool_result":
				if msgs[i].Role != "user" {
					t.Errorf("tool_result in %s message", msgs[i].Role)
				}
				if pairs[b.ToolUseID] == nil {
					pairs[b.ToolUseID] = &pair{}
				}
				pairs[b.ToolUseID].result = b
			}
		}
	}
	if len(pairs) != 2 {
		t.Fatalf("expected 2 tool exchanges, got %d", len(pairs))
	}
	for id, p := range pairs {
		if p.use == nil || p.result == nil {
			t.Fatalf("unpaired tool exchange for id %q", id)
		}
	}

	scheme := pairs["toolu_abc"]
	if scheme == nil || scheme.use.Name != "scheme" || !strings.Contains(string(scheme.use.Input), "(ls)") {
		t.Fatalf("scheme replay wrong: %+v", scheme)
	}
	if scheme.result.Content != "(3 files)" || scheme.result.IsError {
		t.Fatalf("scheme result wrong: %+v", scheme.result)
	}

	for id, p := range pairs {
		if id == "toolu_abc" {
			continue
		}
		// The read exchange: synthesized id, raw JSON input, error result.
		if p.use.Name != "read" || !strings.Contains(string(p.use.Input), "f.txt") {
			t.Fatalf("read replay wrong: %+v", p.use)
		}
		if !p.result.IsError || !strings.Contains(p.result.Content, "no such file") {
			t.Fatalf("read result wrong: %+v", p.result)
		}
	}

	if strings.Contains(callText(fake.calls[0]), "Tool call:") {
		t.Error("history must not contain flattened Tool call text")
	}
}

func TestMaxTokensBinding(t *testing.T) {
	db, err := sqlx.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	gs := core.NewGraphStore(db)

	fake := &fakeModel{responses: []*Response{textResponse("a"), textResponse("b"), textResponse("c")}}
	chat := newChat(gs, Config{Provider: models.ProviderOllama, Model: "fake"}, fake)

	if _, err := chat.Message(context.Background(), "hi", nil, ""); err != nil {
		t.Fatal(err)
	}
	if got := fake.calls[0].MaxTokens; got != DefaultMaxTokens {
		t.Fatalf("default max_tokens = %d, want %d", got, DefaultMaxTokens)
	}

	chat.Env.Set("default-model", eval.Dictionary{"max-tokens": eval.Number(64000)})
	if _, err := chat.Message(context.Background(), "hi", nil, ""); err != nil {
		t.Fatal(err)
	}
	if got := fake.calls[1].MaxTokens; got != 64000 {
		t.Fatalf("bound max_tokens = %d, want 64000", got)
	}

	// A mistyped value is an error on the next call, not silence.
	chat.Env.Set("default-model", eval.Dictionary{"max-tokens": eval.String("lots")})
	if _, err := chat.Message(context.Background(), "hi", nil, ""); err == nil || !strings.Contains(err.Error(), ":max-tokens") {
		t.Fatalf("mistyped :max-tokens should fail the call, got %v", err)
	}
}

func TestMessageSteering(t *testing.T) {
	db, err := sqlx.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	gs := core.NewGraphStore(db)

	fake := &fakeModel{responses: []*Response{
		textResponse("First answer."),
		textResponse("Steered answer."),
	}}
	chat := newChat(gs, Config{Provider: models.ProviderOllama, Model: "fake"}, fake)

	steered := false
	chat.OnDelta = func(DeltaKind, string) {
		if !steered {
			steered = true
			chat.Steer("actually, shorter please")
		}
	}
	var steerEvents []string
	chat.OnEvent = func(e Event, msg *models.Message) {
		if e == EventSteer {
			steerEvents = append(steerEvents, msg.Content)
		}
	}

	replies, err := chat.Message(context.Background(), "explain monads", nil, "")
	if err != nil {
		t.Fatalf("Message: %v", err)
	}

	if len(fake.calls) != 2 {
		t.Fatalf("expected 2 model calls (answer + steered round), got %d", len(fake.calls))
	}
	msgs := fake.calls[1].Messages
	last := msgs[len(msgs)-1]
	if last.Role != "user" || !strings.Contains(callText(fake.calls[1]), "actually, shorter please") {
		t.Errorf("steer should be the trailing user message: %+v", msgs)
	}
	for i := 1; i < len(msgs); i++ {
		if msgs[i].Role == msgs[i-1].Role {
			t.Fatalf("messages %d and %d share role %q", i-1, i, msgs[i].Role)
		}
	}

	if len(steerEvents) != 1 || steerEvents[0] != "actually, shorter please" {
		t.Errorf("EventSteer = %v", steerEvents)
	}

	// The transcript interleaves: assistant, steered user, assistant.
	var kinds []string
	for _, msg := range replies {
		kinds = append(kinds, msg.Role+"/"+msg.Type)
	}
	want := []string{"assistant/text", "user/text", "assistant/text"}
	if strings.Join(kinds, ",") != strings.Join(want, ",") {
		t.Errorf("transcript order = %v, want %v", kinds, want)
	}
}

func TestMessageSteerResetsBudget(t *testing.T) {
	db, err := sqlx.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	gs := core.NewGraphStore(db)

	fake := &fakeModel{}
	for range maxToolIterations - 1 {
		fake.responses = append(fake.responses, toolCallResponse("t", "(+ 1 1)"))
	}
	fake.responses = append(fake.responses, textResponse("first done"))
	for range maxToolIterations - 1 {
		fake.responses = append(fake.responses, toolCallResponse("t", "(+ 2 2)"))
	}
	fake.responses = append(fake.responses, textResponse("second done"))

	chat := newChat(gs, Config{Provider: models.ProviderOllama, Model: "fake"}, fake)
	// Steer once, right after the first text answer.
	chat.OnEvent = func(e Event, msg *models.Message) {
		if e == EventText && msg.Content == "first done" {
			chat.Steer("keep going")
		}
	}

	if _, err := chat.Message(context.Background(), "work", nil, ""); err != nil {
		t.Fatalf("Message: %v", err)
	}
	want := 2 * maxToolIterations
	if len(fake.calls) != want {
		t.Errorf("expected %d model calls across the steered turn, got %d", want, len(fake.calls))
	}
}

func TestMessageEventsAndApproval(t *testing.T) {
	db, err := sqlx.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	gs := core.NewGraphStore(db)

	dir := t.TempDir()
	target := dir + "/doomed.txt"
	if err := os.WriteFile(target, []byte("x"), 0644); err != nil {
		t.Fatal(err)
	}

	fake := &fakeModel{responses: []*Response{
		toolCallResponse("toolu_rm", `(rm "`+target+`")`),
		textResponse("Removed it."),
	}}
	chat := newChat(gs, Config{Provider: models.ProviderOllama, Model: "fake"}, fake)

	var events []Event
	var asked []string
	var streamed strings.Builder
	chat.OnDelta = func(kind DeltaKind, delta string) {
		if kind == DeltaText {
			streamed.WriteString(delta)
		}
	}
	chat.OnEvent = func(e Event, msg *models.Message) {
		events = append(events, e)
		if e == EventToolResult && msg.ErrorMessage == "" {
			t.Error("denied rm should surface an error on the tool result")
		}
		if e == EventText && streamed.String() != msg.Content {
			t.Errorf("deltas should equal the completed text: %q vs %q", streamed.String(), msg.Content)
		}
	}
	chat.Approve = func(action string) bool {
		asked = append(asked, action)
		return false
	}

	if _, err := chat.Message(context.Background(), "delete doomed.txt", nil, ""); err != nil {
		t.Fatalf("Message: %v", err)
	}

	want := []Event{EventToolCall, EventToolResult, EventText}
	if len(events) != len(want) {
		t.Fatalf("expected events %v, got %v", want, events)
	}
	for i := range want {
		if events[i] != want[i] {
			t.Fatalf("expected events %v, got %v", want, events)
		}
	}
	if len(asked) != 1 || !strings.Contains(asked[0], "rm") {
		t.Errorf("approver saw wrong actions: %v", asked)
	}
	if _, err := os.Stat(target); err != nil {
		t.Error("denied rm must not delete the file")
	}
	if streamed.String() != "Removed it." {
		t.Errorf("expected streamed deltas, got %q", streamed.String())
	}

	sawDenial := false
	for _, msg := range fake.calls[1].Messages {
		for _, block := range msg.Content {
			if block.Type == "tool_result" && block.IsError && strings.Contains(block.Content, "denied") {
				sawDenial = true
			}
		}
	}
	if !sawDenial {
		t.Error("denial should be fed back to the model as an error tool result")
	}

	// The user's own evaluator path stays ungated between tool calls
	if _, err := chat.Eval.EvalAll(mustParse(t, `(rm "`+target+`")`), chat.Env); err != nil {
		t.Errorf("user-path rm should not be gated: %v", err)
	}
}

func mustParse(t *testing.T, code string) []eval.Value {
	t.Helper()
	exprs, err := eval.ParseAll(code)
	if err != nil {
		t.Fatal(err)
	}
	return exprs
}

func TestMessage_Arithmetic(t *testing.T) {
	if os.Getenv("RF_LIVE_LLM") == "" {
		t.Skip("set RF_LIVE_LLM=1 to test against live Ollama")
	}
	// Create a simple test database
	db, err := sqlx.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("Failed to create test database: %v", err)
	}
	defer db.Close()

	// Create GraphStore
	gs := core.NewGraphStore(db)

	chat, err := NewChat(gs)
	if err != nil {
		t.Skip("Ollama not available, skipping test")
	}

	// Quick check if Ollama is actually responding
	ctx := context.Background()
	_, err = chat.Model().Messages(ctx, &Request{
		Model:     chat.Config().Model,
		MaxTokens: 64,
		Messages:  []Message{TextMessage("user", "test")},
	}, nil)
	if err != nil {
		t.Skipf("Ollama not responding, skipping test: %v", err)
	}

	// Test a simple arithmetic question
	question := "What is 1412+923?"
	messages, err := chat.Message(ctx, question, nil, "") // No history for test
	if err != nil {
		t.Fatalf("Message failed: %v", err)
	}

	// Check that we got some messages
	if len(messages) == 0 {
		t.Error("Expected at least one message")
	}

	// Execute tool calls (updates messages in-place)
	err = chat.ExecuteToolCalls(ctx, messages)
	if err != nil {
		t.Fatalf("ExecuteToolCalls failed: %v", err)
	}

	// Check that we got tool calls for the arithmetic
	var textContent string
	var toolMessages []*models.Message
	for _, msg := range messages {
		switch msg.Type {
		case models.MessageTypeText:
			textContent = msg.Content
		case models.MessageTypeTool:
			toolMessages = append(toolMessages, msg)
		}
	}

	if len(toolMessages) == 0 {
		t.Error("Expected at least one tool call for arithmetic evaluation")
	}

	// Check that the tool call contains the expected Scheme code
	foundArithmetic := false
	for _, msg := range toolMessages {
		if msg.ErrorMessage != "" {
			t.Errorf("Tool call failed: %s", msg.ErrorMessage)
			continue
		}

		// Check if the code contains the expected arithmetic expression
		if strings.Contains(msg.Code, "(+ 1412 923)") {
			foundArithmetic = true
			// The result should be 2335
			if !strings.Contains(msg.Result, "2335") {
				t.Errorf("Expected result to contain 2335, got: %s", msg.Result)
			}
		}
	}

	if !foundArithmetic {
		t.Error("Expected to find arithmetic tool call with (+ 1412 923)")
	}

	t.Logf("Text content: %s", textContent)
	for i, msg := range toolMessages {
		t.Logf("Tool call %d - Code: %s, Result: %s", i, msg.Code, msg.Result)
	}
}

func TestCacheBreakpointMarksLastBlock(t *testing.T) {
	fake := &fakeModel{responses: []*Response{
		toolCallResponse("toolu_1", "(+ 1 2)"),
		textResponse("3"),
	}}
	chat := usageChat(t, "claude-sonnet-4-6", fake)

	if _, err := chat.Message(context.Background(), "add", nil, ""); err != nil {
		t.Fatalf("Message: %v", err)
	}
	if len(fake.calls) != 2 {
		t.Fatalf("expected 2 calls, got %d", len(fake.calls))
	}

	last := fake.calls[len(fake.calls)-1]
	markers := 0
	for _, msg := range last.Messages {
		for _, block := range msg.Content {
			if block.CacheControl != nil {
				markers++
			}
		}
	}
	if markers != 1 {
		t.Fatalf("expected exactly 1 cache_control marker, got %d", markers)
	}
	lastMsg := last.Messages[len(last.Messages)-1]
	lastBlock := lastMsg.Content[len(lastMsg.Content)-1]
	if lastBlock.CacheControl == nil || lastBlock.CacheControl.Type != "ephemeral" {
		t.Errorf("last block cache_control = %+v, want ephemeral", lastBlock.CacheControl)
	}
	if lastBlock.Type != "tool_result" {
		t.Errorf("marker should sit on the tool_result block, got %q", lastBlock.Type)
	}
}

func TestMarkCacheBreakpoint(t *testing.T) {
	messages := []Message{
		{Role: "user", Content: []ContentBlock{
			{Type: "text", Text: "a", CacheControl: &CacheControl{Type: "ephemeral"}},
		}},
		{Role: "assistant", Content: []ContentBlock{{Type: "text", Text: "b"}}},
		{Role: "user", Content: []ContentBlock{
			{Type: "text", Text: "c"},
			{Type: "text", Text: "d"},
		}},
	}
	markCacheBreakpoint(messages)
	if messages[0].Content[0].CacheControl != nil {
		t.Error("stale marker not cleared")
	}
	if messages[2].Content[0].CacheControl != nil {
		t.Error("non-final block should not be marked")
	}
	if cc := messages[2].Content[1].CacheControl; cc == nil || cc.Type != "ephemeral" {
		t.Errorf("final block marker = %+v", cc)
	}

	markCacheBreakpoint(nil)
	markCacheBreakpoint([]Message{{Role: "user"}})
}
