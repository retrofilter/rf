package eval

import (
	"strings"
	"testing"
)

func evalBM25(t *testing.T, expr string) (Value, error) {
	t.Helper()
	ev := NewEvaluator()
	return evalExpr(expr, ev, ev.globalEnv)
}

func bm25Texts(t *testing.T, v Value) []string {
	t.Helper()
	rows, ok := v.([]Value)
	if !ok {
		t.Fatalf("expected a list of rows, got %T", v)
	}
	var texts []string
	for _, r := range rows {
		d, ok := r.(Dictionary)
		if !ok {
			t.Fatalf("expected dictionary rows, got %T", r)
		}
		if _, hasScore := d["score"].(Number); !hasScore {
			t.Fatalf("row missing score: %v", d)
		}
		texts = append(texts, string(d["text"].(String)))
	}
	return texts
}

func TestBM25Ranking(t *testing.T) {
	got, err := evalBM25(t, `(bm25 "the zebra runs" (list
		"the zebra runs"
		"the cat runs"
		"the dog runs fast"
		"the mailman waved"
		"quantum flux capacitors"))`)
	if err != nil {
		t.Fatal(err)
	}
	texts := bm25Texts(t, got)
	if len(texts) != 3 {
		t.Fatalf("expected 3 rows (stopword-only and no-overlap dropped), got %v", texts)
	}
	if texts[0] != "the zebra runs" {
		t.Errorf("expected the zebra doc first, got %q", texts[0])
	}
	for _, tx := range texts {
		if tx == "the mailman waved" || tx == "quantum flux capacitors" {
			t.Errorf("%q shares no content-bearing term and should have been dropped", tx)
		}
	}
}

func TestBM25StopwordOnlyQueryFallsBack(t *testing.T) {
	// A query that is all stopwords keeps them rather than erroring.
	got, err := evalBM25(t, `(bm25 "to be or not" (list
		"to be continued"
		"quantum flux capacitors"))`)
	if err != nil {
		t.Fatal(err)
	}
	texts := bm25Texts(t, got)
	if len(texts) != 1 || texts[0] != "to be continued" {
		t.Errorf("expected the stopword-sharing doc alone, got %v", texts)
	}
}

func TestBM25TermFrequencyAndLength(t *testing.T) {
	got, err := evalBM25(t, `(bm25 "wolf" (list
		"wolf"
		"wolf wolf in a long sentence about a wolf pack roaming the woods"
		"a lone wolf crossed the ridge"))`)
	if err != nil {
		t.Fatal(err)
	}
	texts := bm25Texts(t, got)
	if len(texts) != 3 {
		t.Fatalf("expected 3 rows, got %v", texts)
	}
	if texts[len(texts)-1] != "a lone wolf crossed the ridge" {
		t.Errorf("single occurrence in a longer doc should rank last, got order %v", texts)
	}
}

func TestBM25RowsAndOptions(t *testing.T) {
	got, err := evalBM25(t, `(bm25 "beta" (list
		{:name "one" :text "alpha beta"}
		{:name "two" :text "beta beta gamma"}
		{:name "three" :text "delta"}) {:key "text" :limit 1})`)
	if err != nil {
		t.Fatal(err)
	}
	rows := got.([]Value)
	if len(rows) != 1 {
		t.Fatalf("limit 1: got %d rows", len(rows))
	}
	d := rows[0].(Dictionary)
	if d["name"] != String("two") {
		t.Errorf("expected doc with highest tf first, got %v", d)
	}
	if pat, ok := d["_match"].(String); !ok || !strings.Contains(string(pat), "beta") {
		t.Errorf("expected _match with the query term, got %v", d["_match"])
	}
}

func TestBM25DoesNotMutateInput(t *testing.T) {
	ev := NewEvaluator()
	env := ev.globalEnv
	if _, err := evalExpr(`(define row {:text "needle here"})`, ev, env); err != nil {
		t.Fatal(err)
	}
	if _, err := evalExpr(`(bm25 "needle" (list row))`, ev, env); err != nil {
		t.Fatal(err)
	}
	keys, err := evalExpr(`(length (keys row))`, ev, env)
	if err != nil {
		t.Fatal(err)
	}
	if keys != Integer(1) {
		t.Fatalf("input row gained keys: %v", keys)
	}
}

func TestBM25Inputs(t *testing.T) {
	withTempDir(t, func(dir string, ev *Evaluator, env *Environment) {
		writeTree(t, dir, map[string]string{
			"doc.txt":     "first line about wolves\n\nplain filler\n",
			"sub/two.txt": "wolves again\n",
			".gitignore":  "skipme.txt\n",
			"skipme.txt":  "wolves hidden\n",
		})

		// A bare string names a file: its non-blank lines rank.
		got, err := evalExpr(`(bm25 "wolves" "doc.txt")`, ev, env)
		if err != nil {
			t.Fatal(err)
		}
		if texts := bm25Texts(t, got); len(texts) != 1 || texts[0] != "first line about wolves" {
			t.Fatalf("file input: got %v", texts)
		}

		got, err = evalExpr(`(bm25 "wolves" (grep "wol" "."))`, ev, env)
		if err != nil {
			t.Fatal(err)
		}
		rows := got.([]Value)
		if len(rows) != 2 {
			t.Fatalf("grep-dir pipe: expected 2 rows, got %v", got)
		}
		if pat := rows[0].(Dictionary)["_match"].(String); !strings.Contains(string(pat), "wolves") {
			t.Errorf("bm25 should replace grep's _match, got %q", pat)
		}

		got, err = evalExpr(`(bm25 "wolves" ".")`, ev, env)
		if err != nil {
			t.Fatal(err)
		}
		rows = got.([]Value)
		if len(rows) != 2 {
			t.Fatalf("dir input: expected 2 rows, got %v", got)
		}
		for _, r := range rows {
			if strings.Contains(string(r.(Dictionary)["file"].(String)), "skipme") {
				t.Error("dir input should honor .gitignore")
			}
		}
	})
}

func TestBM25Errors(t *testing.T) {
	for _, c := range []struct{ expr, want string }{
		{`(bm25 "q")`, "bm25 expects"},
		{`(bm25 42 (list "a"))`, "query must be a string"},
		{`(bm25 "..." (list "a"))`, "no word tokens"},
		{`(bm25 "q" 42)`, "must be a file path, a stream, or a list"},
		{`(bm25 "q" (list "a") {:bogus 1})`, "unknown option :bogus"},
		{`(bm25 "q" (list {:size 5}))`, "no string values to rank"},
	} {
		_, err := evalBM25(t, c.expr)
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: expected error containing %q, got %v", c.expr, c.want, err)
		}
	}
}

func BenchmarkBM25(b *testing.B) {
	lines := benchCorpus()
	items := make([]Value, len(lines))
	for i, l := range lines {
		items[i] = String(l)
	}
	terms, keepStop := bm25QueryTerms("timeout retrying worker")
	texts := make([]string, len(items))
	for i := range items {
		texts[i] = lines[i]
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		scores := bm25Scores(terms, texts, keepStop)
		if scores[250] <= 0 {
			b.Fatal("expected the ERR_TIMEOUT line to score")
		}
	}
}
