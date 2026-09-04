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

const mapDefaultBatch = 10

const mapDefaultLimit = 100

const mapSystemPrompt = "You are called as a function inside a Scheme shell, applying an " +
	"instruction to each item of a numbered list. Reply with exactly one line per item, in " +
	"the form `N. answer` with N the item's number — every item answered, no other text, no fences."

const classifySystemPrompt = mapSystemPrompt + " Each answer is a single short label " +
	"(one or two words); give identical categories identical spelling."

const mapItemSystemPrompt = "You are called as a function inside a Scheme shell, applying an " +
	"instruction to one item. Reply with only the answer itself, on one line — no preamble, no fences."

type mapSpec struct {
	op         string
	column     string
	system     string
	itemSystem string
}

func (c *Chat) registerMapBuiltins() {
	mapOptions := []eval.Option{
		{Long: "key", Short: "k", Kind: eval.OptionString, Placeholder: "COL", Doc: "read item text from this column (dots reach nested dicts)"},
		{Long: "column", Short: "c", Kind: eval.OptionString, Placeholder: "NAME", Doc: "name of the reply column"},
		{Long: "batch", Short: "b", Kind: eval.OptionInt, Placeholder: "N", Doc: "items per completion (default 10)"},
		{Long: "limit", Short: "n", Kind: eval.OptionInt, Placeholder: "N", Doc: "cost guard: max items (default 100)"},
	}
	eval.Register("llm-map", "map an LLM instruction over items, replies as a new llm column — history | take 20 | llm-map \"one-line summary\"",
		eval.CommandMeta{Command: true, Stage: true, MinArgs: 1, MaxArgs: 2, Usage: "\"instruction\" [input]", Options: mapOptions})
	eval.Register("classify", "label each item via the LLM — history | take 50 | classify \"bug-fix or feature?\" | count-by label",
		eval.CommandMeta{Command: true, Stage: true, MinArgs: 1, MaxArgs: 2, Usage: "\"instruction\" [input]", Options: mapOptions})
	c.Env.Set("llm-map", c.mapBuiltin(mapSpec{
		op: "llm-map", column: "llm",
		system:     mapSystemPrompt,
		itemSystem: mapItemSystemPrompt,
	}))
	c.Env.Set("classify", c.mapBuiltin(mapSpec{
		op: "classify", column: "label",
		system:     classifySystemPrompt,
		itemSystem: mapItemSystemPrompt + " The answer is a single short label (one or two words).",
	}))
}

func (c *Chat) mapBuiltin(spec mapSpec) eval.BuiltinFunc {
	usage := fmt.Sprintf("(%s \"instruction\" items [{:key \"col\" :column \"name\" :batch n :limit n}])", spec.op)
	return func(args []eval.Value, env *eval.Environment) (eval.Value, error) {
		pos, opts, err := eval.ParseOptions(spec.op, args)
		if err != nil {
			return nil, err
		}
		if len(pos) == 0 {
			return nil, fmt.Errorf("%s requires an instruction: %s", spec.op, usage)
		}
		instruction, ok := pos[0].(eval.String)
		if !ok {
			return nil, fmt.Errorf("%s: instruction must be a string, got %s", spec.op, eval.PrintValue(pos[0]))
		}
		if len(pos) > 2 {
			return nil, fmt.Errorf("%s expects one input — quote the instruction: %s", spec.op, usage)
		}
		if len(pos) < 2 {
			return nil, fmt.Errorf("%s expects an input — pipe items in (history | %s \"...\") or pass a list, stream, or file path", spec.op, spec.op)
		}
		input := pos[1]
		key, column := eval.OptString(opts, "key", ""), eval.OptString(opts, "column", spec.column)
		batch, limit := eval.OptInt(opts, "batch", mapDefaultBatch), eval.OptInt(opts, "limit", mapDefaultLimit)
		if batch < 1 || limit < 1 {
			return nil, fmt.Errorf("%s: :batch and :limit must be positive", spec.op)
		}

		items, err := c.Eval.RankInput(spec.op, input)
		if err != nil {
			return nil, err
		}
		if len(items) == 0 {
			return []eval.Value{}, nil
		}
		if len(items) > limit {
			return nil, fmt.Errorf("%s: %d items exceeds the cost guard of %d completions' worth — narrow the input (| take %d) or raise :limit",
				spec.op, len(items), limit, limit)
		}
		texts := make([]string, len(items))
		for i, item := range items {
			if texts[i], err = eval.RankText(spec.op, item, key); err != nil {
				return nil, err
			}
		}

		replies := make([]string, len(items))
		for start := 0; start < len(items); start += batch {
			if eval.Interrupted() {
				return nil, eval.ErrInterrupted
			}
			end := min(start+batch, len(items))
			if err := c.mapBatch(spec, string(instruction), texts[start:end], replies[start:end]); err != nil {
				return nil, err
			}
		}

		out := make([]eval.Value, len(items))
		for i, item := range items {
			row := eval.Dictionary{}
			if d, ok := item.(eval.Dictionary); ok {
				maps.Copy(row, d)
			} else if s, ok := item.(eval.String); ok {
				row["item"] = s
			} else {
				row["item"] = eval.String(eval.PrintValue(item))
			}
			row[column] = eval.String(replies[i])
			out[i] = row
		}
		return out, nil
	}
}

func (c *Chat) mapBatch(spec mapSpec, instruction string, texts, replies []string) error {
	var b strings.Builder
	b.WriteString(instruction)
	b.WriteString("\n\n")
	for i, t := range texts {
		fmt.Fprintf(&b, "%d. %s\n", i+1, flattenItem(t))
	}
	reply, err := c.plainCompletion(spec.system, b.String())
	if err != nil {
		return fmt.Errorf("%s: %w", spec.op, err)
	}
	if parsed, ok := parseNumbered(reply, len(texts)); ok {
		copy(replies, parsed)
		return nil
	}
	for i, t := range texts {
		if eval.Interrupted() {
			return eval.ErrInterrupted
		}
		reply, err := c.plainCompletion(spec.itemSystem, instruction+"\n\n"+flattenItem(t))
		if err != nil {
			return fmt.Errorf("%s: %w", spec.op, err)
		}
		replies[i] = firstCommandLine(reply)
	}
	return nil
}

var numberedReply = regexp.MustCompile(`^\s*(\d+)[.):]\s*(.+)$`)

func parseNumbered(reply string, n int) ([]string, bool) {
	replies := make([]string, n)
	for line := range strings.SplitSeq(reply, "\n") {
		m := numberedReply.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		idx, err := strconv.Atoi(m[1])
		if err != nil || idx < 1 || idx > n || replies[idx-1] != "" {
			return nil, false
		}
		replies[idx-1] = strings.TrimSpace(m[2])
	}
	if slices.Contains(replies, "") {
		return nil, false
	}
	return replies, true
}

func flattenItem(s string) string {
	return strings.Join(strings.Fields(s), " ")
}
