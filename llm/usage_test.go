package llm

import (
	"context"
	"math"
	"testing"

	"github.com/jmoiron/sqlx"
	"github.com/retrofilter/rf/core"
	"github.com/retrofilter/rf/eval"
	"github.com/retrofilter/rf/models"
	_ "modernc.org/sqlite"
)

func usageChat(t *testing.T, model string, fake *fakeModel) *Chat {
	t.Helper()
	db, err := sqlx.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return newChat(core.NewGraphStore(db), Config{Provider: models.ProviderAnthropic, Model: model}, fake)
}

func withUsage(resp *Response, in, out int) *Response {
	resp.Usage = Usage{InputTokens: in, OutputTokens: out}
	return resp
}

func TestUsageAccumulation(t *testing.T) {
	fake := &fakeModel{responses: []*Response{
		withUsage(toolCallResponse("toolu_1", "(+ 1 2)"), 1000, 50),
		withUsage(textResponse("3"), 2000, 100),
	}}
	chat := usageChat(t, "claude-sonnet-4-6", fake)

	if _, err := chat.Message(context.Background(), "add", nil, ""); err != nil {
		t.Fatalf("Message: %v", err)
	}

	totals, contextTokens := chat.Usage()
	if totals.InputTokens != 3000 || totals.OutputTokens != 150 {
		t.Errorf("totals = %+v, want 3000 in / 150 out", totals)
	}
	// Sonnet: $3/MTok in, $15/MTok out
	want := (3000*3.0 + 150*15.0) / 1e6
	if math.Abs(totals.Cost-want) > 1e-9 {
		t.Errorf("cost = %v, want %v", totals.Cost, want)
	}
	if contextTokens != 2100 {
		t.Errorf("contextTokens = %d, want 2100 (last call only)", contextTokens)
	}
}

func TestResetContext(t *testing.T) {
	fake := &fakeModel{responses: []*Response{withUsage(textResponse("hi"), 1000, 50)}}
	chat := usageChat(t, "claude-sonnet-4-6", fake)

	if _, err := chat.Message(context.Background(), "hello", nil, ""); err != nil {
		t.Fatalf("Message: %v", err)
	}

	chat.ResetContext()
	totals, contextTokens := chat.Usage()
	if contextTokens != 0 {
		t.Errorf("contextTokens = %d, want 0 after ResetContext", contextTokens)
	}
	if totals.InputTokens != 1000 || totals.OutputTokens != 50 {
		t.Errorf("totals = %+v, want them untouched by ResetContext", totals)
	}
}

func TestUsageEstimatedWhenMissing(t *testing.T) {
	fake := &fakeModel{responses: []*Response{textResponse("a reply long enough to estimate")}}
	chat := usageChat(t, "granite4:3b", fake)

	if _, err := chat.Message(context.Background(), "hello there, model", nil, ""); err != nil {
		t.Fatalf("Message: %v", err)
	}

	totals, _ := chat.Usage()
	if totals.InputTokens == 0 || totals.OutputTokens == 0 {
		t.Errorf("expected estimated tokens, got %+v", totals)
	}
	// The system prompt alone is thousands of chars; the reply is 31 chars.
	if totals.OutputTokens != len("a reply long enough to estimate")/4 {
		t.Errorf("output = %d, want %d", totals.OutputTokens, len("a reply long enough to estimate")/4)
	}
	if totals.Cost != 0 {
		t.Errorf("Ollama model should cost 0, got %v", totals.Cost)
	}
}

func TestLLMBuiltinCountsUsage(t *testing.T) {
	fake := &fakeModel{responses: []*Response{withUsage(textResponse("four"), 500, 10)}}
	chat := usageChat(t, "claude-sonnet-4-6", fake)

	expr, err := eval.Parse(`(llm "2+2?")`)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := chat.Eval.Eval(expr, chat.Env); err != nil {
		t.Fatalf("(llm): %v", err)
	}

	totals, contextTokens := chat.Usage()
	if totals.InputTokens != 500 || totals.OutputTokens != 10 {
		t.Errorf("totals = %+v, want 500/10", totals)
	}
	if contextTokens != 0 {
		t.Errorf("contextTokens = %d, want 0 (llm builtin is not a turn)", contextTokens)
	}
}

func TestContextWindow(t *testing.T) {
	if w := ContextWindow("claude-sonnet-4-6"); w != 1000000 {
		t.Errorf("sonnet 4.6 window = %d, want 1000000", w)
	}
	if w := ContextWindow("claude-haiku-4-5-20251001"); w != 200000 {
		t.Errorf("haiku window = %d, want 200000", w)
	}
	if w := ContextWindow("granite4:3b"); w != DefaultContextWindow {
		t.Errorf("unknown-model window = %d, want %d", w, DefaultContextWindow)
	}
	if DefaultContextWindow-CompactReserveTokens <= DefaultKeepRecentTokens {
		t.Errorf("default window %d leaves no room past reserve %d + keep budget %d",
			DefaultContextWindow, CompactReserveTokens, DefaultKeepRecentTokens)
	}
}

func TestNeedsCompaction(t *testing.T) {
	chat := &Chat{config: Config{Model: "claude-haiku-4-5-20251001"}}
	if chat.NeedsCompaction() {
		t.Error("fresh chat must not need compaction")
	}
	chat.contextTokens = 200000 - CompactReserveTokens
	if chat.NeedsCompaction() {
		t.Error("context exactly at threshold must not trigger")
	}
	chat.contextTokens = 200000 - CompactReserveTokens + 1
	if !chat.NeedsCompaction() {
		t.Error("context past threshold must trigger")
	}
}

func TestUsageBuiltin(t *testing.T) {
	fake := &fakeModel{responses: []*Response{withUsage(textResponse("hi"), 1200, 34)}}
	chat := usageChat(t, "claude-sonnet-4-6", fake)

	if _, err := chat.Message(context.Background(), "hi", nil, ""); err != nil {
		t.Fatal(err)
	}

	expr, err := eval.Parse(`(usage)`)
	if err != nil {
		t.Fatal(err)
	}
	result, err := chat.Eval.Eval(expr, chat.Env)
	if err != nil {
		t.Fatalf("(usage): %v", err)
	}
	dict, ok := result.(eval.Dictionary)
	if !ok {
		t.Fatalf("expected dictionary, got %T", result)
	}
	if dict["input"] != eval.Number(1200) || dict["output"] != eval.Number(34) {
		t.Errorf("dict = %v", dict)
	}
	if dict["context"] != eval.Number(1234) {
		t.Errorf("context = %v, want 1234", dict["context"])
	}
	if dict["cost"] == eval.Number(0) {
		t.Errorf("expected non-zero cost for sonnet")
	}
}

func TestUsageLine(t *testing.T) {
	chat := usageChat(t, "claude-sonnet-4-6", &fakeModel{})
	if line := chat.UsageLine(); line != "" {
		t.Errorf("empty session should have no usage line, got %q", line)
	}

	chat.usage = UsageTotals{InputTokens: 12345, OutputTokens: 3210, Cost: 0.0842}
	if got, want := chat.UsageLine(), "↑12k ↓3.2k $0.08"; got != want {
		t.Errorf("UsageLine = %q, want %q", got, want)
	}

	chat.usage = UsageTotals{InputTokens: 900, OutputTokens: 42}
	if got, want := chat.UsageLine(), "↑900 ↓42"; got != want {
		t.Errorf("UsageLine = %q, want %q", got, want)
	}

	chat.usage = UsageTotals{InputTokens: 2000, OutputTokens: 100, Cost: 0.0012}
	if got, want := chat.UsageLine(), "↑2k ↓100 $0.0012"; got != want {
		t.Errorf("UsageLine = %q, want %q", got, want)
	}
}

func TestCostUSD(t *testing.T) {
	u := Usage{InputTokens: 1_000_000, OutputTokens: 1_000_000}
	cases := []struct {
		model string
		want  float64
	}{
		{"claude-sonnet-4-6", 18},
		{"claude-opus-4-8", 30},
		{"claude-opus-4-1", 90}, // legacy pricing, must win over the claude-opus entry
		{"claude-haiku-4-5-20251001", 6},
		{"claude-3-5-haiku-20241022", 4.8}, // legacy pricing, must win over claude-haiku
		{"claude-fable-5", 60},
		{"granite4:3b", 0},
	}
	for _, tc := range cases {
		if got := costUSD(tc.model, u); math.Abs(got-tc.want) > 1e-9 {
			t.Errorf("costUSD(%s) = %v, want %v", tc.model, got, tc.want)
		}
	}
}

func TestCostUSDCached(t *testing.T) {
	u := Usage{
		InputTokens:              1_000_000,
		CacheCreationInputTokens: 1_000_000,
		CacheReadInputTokens:     1_000_000,
		OutputTokens:             1_000_000,
	}
	// Sonnet: in $3, out $15 → 3 + 3*1.25 + 3*0.1 + 15 = 22.05
	if got, want := costUSD("claude-sonnet-4-6", u), 22.05; math.Abs(got-want) > 1e-9 {
		t.Errorf("costUSD = %v, want %v", got, want)
	}
}

func TestUsageCacheAccumulation(t *testing.T) {
	resp := textResponse("hi")
	resp.Usage = Usage{
		InputTokens:              200,
		OutputTokens:             50,
		CacheCreationInputTokens: 1000,
		CacheReadInputTokens:     8000,
	}
	fake := &fakeModel{responses: []*Response{resp}}
	chat := usageChat(t, "claude-sonnet-4-6", fake)

	if _, err := chat.Message(context.Background(), "hi", nil, ""); err != nil {
		t.Fatalf("Message: %v", err)
	}

	totals, contextTokens := chat.Usage()
	if totals.InputTokens != 9200 || totals.OutputTokens != 50 {
		t.Errorf("totals = %+v, want 9200 in / 50 out", totals)
	}
	if totals.CacheReadTokens != 8000 || totals.CacheWriteTokens != 1000 {
		t.Errorf("cache totals = %+v, want 8000 read / 1000 write", totals)
	}
	if contextTokens != 9250 {
		t.Errorf("contextTokens = %d, want 9250", contextTokens)
	}
	// Sonnet: (200 + 1000*1.25 + 8000*0.1)*3 + 50*15
	want := (200*3.0 + 1000*1.25*3.0 + 8000*0.1*3.0 + 50*15.0) / 1e6
	if math.Abs(totals.Cost-want) > 1e-9 {
		t.Errorf("cost = %v, want %v", totals.Cost, want)
	}

	expr, err := eval.Parse(`(usage)`)
	if err != nil {
		t.Fatal(err)
	}
	result, err := chat.Eval.Eval(expr, chat.Env)
	if err != nil {
		t.Fatalf("(usage): %v", err)
	}
	dict := result.(eval.Dictionary)
	if dict["cache-read"] != eval.Number(8000) || dict["cache-write"] != eval.Number(1000) {
		t.Errorf("(usage) cache fields = %v", dict)
	}
}
