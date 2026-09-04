package llm

import (
	"slices"
	"strings"
	"testing"

	"github.com/retrofilter/rf/eval"
)

func TestRerankSingleWindow(t *testing.T) {
	chat, fake := newFakeChat(t, textResponse("[3] > [1] > [2]"))

	v, err := evalIn(t, chat,
		`(rerank "two sum" (list {:id "a" :content "alpha"} {:id "b" :content "beta"} {:id "c" :content "gamma"}) {:key "content"})`)
	if err != nil {
		t.Fatalf("rerank: %v", err)
	}
	rows := rowsOf(t, v)
	if len(rows) != 3 {
		t.Fatalf("expected 3 rows, got %d", len(rows))
	}
	gotIDs := []string{string(rows[0]["id"].(eval.String)), string(rows[1]["id"].(eval.String)), string(rows[2]["id"].(eval.String))}
	if !slices.Equal(gotIDs, []string{"c", "a", "b"}) {
		t.Errorf("order = %v, want [c a b]", gotIDs)
	}
	for i, row := range rows {
		if row["rank"] != eval.Integer(i+1) {
			t.Errorf("row %d rank = %s", i, eval.PrintValue(row["rank"]))
		}
	}

	if len(fake.calls) != 1 {
		t.Fatalf("expected 1 call, got %d", len(fake.calls))
	}
	req := fake.calls[0]
	if req.System != rerankSystemPrompt {
		t.Errorf("system prompt: %q", req.System)
	}
	body := callText(req)
	for _, want := range []string{"[1] alpha", "[2] beta", "[3] gamma", "Search Query: two sum", "[4] > [2]"} {
		if !strings.Contains(body, want) {
			t.Errorf("request missing %q: %q", want, body)
		}
	}
}

func TestRerankSlidingWindows(t *testing.T) {
	chat, fake := newFakeChat(t,
		// Window over items 3..6: last item (f, [4]) to the front.
		textResponse("[4] > [1] > [2] > [3]"),
		// Window over items 1..4 (now a b f c): f is [3], promote it.
		textResponse("[3] > [1] > [2] > [4]"),
		// Final top-2 window (f a): keep order.
		textResponse("[1] > [2]"),
	)

	v, err := evalIn(t, chat,
		`(rerank "q" (list "a" "b" "c" "d" "e" "f") {:window 4 :stride 2})`)
	if err != nil {
		t.Fatalf("rerank: %v", err)
	}
	rows := rowsOf(t, v)
	var got []string
	for _, row := range rows {
		got = append(got, string(row["text"].(eval.String)))
	}
	if !slices.Equal(got, []string{"f", "a", "b", "c", "d", "e"}) {
		t.Errorf("order = %v", got)
	}
	if len(fake.calls) != 3 {
		t.Fatalf("expected 3 window calls, got %d", len(fake.calls))
	}
	// First call sees the bottom window, passages c d e f.
	body := callText(fake.calls[0])
	for _, want := range []string{"[1] c", "[2] d", "[3] e", "[4] f"} {
		if !strings.Contains(body, want) {
			t.Errorf("first window missing %q: %q", want, body)
		}
	}
}

func TestRerankUnparseableReply(t *testing.T) {
	chat, _ := newFakeChat(t, textResponse("I cannot rank these passages."))

	v, err := evalIn(t, chat, `(rerank "q" (list "a" "b" "c"))`)
	if err != nil {
		t.Fatalf("rerank: %v", err)
	}
	rows := rowsOf(t, v)
	var got []string
	for _, row := range rows {
		got = append(got, string(row["text"].(eval.String)))
	}
	if !slices.Equal(got, []string{"a", "b", "c"}) {
		t.Errorf("order = %v, want original", got)
	}
}

func TestRerankModelOverride(t *testing.T) {
	chat, fake := newFakeChat(t, textResponse("[1] > [2]"), textResponse("[1] > [2]"))

	if _, err := evalIn(t, chat, `(rerank "q" (list "a" "b") {:model "claude-haiku-4-5"})`); err != nil {
		t.Fatalf("rerank: %v", err)
	}
	if got := fake.calls[0].Model; got != "claude-haiku-4-5" {
		t.Errorf("model = %q, want claude-haiku-4-5", got)
	}
	if _, err := evalIn(t, chat, `(rerank "q" (list "a" "b"))`); err != nil {
		t.Fatalf("rerank: %v", err)
	}
	if got := fake.calls[1].Model; got != "fake" {
		t.Errorf("model = %q, want session model", got)
	}
}

func TestRerankCostGuard(t *testing.T) {
	chat, fake := newFakeChat(t)

	_, err := evalIn(t, chat, `(rerank "q" (make-list 101 "same passage"))`)
	if err == nil || !strings.Contains(err.Error(), ":limit") {
		t.Fatalf("expected cost-guard error naming :limit, got %v", err)
	}
	if len(fake.calls) != 0 {
		t.Errorf("expected no calls, got %d", len(fake.calls))
	}
}

func TestRerankPermutation(t *testing.T) {
	cases := []struct {
		reply string
		n     int
		want  []int
	}{
		{"[3] > [1] > [2]", 3, []int{2, 0, 1}},
		{"[2] > [2] > [1]", 3, []int{1, 0, 2}},
		{"[9] > [2]", 3, []int{1, 0, 2}},
		{"no brackets at all", 3, []int{0, 1, 2}},
		{"<think>[3] is best</think>[2] > [3] > [1]", 3, []int{1, 2, 0}},
	}
	for _, c := range cases {
		if got := rerankPermutation(c.reply, c.n); !slices.Equal(got, c.want) {
			t.Errorf("rerankPermutation(%q, %d) = %v, want %v", c.reply, c.n, got, c.want)
		}
	}
}
