package eval

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	potion "github.com/trengrj/go-potion"
)

type fakeEncoder struct {
	vectors map[string][]float32
}

func (f *fakeEncoder) Encode(sentence string) ([]float32, error) {
	if v, ok := f.vectors[sentence]; ok {
		return v, nil
	}
	return []float32{0, 0, 0, 1}, nil
}

func (f *fakeEncoder) EncodeMany(sentences []string) ([][]float32, error) {
	out := make([][]float32, len(sentences))
	for i, s := range sentences {
		v, err := f.Encode(s)
		if err != nil {
			return nil, err
		}
		out[i] = v
	}
	return out, nil
}

func installFakeEncoder(t *testing.T, f textEncoder) {
	t.Helper()
	installFakeEncoderFor(t, defaultPotionModel, f)
}

func installFakeEncoderFor(t *testing.T, model potion.Model, f textEncoder) {
	t.Helper()
	encoderMu.Lock()
	prev, had := encoders[model]
	encoders[model] = f
	encoderMu.Unlock()
	t.Cleanup(func() {
		encoderMu.Lock()
		if had {
			encoders[model] = prev
		} else {
			delete(encoders, model)
		}
		encoderMu.Unlock()
	})
}

func evalSimilar(t *testing.T, code string) Value {
	t.Helper()
	ev := NewEvaluator()
	expr, err := Parse(code)
	require.NoError(t, err)
	result, err := ev.Eval(expr, ev.GlobalEnv())
	require.NoError(t, err)
	return result
}

func evalSimilarErr(t *testing.T, code string) error {
	t.Helper()
	ev := NewEvaluator()
	expr, err := Parse(code)
	require.NoError(t, err)
	_, err = ev.Eval(expr, ev.GlobalEnv())
	require.Error(t, err)
	return err
}

func docsFake() *fakeEncoder {
	return &fakeEncoder{vectors: map[string][]float32{
		"documentation": {1, 0, 0, 0},
		"docs.md":       {0.9, 0.1, 0, 0},
		"README.md":     {0.6, 0.4, 0, 0},
		"main.go":       {0, 1, 0, 0},
	}}
}

func TestSimilarMarksExactMatches(t *testing.T) {
	installFakeEncoder(t, docsFake())
	result := evalSimilar(t, `(similar "documentation" (list "reading DOCUMENTATION here" "main.go"))`)
	rows := result.([]Value)
	require.Len(t, rows, 2)
	for _, r := range rows {
		d := r.(Dictionary)
		pat, marked := d["_match"]
		if strings.Contains(strings.ToLower(string(d["text"].(String))), "documentation") {
			require.Equal(t, String("(?i)documentation"), pat)
		} else {
			require.False(t, marked, "no exact hit, no mark: %v", d)
		}
	}
}

func TestSimilarRanksStrings(t *testing.T) {
	installFakeEncoder(t, docsFake())
	result := evalSimilar(t, `(similar "documentation" (list "main.go" "docs.md" "README.md"))`)
	rows, ok := result.([]Value)
	require.True(t, ok)
	require.Len(t, rows, 3)
	first := rows[0].(Dictionary)
	require.Equal(t, String("docs.md"), first["text"])
	require.InDelta(t, 0.9, float64(first["score"].(Number)), 0.001)
	require.Equal(t, String("README.md"), rows[1].(Dictionary)["text"])
	require.Equal(t, String("main.go"), rows[2].(Dictionary)["text"])
}

func TestSimilarRanksDictionariesByName(t *testing.T) {
	installFakeEncoder(t, docsFake())
	result := evalSimilar(t, `(similar "documentation" (list {:name "main.go" :size 10} {:name "docs.md" :size 20}))`)
	rows := result.([]Value)
	require.Len(t, rows, 2)
	first := rows[0].(Dictionary)
	require.Equal(t, String("docs.md"), first["name"])
	require.Equal(t, Integer(20), first["size"])
	require.InDelta(t, 0.9, float64(first["score"].(Number)), 0.001)
}

func TestSimilarRanksFileLines(t *testing.T) {
	installFakeEncoder(t, docsFake())
	path := filepath.Join(t.TempDir(), "files.txt")
	require.NoError(t, os.WriteFile(path, []byte("main.go\n\ndocs.md\n   \nREADME.md\n"), 0644))
	result := evalSimilar(t, `(similar "documentation" "`+path+`")`)
	rows := result.([]Value)
	require.Len(t, rows, 3)
	require.Equal(t, String("docs.md"), rows[0].(Dictionary)["text"])
	require.Equal(t, String("README.md"), rows[1].(Dictionary)["text"])
	require.Equal(t, String("main.go"), rows[2].(Dictionary)["text"])
}

func TestSimilarRanksStreamLines(t *testing.T) {
	installFakeEncoder(t, docsFake())
	// A stream input (cat, sh) materializes, blank lines dropped.
	ev := NewEvaluator()
	ev.GlobalEnv().Set("input", streamFromList([]Value{
		String("main.go"), String(""), String("docs.md"), String("   "), String("README.md"),
	}, true))
	expr, err := Parse(`(similar "documentation" input)`)
	require.NoError(t, err)
	result, err := ev.Eval(expr, ev.GlobalEnv())
	require.NoError(t, err)
	rows := result.([]Value)
	require.Len(t, rows, 3)
	require.Equal(t, String("docs.md"), rows[0].(Dictionary)["text"])
	require.Equal(t, String("README.md"), rows[1].(Dictionary)["text"])
	require.Equal(t, String("main.go"), rows[2].(Dictionary)["text"])
}

func TestSimilarKeyAndLimitOptions(t *testing.T) {
	installFakeEncoder(t, docsFake())
	result := evalSimilar(t, `(similar "documentation" (list {:file "main.go"} {:file "docs.md"} {:file "README.md"}) {:key "file" :limit 2})`)
	rows := result.([]Value)
	require.Len(t, rows, 2)
	require.Equal(t, String("docs.md"), rows[0].(Dictionary)["file"])
	require.Equal(t, String("README.md"), rows[1].(Dictionary)["file"])
}

func TestSimilarDoesNotMutateInput(t *testing.T) {
	installFakeEncoder(t, docsFake())
	ev := NewEvaluator()
	for _, code := range []string{
		`(define items (list {:name "docs.md"}))`,
		`(similar "documentation" items)`,
	} {
		expr, err := Parse(code)
		require.NoError(t, err)
		_, err = ev.Eval(expr, ev.GlobalEnv())
		require.NoError(t, err)
	}
	expr, err := Parse(`(keys (get 0 items))`)
	require.NoError(t, err)
	keys, err := ev.Eval(expr, ev.GlobalEnv())
	require.NoError(t, err)
	require.Len(t, keys.([]Value), 1, "input dictionary must not gain a score key")
}

func TestSimilarErrors(t *testing.T) {
	installFakeEncoder(t, docsFake())
	require.Contains(t, evalSimilarErr(t, `(similar "q")`).Error(), "similar expects")
	require.Contains(t, evalSimilarErr(t, `(similar "q" 42)`).Error(), "must be a file path, a stream, or a list")
	require.Contains(t, evalSimilarErr(t, `(similar "q" (list "a") {:bogus 1})`).Error(), "unknown option :bogus")
	require.Contains(t, evalSimilarErr(t, `(similar "q" (list {:name "a"}) {:key "missing"})`).Error(), `no string value for key "missing"`)
	require.Contains(t, evalSimilarErr(t, `(similar "q" (list {:size 5}))`).Error(), "no string values to rank")
}

func TestPotionModelBinding(t *testing.T) {
	installFakeEncoderFor(t, potion.BASE2M, &fakeEncoder{vectors: map[string][]float32{
		"documentation": {1, 0, 0, 0},
		"main.go":       {1, 0, 0, 0},
		"docs.md":       {0, 1, 0, 0},
	}})
	ev := NewEvaluator()
	for _, code := range []string{
		`(define embedding-model "base2m")`, // lowercase: binding is case-insensitive
		`(define ranked (similar "documentation" (list "docs.md" "main.go")))`,
	} {
		expr, err := Parse(code)
		require.NoError(t, err)
		_, err = ev.Eval(expr, ev.GlobalEnv())
		require.NoError(t, err)
	}
	expr, err := Parse(`(get "text" (get 0 ranked))`)
	require.NoError(t, err)
	first, err := ev.Eval(expr, ev.GlobalEnv())
	require.NoError(t, err)
	require.Equal(t, String("main.go"), first)
}

func TestPotionModelBindingUnknown(t *testing.T) {
	ev := NewEvaluator()
	for _, code := range []string{`(define embedding-model "gpt-9")`} {
		expr, err := Parse(code)
		require.NoError(t, err)
		_, err = ev.Eval(expr, ev.GlobalEnv())
		require.NoError(t, err)
	}
	expr, err := Parse(`(embed "x")`)
	require.NoError(t, err)
	_, err = ev.Eval(expr, ev.GlobalEnv())
	require.Error(t, err)
	require.Contains(t, err.Error(), `unknown embedding-model "gpt-9"`)
	require.Contains(t, err.Error(), "BASE2M")
}

func TestSimilarEmptyList(t *testing.T) {
	installFakeEncoder(t, docsFake())
	result := evalSimilar(t, `(similar "q" (list))`)
	require.Empty(t, result.([]Value))
}

func TestSimilarityAndEmbed(t *testing.T) {
	installFakeEncoder(t, docsFake())
	score := evalSimilar(t, `(similarity "documentation" "docs.md")`)
	require.InDelta(t, 0.9, float64(score.(Number)), 0.001)

	vec := evalSimilar(t, `(embed "documentation")`)
	require.Equal(t, []Value{Number(1), Number(0), Number(0), Number(0)}, vec)
}

func TestSimilarLive(t *testing.T) {
	if os.Getenv("RF_LIVE_POTION") != "1" {
		t.Skip("set RF_LIVE_POTION=1 to run the live embedding test")
	}
	result := evalSimilar(t, `(similar "documentation" (list "shopping list" "api reference manual" "banana bread recipe"))`)
	rows := result.([]Value)
	require.Len(t, rows, 3)
	require.Equal(t, String("api reference manual"), rows[0].(Dictionary)["text"],
		fmt.Sprintf("expected the manual to rank first, got %s", PrintValue(result)))
}

func TestRankText(t *testing.T) {
	got, err := rankText("similar", Dictionary{"name": String("John"), "knows": String("Jill")}, "")
	require.NoError(t, err)
	require.Equal(t, "Jill John", got)

	// Underscore-prefixed (hidden) columns are skipped.
	got, err = rankText("similar", Dictionary{"text": String("fix tests"), "_age-days": String("30")}, "")
	require.NoError(t, err)
	require.Equal(t, "fix tests", got)

	got, err = rankText("similar", Dictionary{
		"type": String("task"),
		"properties": Dictionary{
			"text":   String("ship it"),
			"deeper": Dictionary{"lost": String("nope")},
		},
	}, "")
	require.NoError(t, err)
	require.Equal(t, "ship it task", got)

	got, err = rankText("bm25", Dictionary{"properties": Dictionary{"title": String("hub")}}, "properties.title")
	require.NoError(t, err)
	require.Equal(t, "hub", got)
	_, err = rankText("bm25", Dictionary{"properties": Dictionary{}}, "properties.title")
	require.Error(t, err)
	_, err = rankText("bm25", Dictionary{"title": String("x")}, "title.deeper")
	require.Error(t, err)

	// Rows with no visible string values refuse rather than rank garbage.
	_, err = rankText("similar", Dictionary{"size": Integer(3)}, "")
	require.Error(t, err)
}
