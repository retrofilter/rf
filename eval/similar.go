package eval

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"math"
	"os"
	"regexp"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/tphakala/simd/f32"
	potion "github.com/trengrj/go-potion"
)

type textEncoder interface {
	Encode(sentence string) ([]float32, error)
	EncodeMany(sentences []string) ([][]float32, error)
}

const defaultPotionModel = potion.RETRIEVAL32M

const encoderTimeout = 5 * time.Minute

var (
	encoderMu sync.Mutex
	encoders  = map[potion.Model]textEncoder{}
)

func currentModel(env *Environment) (potion.Model, error) {
	name := string(defaultPotionModel)
	if val, err := env.Lookup("embedding-model"); err == nil {
		if s, ok := asString(val); ok && s != "" {
			name = s
		}
	}
	model := potion.Model(strings.ToUpper(name))
	known := potion.Models()
	if slices.Contains(known, model) {
		return model, nil
	}
	names := make([]string, len(known))
	for i, m := range known {
		names[i] = string(m)
	}
	return "", fmt.Errorf("unknown embedding-model %q (supported: %s)", name, strings.Join(names, " "))
}

func getEncoder(env *Environment) (textEncoder, error) {
	model, err := currentModel(env)
	if err != nil {
		return nil, err
	}
	encoderMu.Lock()
	defer encoderMu.Unlock()
	if enc, ok := encoders[model]; ok {
		return enc, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), encoderTimeout)
	defer cancel()
	notice := time.AfterFunc(time.Second, func() {
		fmt.Fprintf(os.Stderr, "downloading embedding model %q...\n", string(model))
	})
	p, err := potion.New(ctx, model)
	notice.Stop()
	if err != nil {
		return nil, fmt.Errorf("loading embedding model %s: %w", model, err)
	}
	encoders[model] = p
	return p, nil
}

func dot(a, b []float32) float64 {
	return float64(f32.DotProduct(a, b))
}

func roundScore(s float64) Number {
	return Number(math.Round(s*1000) / 1000)
}

func rankText(op string, item Value, key string) (string, error) {
	switch v := item.(type) {
	case String:
		return string(v), nil
	case Dictionary:
		if key != "" {
			s, ok := rankKeyPath(v, key)
			if !ok {
				return "", fmt.Errorf("%s: item has no string value for key %q: %s", op, key, PrintValue(item))
			}
			return s, nil
		}
		parts := rankStrings(v, 1)
		if len(parts) == 0 {
			return "", fmt.Errorf("%s: item has no string values to rank: %s", op, PrintValue(item))
		}
		return strings.Join(parts, " "), nil
	default:
		return PrintValue(item), nil
	}
}

func rankKeyPath(dict Dictionary, path string) (string, bool) {
	segs := strings.Split(path, ".")
	for i, seg := range segs {
		if i == len(segs)-1 {
			s, ok := dict[seg].(String)
			return string(s), ok
		}
		next, ok := dict[seg].(Dictionary)
		if !ok {
			return "", false
		}
		dict = next
	}
	return "", false
}

func rankStrings(dict Dictionary, depth int) []string {
	keys := make([]string, 0, len(dict))
	for k := range dict {
		if strings.HasPrefix(k, "_") {
			continue
		}
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var parts []string
	for _, k := range keys {
		switch val := dict[k].(type) {
		case String:
			parts = append(parts, string(val))
		case Dictionary:
			if depth > 0 {
				parts = append(parts, rankStrings(val, depth-1)...)
			}
		}
	}
	return parts
}

func dropBlankLines(lst []Value) []Value {
	var items []Value
	for _, v := range lst {
		if s, isStr := v.(String); isStr && strings.TrimSpace(string(s)) == "" {
			continue
		}
		items = append(items, v)
	}
	return items
}

func rankInput(op string, v Value, approval *approvalGate) ([]Value, error) {
	switch in := normalizeList(v).(type) {
	case []Value:
		return in, nil
	case *Stream:
		lst, _, err := AsList(in)
		if err != nil {
			return nil, err
		}
		return dropBlankLines(lst), nil
	case String:
		path := expandHome(string(in))
		if err := approval.requireRead(fmt.Sprintf("%s %q", op, path), path); err != nil {
			return nil, err
		}
		f, err := os.Open(path)
		if err != nil {
			return nil, fmt.Errorf("%s: %v (the input string names a file — use (lines \"text\") to rank literal text)", op, err)
		}
		lst, _, err := AsList(streamFromReader(f, f.Close))
		if err != nil {
			return nil, err
		}
		return dropBlankLines(lst), nil
	default:
		return nil, fmt.Errorf("%s: second argument must be a file path, a stream, or a list, got %s", op, PrintValue(v))
	}
}

// RankInput is rankInput for builtins registered outside the package (llm-
// map/classify in llm/), sharing the ranking builtins' input convention
// including the read gate on file inputs.
func (e *Evaluator) RankInput(op string, v Value) ([]Value, error) {
	return rankInput(op, v, e.approval)
}

// RankText is rankText for builtins registered outside the package.
func RankText(op string, item Value, key string) (string, error) { return rankText(op, item, key) }

var rankerOptions = []Option{
	{Long: "key", Short: "k", Kind: OptionString, Placeholder: "COL", Doc: "rank rows by this column's text (dots reach nested dicts)"},
	{Long: "limit", Short: "n", Kind: OptionInt, Placeholder: "N", Doc: "rows returned (default 10)"},
}

func rankOptions(op string, opts map[string]Value) (key string, limit int, err error) {
	key = OptString(opts, "key", "")
	limit = OptInt(opts, "limit", 10)
	if limit < 1 {
		return "", 0, fmt.Errorf("%s: :limit must be a positive number", op)
	}
	return key, limit, nil
}

func similarBuiltins(env *Environment, approval *approvalGate) {
	env.Set("embedding-model", String(defaultPotionModel))

	Register("embed", "a text's embedding vector as a list of numbers", CommandMeta{
		Command: true, MinArgs: 1, MaxArgs: 1, Usage: "\"text\""})
	env.Set("embed", BuiltinFunc(func(args []Value, env *Environment) (Value, error) {
		if len(args) != 1 {
			return nil, errors.New("embed expects one string: (embed \"text\")")
		}
		s, ok := args[0].(String)
		if !ok {
			return nil, errors.New("embed expects a string")
		}
		enc, err := getEncoder(env)
		if err != nil {
			return nil, err
		}
		vec, err := enc.Encode(string(s))
		if err != nil {
			return nil, err
		}
		out := make([]Value, len(vec))
		for i, f := range vec {
			out[i] = Number(f)
		}
		return out, nil
	}))

	env.SetBuiltin("similarity", "semantic similarity score of two strings", BuiltinFunc(func(args []Value, env *Environment) (Value, error) {
		if len(args) != 2 {
			return nil, errors.New("similarity expects two strings: (similarity \"a\" \"b\")")
		}
		a, ok1 := args[0].(String)
		b, ok2 := args[1].(String)
		if !ok1 || !ok2 {
			return nil, errors.New("similarity expects two strings")
		}
		enc, err := getEncoder(env)
		if err != nil {
			return nil, err
		}
		vecs, err := enc.EncodeMany([]string{string(a), string(b)})
		if err != nil {
			return nil, err
		}
		return roundScore(dot(vecs[0], vecs[1])), nil
	}))

	Register("similar", "rank items by semantic similarity to a query", CommandMeta{
		Command: true, MinArgs: 2, MaxArgs: 2, Stage: true,
		Usage: "\"query\" [input]", Options: rankerOptions})
	env.Set("similar", BuiltinFunc(func(args []Value, env *Environment) (Value, error) {
		pos, opts, err := ParseOptions("similar", args)
		if err != nil {
			return nil, err
		}
		if len(pos) != 2 {
			return nil, errors.New("similar expects a query and an input: (similar \"query\" items [{:key \"name\" :limit n}])")
		}
		query, ok := pos[0].(String)
		if !ok {
			return nil, errors.New("similar: query must be a string")
		}
		items, err := rankInput("similar", pos[1], approval)
		if err != nil {
			return nil, err
		}
		key, limit, err := rankOptions("similar", opts)
		if err != nil {
			return nil, err
		}
		if len(items) == 0 {
			return []Value{}, nil
		}

		texts := make([]string, len(items))
		for i, item := range items {
			t, err := rankText("similar", item, key)
			if err != nil {
				return nil, err
			}
			texts[i] = t
		}

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

		type scored struct {
			row   Dictionary
			score float64
		}
		queryLower := strings.ToLower(string(query))
		matchPat := String("(?i)" + regexp.QuoteMeta(string(query)))
		rows := make([]scored, len(items))
		for i, item := range items {
			score := dot(queryVec, itemVecs[i])
			row, ok := item.(Dictionary)
			if !ok {
				row = Dictionary{"text": String(texts[i])}
			} else {
				copied := make(Dictionary, len(row)+1)
				maps.Copy(copied, row)
				row = copied
			}
			row["score"] = roundScore(score)
			if queryLower != "" && strings.Contains(strings.ToLower(texts[i]), queryLower) {
				row["_match"] = matchPat
			}
			rows[i] = scored{row, score}
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
