package llm

import (
	"fmt"
	"maps"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/retrofilter/rf/eval"
)

const (
	rerankDefaultWindow = 10
	rerankDefaultStride = 5

	rerankDefaultLimit = 100

	rerankMaxPassageChars = 6000
)

const rerankSystemPrompt = "You are RankLLM, an intelligent assistant that can rank passages " +
	"based on their relevancy to the query."

type rerankOptions struct {
	key         string // column to read passage text from
	window      int
	stride      int
	limit       int
	model       string // per-call model override, "" = the session model
	instruction string // task-specific relevance definition, "" = none
}

func (c *Chat) registerRerankBuiltin() {
	eval.Register("rerank", "reorder retrieved rows by listwise LLM relevance judgment — bm25 \"query\" corpus -n 100 | rerank \"query\"",
		eval.CommandMeta{Command: true, Stage: true, MinArgs: 1, MaxArgs: 2,
			Usage: "\"query\" [input]",
			Options: []eval.Option{
				{Long: "key", Short: "k", Kind: eval.OptionString, Placeholder: "COL", Doc: "read passage text from this column (dots reach nested dicts)"},
				{Long: "limit", Short: "n", Kind: eval.OptionInt, Placeholder: "N", Doc: "cost guard: max candidates (default 100)"},
				{Long: "window", Short: "w", Kind: eval.OptionInt, Placeholder: "N", Doc: "passages ranked per LLM call (default 10)"},
				{Long: "stride", Kind: eval.OptionInt, Placeholder: "N", Doc: "sliding-window step (default 5)"},
				{Long: "model", Kind: eval.OptionString, Placeholder: "ID", Doc: "per-call model override"},
				{Long: "instruction", Kind: eval.OptionString, Placeholder: "TEXT", Doc: "task-specific relevance definition"},
			}})
	usage := "(rerank \"query\" items [{:key \"col\" :window n :stride n :limit n :model \"id\" :instruction \"...\"}])"
	c.Env.Set("rerank", eval.BuiltinFunc(func(args []eval.Value, env *eval.Environment) (eval.Value, error) {
		pos, optMap, err := eval.ParseOptions("rerank", args)
		if err != nil {
			return nil, err
		}
		if len(pos) == 0 {
			return nil, fmt.Errorf("rerank requires a query: %s", usage)
		}
		query, ok := pos[0].(eval.String)
		if !ok {
			return nil, fmt.Errorf("rerank: query must be a string, got %s", eval.PrintValue(pos[0]))
		}
		if len(pos) > 2 {
			return nil, fmt.Errorf("rerank expects one input — quote the query: %s", usage)
		}
		opts := rerankOptions{
			key:         eval.OptString(optMap, "key", ""),
			window:      eval.OptInt(optMap, "window", rerankDefaultWindow),
			stride:      eval.OptInt(optMap, "stride", rerankDefaultStride),
			limit:       eval.OptInt(optMap, "limit", rerankDefaultLimit),
			model:       eval.OptString(optMap, "model", ""),
			instruction: eval.OptString(optMap, "instruction", ""),
		}
		if opts.stride < 1 || opts.limit < 1 {
			return nil, fmt.Errorf("rerank: :stride and :limit must be positive")
		}
		if len(pos) < 2 {
			return nil, fmt.Errorf("rerank expects an input — pipe candidates in (bm25 \"query\" corpus | rerank \"query\") or pass a list, stream, or file path")
		}
		input := pos[1]
		if opts.window < 2 {
			return nil, fmt.Errorf("rerank: :window must be at least 2, got %d", opts.window)
		}
		if opts.stride > opts.window {
			return nil, fmt.Errorf("rerank: :stride %d exceeds :window %d — sliding would skip items", opts.stride, opts.window)
		}

		items, err := c.Eval.RankInput("rerank", input)
		if err != nil {
			return nil, err
		}
		if len(items) == 0 {
			return []eval.Value{}, nil
		}
		if len(items) > opts.limit {
			return nil, fmt.Errorf("rerank: %d items exceeds the cost guard of %d — narrow the input (bm25's :limit) or raise :limit",
				len(items), opts.limit)
		}

		texts := make([]string, len(items))
		for i, item := range items {
			t, err := eval.RankText("rerank", item, opts.key)
			if err != nil {
				return nil, err
			}
			texts[i] = rerankPassage(t)
		}

		order, err := c.rerankOrder(string(query), texts, opts)
		if err != nil {
			return nil, err
		}

		out := make([]eval.Value, len(order))
		for rank, idx := range order {
			row := eval.Dictionary{}
			if d, ok := items[idx].(eval.Dictionary); ok {
				maps.Copy(row, d)
			} else {
				row["text"] = eval.String(texts[idx])
			}
			row["rank"] = eval.Integer(rank + 1)
			out[rank] = row
		}
		return out, nil
	}))
}

func (c *Chat) rerankOrder(query string, texts []string, opts rerankOptions) ([]int, error) {
	n := len(texts)
	order := make([]int, n)
	for i := range order {
		order[i] = i
	}
	end, start := n, n-opts.window
	for end > 0 {
		if eval.Interrupted() {
			return nil, eval.ErrInterrupted
		}
		s := max(start, 0)
		win := order[s:end]
		if len(win) >= 2 { // one passage has no ranking to ask for
			wtexts := make([]string, len(win))
			for i, idx := range win {
				wtexts[i] = texts[idx]
			}
			reply, err := c.completionWithModel(opts.model, rerankSystemPrompt,
				rerankPrompt(query, opts.instruction, wtexts))
			if err != nil {
				return nil, fmt.Errorf("rerank: %w", err)
			}
			prev := slices.Clone(win)
			for newIdx, oldIdx := range rerankPermutation(reply, len(win)) {
				win[newIdx] = prev[oldIdx]
			}
		}
		end -= opts.stride
		start -= opts.stride
	}
	return order, nil
}

func rerankPrompt(query, instruction string, passages []string) string {
	flatQuery := flattenItem(query)
	var b strings.Builder
	fmt.Fprintf(&b, "I will provide you with %d passages, each indicated by a numerical identifier [].\n", len(passages))
	fmt.Fprintf(&b, "Rank the passages based on their relevance to the query: %s\n", flatQuery)
	if instruction != "" {
		b.WriteString(strings.TrimSpace(instruction))
		b.WriteString("\n")
	}
	for i, p := range passages {
		fmt.Fprintf(&b, "[%d] %s\n", i+1, p)
	}
	fmt.Fprintf(&b, "\nSearch Query: %s\n", flatQuery)
	fmt.Fprintf(&b, "Rank the %d passages above based on their relevance to the query. "+
		"All the passages should be included and listed using identifiers, in descending order of relevance. "+
		"The output format should be [] > [], e.g., [4] > [2]. "+
		"Think before responding only with the ranking results, do not say any word or explain.", len(passages))
	return b.String()
}

var rerankBracketID = regexp.MustCompile(`\[(\d+)\]`)

func rerankPermutation(reply string, n int) []int {
	if i := strings.LastIndex(reply, "</think>"); i >= 0 {
		reply = reply[i+len("</think>"):]
	}
	order := make([]int, 0, n)
	seen := make([]bool, n)
	for _, m := range rerankBracketID.FindAllStringSubmatch(reply, -1) {
		v, err := strconv.Atoi(m[1])
		if err != nil || v < 1 || v > n || seen[v-1] {
			continue
		}
		seen[v-1] = true
		order = append(order, v-1)
	}
	for i := range n {
		if !seen[i] {
			order = append(order, i)
		}
	}
	return order
}

func rerankPassage(text string) string {
	s := flattenItem(text)
	if len(s) <= rerankMaxPassageChars {
		return s
	}
	cut := rerankMaxPassageChars
	for cut > 0 && s[cut]&0xC0 == 0x80 { // back off a split UTF-8 sequence
		cut--
	}
	return s[:cut]
}
