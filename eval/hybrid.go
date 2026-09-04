package eval

import (
	"errors"
	"maps"
	"math"
	"regexp"
	"sort"
	"strings"
)

const rrfK = 60

func rrfFuse(lists [][]float64, weights []float64) []float64 {
	if len(lists) == 0 {
		return nil
	}
	n := len(lists[0])
	fused := make([]float64, n)
	idx := make([]int, n)
	for l, scores := range lists {
		if weights[l] == 0 {
			continue
		}
		for i := range idx {
			idx[i] = i
		}
		sort.SliceStable(idx, func(a, b int) bool { return scores[idx[a]] > scores[idx[b]] })
		for rank, i := range idx {
			if scores[i] <= 0 {
				break // sorted: everything from here on is absent
			}
			fused[i] += weights[l] / float64(rrfK+rank+1)
		}
	}
	return fused
}

func hybridBuiltins(env *Environment, approval *approvalGate) {
	hybridOptions := append(append([]Option{}, rankerOptions...),
		Option{Long: "alpha", Short: "a", Kind: OptionNumber, Placeholder: "X", Doc: "semantic weight 0..1 (1 = pure semantic, default 0.5)"})
	Register("hybrid", "rank items by fused keyword+semantic relevance", CommandMeta{
		Command: true, MinArgs: 2, MaxArgs: 2, Stage: true,
		Usage: "\"query\" [input]", Options: hybridOptions})
	env.Set("hybrid", BuiltinFunc(func(args []Value, env *Environment) (Value, error) {
		pos, opts, err := ParseOptions("hybrid", args)
		if err != nil {
			return nil, err
		}
		if len(pos) != 2 {
			return nil, errors.New("hybrid expects a query and a corpus: (hybrid \"query\" items [{:key \"name\" :limit n :alpha 0.5}])")
		}
		query, ok := pos[0].(String)
		if !ok {
			return nil, errors.New("hybrid: query must be a string")
		}
		items, err := rankInput("hybrid", pos[1], approval)
		if err != nil {
			return nil, err
		}
		alpha := OptNumber(opts, "alpha", 0.5)
		if alpha < 0 || alpha > 1 {
			return nil, errors.New("hybrid: :alpha must be a number between 0 and 1")
		}
		key, limit, err := rankOptions("hybrid", opts)
		if err != nil {
			return nil, err
		}
		if len(items) == 0 {
			return []Value{}, nil
		}

		texts := make([]string, len(items))
		for i, item := range items {
			t, err := rankText("hybrid", item, key)
			if err != nil {
				return nil, err
			}
			texts[i] = t
		}

		semScores := make([]float64, len(items))
		if alpha > 0 {
			enc, err := getEncoder(env)
			if err != nil {
				return nil, err
			}
			queryVec, err := enc.Encode(string(query))
			if err != nil {
				return nil, err
			}
			itemVecs, err := enc.EncodeMany(texts)
			if err != nil {
				return nil, err
			}
			for i := range texts {
				semScores[i] = dot(queryVec, itemVecs[i])
			}
		}

		terms, keepStop := bm25QueryTerms(string(query))
		kwScores := make([]float64, len(items))
		if len(terms) > 0 {
			kwScores = bm25Scores(terms, texts, keepStop)
		}
		if alpha == 0 && len(terms) == 0 {
			return nil, errors.New("hybrid: query has no word tokens and :alpha 0 disables the semantic side")
		}

		minSem := 0.0
		for _, s := range semScores {
			minSem = math.Min(minSem, s)
		}
		if alpha > 0 {
			for i := range semScores {
				semScores[i] += -minSem + 1
			}
		}

		fused := rrfFuse([][]float64{semScores, kwScores}, []float64{alpha, 1 - alpha})

		maxFused := 0.0
		for _, f := range fused {
			maxFused = math.Max(maxFused, f)
		}
		if maxFused == 0 {
			return []Value{}, nil
		}

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
			if fused[i] <= 0 {
				continue // alpha 0 and no term overlap
			}
			row, ok := item.(Dictionary)
			if !ok {
				row = Dictionary{"text": String(texts[i])}
			} else {
				copied := make(Dictionary, len(row)+2)
				maps.Copy(copied, row)
				row = copied
			}
			row["score"] = roundScore(fused[i] / maxFused)
			if len(terms) > 0 && kwScores[i] > 0 {
				row["_match"] = matchPat
			} else {
				delete(row, "_match") // don't inherit an upstream grep mark
			}
			rows = append(rows, scored{row, fused[i]})
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
