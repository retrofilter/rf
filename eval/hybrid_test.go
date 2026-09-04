package eval

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func hybridFake() *fakeEncoder {
	return &fakeEncoder{vectors: map[string][]float32{
		"machine learning":        {1, 0, 0, 0},
		"deep neural networks":    {0.95, 0.05, 0, 0},
		"machine learning basics": {0.5, 0.5, 0, 0},
		"grocery shopping list":   {0, 1, 0, 0},
	}}
}

const hybridCorpus = `(list "deep neural networks" "machine learning basics" "grocery shopping list")`

func hybridOrder(t *testing.T, expr string) []string {
	t.Helper()
	installFakeEncoder(t, hybridFake())
	got := evalSimilar(t, expr)
	rows, ok := got.([]Value)
	require.True(t, ok, "expected a list of rows, got %T", got)
	var texts []string
	for _, r := range rows {
		texts = append(texts, string(r.(Dictionary)["text"].(String)))
	}
	return texts
}

func TestHybridAlphaExtremes(t *testing.T) {
	// alpha 1 is the semantic order: everything ranked, neural nets first.
	require.Equal(t, []string{
		"deep neural networks",
		"machine learning basics",
		"grocery shopping list",
	}, hybridOrder(t, `(hybrid "machine learning" `+hybridCorpus+` {:alpha 1})`))

	// alpha 0 is the bm25 behavior: only keyword overlap survives.
	require.Equal(t, []string{
		"machine learning basics",
	}, hybridOrder(t, `(hybrid "machine learning" `+hybridCorpus+` {:alpha 0})`))
}

func TestHybridFusesAtDefaultAlpha(t *testing.T) {
	want := []string{
		"machine learning basics",
		"deep neural networks",
		"grocery shopping list",
	}
	require.Equal(t, want, hybridOrder(t, `(hybrid "machine learning" `+hybridCorpus+`)`))
	// the default really is 0.5
	require.Equal(t, want, hybridOrder(t, `(hybrid "machine learning" `+hybridCorpus+` {:alpha 0.5})`))
}

func TestHybridScoresAndMarks(t *testing.T) {
	installFakeEncoder(t, hybridFake())
	got := evalSimilar(t, `(hybrid "machine learning" `+hybridCorpus+`)`)
	rows := got.([]Value)
	require.Len(t, rows, 3)

	top := rows[0].(Dictionary)
	require.Equal(t, Number(1), top["score"], "scores normalize to top = 1.0")
	require.Contains(t, string(top["_match"].(String)), "machine",
		"keyword-overlap rows carry the query terms for highlighting")

	last := rows[len(rows)-1].(Dictionary)
	require.Less(t, float64(last["score"].(Number)), 1.0)
	_, marked := last["_match"]
	require.False(t, marked, "no keyword overlap, no highlight mark")
}

func TestHybridPureKeywordNeedsNoModel(t *testing.T) {
	// No fake encoder installed: alpha 0 must not touch the model at all.
	got := evalSimilar(t, `(hybrid "basics" `+hybridCorpus+` {:alpha 0})`)
	rows := got.([]Value)
	require.Len(t, rows, 1)
	require.Equal(t, String("machine learning basics"), rows[0].(Dictionary)["text"])
}

func TestHybridErrors(t *testing.T) {
	installFakeEncoder(t, hybridFake())
	require.Contains(t, evalSimilarErr(t, `(hybrid "q")`).Error(), "hybrid expects")
	require.Contains(t, evalSimilarErr(t, `(hybrid "q" (list "a") {:alpha 2})`).Error(), ":alpha must be a number between 0 and 1")
	require.Contains(t, evalSimilarErr(t, `(hybrid "q" (list "a") {:alpha "x"})`).Error(), ":alpha expects a number")
	require.Contains(t, evalSimilarErr(t, `(hybrid "q" (list "a") {:bogus 1})`).Error(), ":alpha")
	require.Contains(t, evalSimilarErr(t, `(hybrid "..." (list "a") {:alpha 0})`).Error(), "no word tokens")
}

func TestHybridStreamAndLimit(t *testing.T) {
	installFakeEncoder(t, hybridFake())
	got := evalSimilar(t, `(hybrid "machine learning" (take 3 (lines "deep neural networks\nmachine learning basics\ngrocery shopping list")) {:limit 2})`)
	rows := got.([]Value)
	require.Len(t, rows, 2)
	require.Equal(t, String("machine learning basics"), rows[0].(Dictionary)["text"])
}
