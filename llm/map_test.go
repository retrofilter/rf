package llm

import (
	"strings"
	"testing"

	"github.com/retrofilter/rf/eval"
)

func evalIn(t *testing.T, chat *Chat, code string) (eval.Value, error) {
	t.Helper()
	exprs, err := eval.ParseAll(code)
	if err != nil {
		t.Fatalf("parse %q: %v", code, err)
	}
	return chat.Eval.EvalAll(exprs, chat.Env)
}

func rowsOf(t *testing.T, v eval.Value) []eval.Dictionary {
	t.Helper()
	lst, ok, err := eval.AsList(v)
	if err != nil || !ok {
		t.Fatalf("expected a list, got %s (%v)", eval.PrintValue(v), err)
	}
	rows := make([]eval.Dictionary, len(lst))
	for i, item := range lst {
		d, ok := item.(eval.Dictionary)
		if !ok {
			t.Fatalf("expected rows, item %d is %s", i, eval.PrintValue(item))
		}
		rows[i] = d
	}
	return rows
}

func TestLLMMapBatch(t *testing.T) {
	chat, fake := newFakeChat(t, textResponse("1. alpha\n2. beta"))

	v, err := evalIn(t, chat, `(llm-map "describe" (list "one" "two"))`)
	if err != nil {
		t.Fatalf("llm-map: %v", err)
	}
	rows := rowsOf(t, v)
	if len(rows) != 2 {
		t.Fatalf("expected 2 rows, got %d", len(rows))
	}
	if rows[0]["item"] != eval.String("one") || rows[0]["llm"] != eval.String("alpha") {
		t.Errorf("row 0 = %s", eval.PrintValue(rows[0]))
	}
	if rows[1]["llm"] != eval.String("beta") {
		t.Errorf("row 1 = %s", eval.PrintValue(rows[1]))
	}

	if len(fake.calls) != 1 {
		t.Fatalf("expected 1 batched call, got %d", len(fake.calls))
	}
	req := fake.calls[0]
	if req.System != mapSystemPrompt {
		t.Errorf("system prompt: %q", req.System)
	}
	body := callText(req)
	if !strings.Contains(body, "describe") || !strings.Contains(body, "1. one") || !strings.Contains(body, "2. two") {
		t.Errorf("request missing instruction or numbered items: %q", body)
	}
}

func TestLLMMapRows(t *testing.T) {
	chat, fake := newFakeChat(t, textResponse("1. yes"))

	v, err := evalIn(t, chat,
		`(llm-map "big?" (list {:name "a.go" :size 10}) {:key "name" :column "verdict"})`)
	if err != nil {
		t.Fatalf("llm-map: %v", err)
	}
	rows := rowsOf(t, v)
	if rows[0]["verdict"] != eval.String("yes") || rows[0]["name"] != eval.String("a.go") {
		t.Errorf("row = %s", eval.PrintValue(rows[0]))
	}
	if body := callText(fake.calls[0]); !strings.Contains(body, "1. a.go") || strings.Contains(body, "10") {
		t.Errorf(":key should send only the name column: %q", body)
	}
}

func TestLLMMapFallback(t *testing.T) {
	chat, fake := newFakeChat(t,
		textResponse("here you go: alpha and beta"), // unparseable batch reply
		textResponse("alpha"),
		textResponse("beta"),
	)

	v, err := evalIn(t, chat, `(llm-map "describe" (list "one" "two"))`)
	if err != nil {
		t.Fatalf("llm-map: %v", err)
	}
	rows := rowsOf(t, v)
	if rows[0]["llm"] != eval.String("alpha") || rows[1]["llm"] != eval.String("beta") {
		t.Errorf("fallback rows = %s", eval.PrintValue(v))
	}
	if len(fake.calls) != 3 {
		t.Fatalf("expected batch + 2 fallback calls, got %d", len(fake.calls))
	}
	if fake.calls[1].System != mapItemSystemPrompt {
		t.Errorf("fallback system prompt: %q", fake.calls[1].System)
	}
}

func TestClassify(t *testing.T) {
	chat, fake := newFakeChat(t, textResponse("1. fruit\n2. tool"))

	v, err := evalIn(t, chat, `(classify "fruit or tool?" (list "apple" "hammer"))`)
	if err != nil {
		t.Fatalf("classify: %v", err)
	}
	rows := rowsOf(t, v)
	if rows[0]["label"] != eval.String("fruit") || rows[1]["label"] != eval.String("tool") {
		t.Errorf("rows = %s", eval.PrintValue(v))
	}
	if fake.calls[0].System != classifySystemPrompt {
		t.Errorf("system prompt: %q", fake.calls[0].System)
	}
}

func TestLLMMapCostGuard(t *testing.T) {
	chat, fake := newFakeChat(t, textResponse("1. a\n2. b\n3. c"))

	if _, err := evalIn(t, chat, `(llm-map "x" (list "a" "b" "c") {:limit 2})`); err == nil || !strings.Contains(err.Error(), "cost guard") {
		t.Fatalf("expected cost-guard error, got %v", err)
	}
	if len(fake.calls) != 0 {
		t.Fatalf("cost guard must fire before any completion, got %d calls", len(fake.calls))
	}
	if _, err := evalIn(t, chat, `(llm-map "x" (list "a" "b" "c") {:limit 3})`); err != nil {
		t.Fatalf("raised :limit should pass: %v", err)
	}
}

func TestLLMMapBatchSize(t *testing.T) {
	chat, fake := newFakeChat(t,
		textResponse("1. a\n2. b"),
		textResponse("1. c"),
	)

	v, err := evalIn(t, chat, `(llm-map "x" (list "p" "q" "r") {:batch 2})`)
	if err != nil {
		t.Fatalf("llm-map: %v", err)
	}
	rows := rowsOf(t, v)
	if rows[2]["llm"] != eval.String("c") {
		t.Errorf("rows = %s", eval.PrintValue(v))
	}
	if len(fake.calls) != 2 {
		t.Fatalf("expected 2 batch calls, got %d", len(fake.calls))
	}
}

func TestLLMMapErrors(t *testing.T) {
	chat, fake := newFakeChat(t)

	for code, want := range map[string]string{
		`(llm-map "x")`:                       "expects an input",
		`(llm-map "x" (list "a") (list "b"))`: "quote the instruction",
		`(llm-map "x" (list "a") :nope 1)`:    "unknown option",
		`(llm-map 42 (list "a"))`:             "instruction must be a string",
	} {
		if _, err := evalIn(t, chat, code); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: expected %q error, got %v", code, want, err)
		}
	}

	v, err := evalIn(t, chat, `(llm-map "x" (list))`)
	if err != nil {
		t.Fatalf("empty input: %v", err)
	}
	if lst, _, _ := eval.AsList(v); len(lst) != 0 {
		t.Errorf("empty input should map to no rows, got %s", eval.PrintValue(v))
	}
	if len(fake.calls) != 0 {
		t.Errorf("no completions expected, got %d", len(fake.calls))
	}
}

func TestParseNumbered(t *testing.T) {
	if got, ok := parseNumbered("1. a\n2) b\n3: c", 3); !ok || got[1] != "b" || got[2] != "c" {
		t.Errorf("separators: %v %v", got, ok)
	}
	for reply, n := range map[string]int{
		"1. a":       2, // missing item
		"1. a\n1. b": 1, // duplicate
		"5. a":       1, // out of range
		"whatever":   1, // no numbers at all
	} {
		if _, ok := parseNumbered(reply, n); ok {
			t.Errorf("parseNumbered(%q, %d) should fail", reply, n)
		}
	}
}
