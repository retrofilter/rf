package eval

import (
	"errors"
	"maps"
	"math"
	"os"
	"regexp"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"
)

const (
	bm25K1 = 1.2
	bm25B  = 0.75
)

func bm25EachToken(s string, fn func(tok []byte)) {
	var buf []byte
	for _, r := range s {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			buf = utf8.AppendRune(buf, unicode.ToLower(r))
			continue
		}
		if len(buf) > 0 {
			fn(buf)
			buf = buf[:0]
		}
	}
	if len(buf) > 0 {
		fn(buf)
	}
}

func bm25QueryTerms(query string) (terms []string, keepStop bool) {
	collect := func(keep bool) (terms []string) {
		seen := map[string]bool{}
		bm25EachToken(query, func(tok []byte) {
			if !keep && stopwords[string(tok)] {
				return
			}
			if !seen[string(tok)] {
				seen[string(tok)] = true
				terms = append(terms, string(tok))
			}
		})
		return terms
	}
	terms = collect(false)
	if len(terms) == 0 {
		terms = collect(true)
		keepStop = true
	}
	return terms, keepStop
}

func bm25Scores(terms []string, texts []string, keepStop bool) []float64 {
	qIdx := make(map[string]int, len(terms))
	for i, t := range terms {
		qIdx[t] = i
	}
	tfs := make([][]int, len(texts))
	dls := make([]int, len(texts))
	totalLen := 0
	for i, text := range texts {
		tf := make([]int, len(terms))
		dl := 0
		bm25EachToken(text, func(tok []byte) {
			if !keepStop && stopwords[string(tok)] {
				return
			}
			dl++
			if j, ok := qIdx[string(tok)]; ok {
				tf[j]++
			}
		})
		tfs[i], dls[i] = tf, dl
		totalLen += dl
	}

	df := make([]int, len(terms))
	for _, tf := range tfs {
		for j, c := range tf {
			if c > 0 {
				df[j]++
			}
		}
	}
	n := float64(len(texts))
	avgdl := totalLen / max(len(texts), 1)
	idf := make([]float64, len(terms))
	for j, d := range df {
		idf[j] = math.Log(1 + (n-float64(d)+0.5)/(float64(d)+0.5))
	}

	scores := make([]float64, len(texts))
	for i, tf := range tfs {
		norm := bm25K1 * (1 - bm25B + bm25B*float64(dls[i])/float64(max(avgdl, 1)))
		var score float64
		for j, c := range tf {
			if c == 0 {
				continue
			}
			score += idf[j] * float64(c) * (bm25K1 + 1) / (float64(c) + norm)
		}
		scores[i] = score
	}
	return scores
}

func bm25Builtins(env *Environment, approval *approvalGate) {
	Register("bm25", "rank items by BM25 keyword relevance to a query", CommandMeta{
		Command: true, MinArgs: 2, MaxArgs: 2, Stage: true,
		Usage: "\"query\" [input]", Options: rankerOptions})
	env.Set("bm25", BuiltinFunc(func(args []Value, env *Environment) (Value, error) {
		pos, opts, err := ParseOptions("bm25", args)
		if err != nil {
			return nil, err
		}
		if len(pos) != 2 {
			return nil, errors.New("bm25 expects a query and a corpus: (bm25 \"query\" items [{:key \"name\" :limit n}])")
		}
		query, ok := pos[0].(String)
		if !ok {
			return nil, errors.New("bm25: query must be a string")
		}
		var items []Value
		if s, isStr := pos[1].(String); isStr {
			if fi, statErr := os.Stat(expandHome(string(s))); statErr == nil && fi.IsDir() {
				matchAll, _ := compileGrepMatcher("")
				lst, _, listErr := AsList(dirGrepStream(matchAll, "", expandHome(string(s)), grepCtxOpt{}))
				if listErr != nil {
					return nil, listErr
				}
				items = lst
			}
		}
		if items == nil {
			items, err = rankInput("bm25", pos[1], approval)
			if err != nil {
				return nil, err
			}
		}
		key, limit, err := rankOptions("bm25", opts)
		if err != nil {
			return nil, err
		}
		terms, keepStop := bm25QueryTerms(string(query))
		if len(terms) == 0 {
			return nil, errors.New("bm25: query has no word tokens")
		}
		if len(items) == 0 {
			return []Value{}, nil
		}

		texts := make([]string, len(items))
		for i, item := range items {
			t, err := rankText("bm25", item, key)
			if err != nil {
				return nil, err
			}
			texts[i] = t
		}

		scores := bm25Scores(terms, texts, keepStop)

		// Highlight the query's terms in kept rows, grep-style.
		quoted := make([]string, len(terms))
		for i, t := range terms {
			quoted[i] = regexp.QuoteMeta(t)
		}
		matchPat := String("(?i)(" + strings.Join(quoted, "|") + ")")

		type scored struct {
			row   Dictionary
			score float64
		}
		var rows []scored
		for i, item := range items {
			if scores[i] <= 0 {
				continue // shares no term with the query
			}
			row, ok := item.(Dictionary)
			if !ok {
				row = Dictionary{"text": String(texts[i])}
			} else {
				copied := make(Dictionary, len(row)+2)
				maps.Copy(copied, row)
				row = copied
			}
			row["score"] = roundScore(scores[i])
			row["_match"] = matchPat
			rows = append(rows, scored{row, scores[i]})
		}
		sort.SliceStable(rows, func(i, j int) bool { return rows[i].score > rows[j].score })
		if limit > 0 && limit < len(rows) {
			rows = rows[:limit]
		}
		ranked := make([]Value, len(rows))
		for i, r := range rows {
			ranked[i] = r.row
		}
		return ranked, nil
	}))
}
