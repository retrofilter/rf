package eval

import (
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
)

func keyName(v Value) (string, bool) {
	switch k := v.(type) {
	case String:
		return string(k), true
	case Keyword:
		return string(k), true
	case Symbol:
		return string(k), true
	}
	return "", false
}

func fieldValue(op string, item Value, key string) (Value, bool, error) {
	dict, isDict := item.(Dictionary)
	if !isDict {
		return nil, false, fmt.Errorf("%s: item has no columns (not a row): %s", op, PrintValue(item))
	}
	v, present := dictPath(dict, key)
	return v, present, nil
}

func dictPath(dict Dictionary, key string) (Value, bool) {
	if v, present := dict[key]; present {
		return v, true
	}
	parent, leaf, dotted := strings.Cut(key, ".")
	if !dotted {
		return nil, false
	}
	sub, isDict := dict[parent].(Dictionary)
	if !isDict {
		return nil, false
	}
	v, present := sub[leaf]
	return v, present
}

var sizeSuffixes = map[string]float64{
	"k": 1 << 10, "kb": 1 << 10, "kib": 1 << 10,
	"m": 1 << 20, "mb": 1 << 20, "mib": 1 << 20,
	"g": 1 << 30, "gb": 1 << 30, "gib": 1 << 30,
	"t": 1 << 40, "tb": 1 << 40, "tib": 1 << 40,
}

func looseNumber(v Value) (float64, bool) {
	switch n := v.(type) {
	case Integer:
		return float64(n), true
	case Number:
		return float64(n), true
	case String:
		s := strings.TrimSpace(string(n))
		if f, err := strconv.ParseFloat(s, 64); err == nil {
			return f, true
		}
		for i := len(s) - 3; i < len(s); i++ {
			if i < 1 {
				continue
			}
			if mult, ok := sizeSuffixes[strings.ToLower(s[i:])]; ok {
				if f, err := strconv.ParseFloat(s[:i], 64); err == nil {
					return f * mult, true
				}
			}
		}
	}
	return 0, false
}

func looseCompare(a, b Value) (cmp int, ordered bool) {
	if an, aok := looseNumber(a); aok {
		if bn, bok := looseNumber(b); bok {
			switch {
			case an < bn:
				return -1, true
			case an > bn:
				return 1, true
			}
			return 0, true
		}
	}
	_, aStr := a.(String)
	_, bStr := b.(String)
	return strings.Compare(textCell(a), textCell(b)), aStr && bStr
}

var whereOps = map[string]func(cmp int, ordered bool) bool{
	"=":   func(c int, _ bool) bool { return c == 0 },
	"==":  func(c int, _ bool) bool { return c == 0 },
	"-eq": func(c int, _ bool) bool { return c == 0 },
	"!=":  func(c int, _ bool) bool { return c != 0 },
	"-ne": func(c int, _ bool) bool { return c != 0 },
	"<":   func(c int, o bool) bool { return o && c < 0 },
	"-lt": func(c int, o bool) bool { return o && c < 0 },
	"<=":  func(c int, o bool) bool { return o && c <= 0 },
	"-le": func(c int, o bool) bool { return o && c <= 0 },
	">":   func(c int, o bool) bool { return o && c > 0 },
	"-gt": func(c int, o bool) bool { return o && c > 0 },
	">=":  func(c int, o bool) bool { return o && c >= 0 },
	"-ge": func(c int, o bool) bool { return o && c >= 0 },
}

var whereOperatorWords = []string{"-gt", "-lt", "-ge", "-le", "-eq", "-ne"}

func filterStream(src *Stream, keep func(Value) (bool, error)) *Stream {
	next := func() (Value, bool, error) {
		for {
			v, ok, err := src.Next()
			if err != nil || !ok {
				return nil, false, err
			}
			k, err := keep(v)
			if err != nil {
				_ = src.Close()
				return nil, false, err
			}
			if k {
				return v, true, nil
			}
		}
	}
	s := newStream(next, src.Close)
	s.lines = src.Lines()
	s.userCode = src.userCode
	return s
}

func mapStream(src *Stream, f func(Value) (Value, error)) *Stream {
	next := func() (Value, bool, error) {
		v, ok, err := src.Next()
		if err != nil || !ok {
			return nil, false, err
		}
		out, err := f(v)
		if err != nil {
			_ = src.Close()
			return nil, false, err
		}
		return out, true, nil
	}
	s := newStream(next, src.Close)
	s.lines = src.Lines()
	s.userCode = src.userCode
	return s
}

func whereClause(keyArg, op, value Value, env *Environment) (func(Value) (bool, error), bool, error) {
	key, ok := keyName(keyArg)
	if !ok {
		return nil, false, errors.New("where: key must be a column name (string, keyword, or symbol)")
	}
	if isCallable(op) {
		return func(item Value) (bool, error) {
			field, present, err := fieldValue("where", item, key)
			if err != nil || !present {
				return false, err
			}
			res, err := callFunction(op, []Value{field, value}, env)
			if err != nil {
				return false, err
			}
			return !isFalsy(res), nil
		}, true, nil
	}
	name, _ := keyName(op)
	judge, known := whereOps[name]
	if !known {
		return nil, false, fmt.Errorf("where: unknown operator %s (supported: = != -gt -lt -ge -le, or < <= > >= in parens)", PrintValue(op))
	}
	return func(item Value) (bool, error) {
		field, present, err := fieldValue("where", item, key)
		if err != nil || !present {
			return false, err
		}
		cmp, ordered := looseCompare(field, value)
		return judge(cmp, ordered), nil
	}, false, nil
}

func rowsBuiltins(env *Environment, approval *approvalGate) {
	whereUsage := errors.New("where expects (where key op value [and/or key op value ...] input) or (where pred input) — pipe rows in: dir | where type = file and size -gt 100MB")
	Register("where", "filter rows by field comparisons (= != -gt -lt -ge -le, chained with and/or) or a predicate — dir | where type = file and size -gt 100MB",
		CommandMeta{Command: true, MaxArgs: -1, Stage: true, Operators: whereOperatorWords, Usage: "key op value [and/or key op value ...]"})
	env.Set("where", BuiltinFunc(func(args []Value, env *Environment) (Value, error) {
		if len(args) < 2 {
			return nil, whereUsage
		}
		input := args[len(args)-1]
		words := args[:len(args)-1]
		var keep func(Value) (bool, error)
		usesScheme := false
		if len(words) == 1 {
			pred := words[0]
			if !isCallable(pred) {
				return nil, whereUsage
			}
			usesScheme = true
			keep = func(item Value) (bool, error) {
				res, err := callFunction(pred, []Value{item}, env)
				if err != nil {
					return false, err
				}
				return !isFalsy(res), nil
			}
		} else {
			var orGroups [][]func(Value) (bool, error)
			var group []func(Value) (bool, error)
			for i := 0; ; i += 4 {
				if len(words)-i < 3 {
					return nil, whereUsage
				}
				clause, schemey, err := whereClause(words[i], words[i+1], words[i+2], env)
				if err != nil {
					return nil, err
				}
				usesScheme = usesScheme || schemey
				group = append(group, clause)
				if i+3 == len(words) {
					break
				}
				conn, _ := keyName(words[i+3])
				switch strings.ToLower(conn) {
				case "and":
				case "or":
					orGroups = append(orGroups, group)
					group = nil
				default:
					return nil, fmt.Errorf("where: expected and/or between comparisons, got %s", PrintValue(words[i+3]))
				}
			}
			orGroups = append(orGroups, group)
			keep = func(item Value) (bool, error) {
				for _, g := range orGroups {
					pass := true
					for _, clause := range g {
						ok, err := clause(item)
						if err != nil {
							return false, err
						}
						if !ok {
							pass = false
							break
						}
					}
					if pass {
						return true, nil
					}
				}
				return false, nil
			}
		}
		switch in := normalizeList(input).(type) {
		case *Stream:
			env.markCaptured()
			s := filterStream(in, keep)
			if usesScheme {
				s.userCode = true
			}
			return s, nil
		case []Value:
			out := []Value{}
			for _, item := range in {
				ok, err := keep(item)
				if err != nil {
					return nil, err
				}
				if ok {
					out = append(out, item)
				}
			}
			return out, nil
		}
		return nil, whereUsage
	}))

	Register("sort-by", "sort rows by a column, or items by themselves — ls | sort-by size -r",
		CommandMeta{Command: true, MaxArgs: -1, Stage: true, Usage: "[key]",
			Options: []Option{{Long: "desc", Short: "r", Kind: OptionBool, Doc: "reverse: biggest or latest first"}}})
	env.Set("sort-by", BuiltinFunc(func(args []Value, env *Environment) (Value, error) {
		rest, opts, err := ParseOptions("sort-by", args)
		if err != nil {
			return nil, err
		}
		desc := OptBool(opts, "desc")
		key := ""
		var input Value
		switch len(rest) {
		case 1:
			input = rest[0]
		case 2:
			k, ok := keyName(rest[0])
			if !ok {
				return nil, errors.New("sort-by: key must be a column name (string or symbol)")
			}
			key, input = k, rest[1]
		default:
			return nil, errors.New("sort-by expects ([key] [{:desc #t}] input) — pipe rows in: ls | sort-by size -r")
		}
		items, err := rankInput("sort-by", input, approval)
		if err != nil {
			return nil, err
		}
		keys := make([]sortKey, len(items))
		for i, item := range items {
			v, present := item, true
			if key != "" {
				if v, present, err = fieldValue("sort-by", item, key); err != nil {
					return nil, err
				}
			}
			keys[i] = sortKeyFor(v, present)
		}
		idx := make([]int, len(items))
		for i := range idx {
			idx[i] = i
		}
		sort.SliceStable(idx, func(i, j int) bool {
			if desc {
				return sortKeyLess(keys[idx[j]], keys[idx[i]])
			}
			return sortKeyLess(keys[idx[i]], keys[idx[j]])
		})
		out := make([]Value, len(items))
		for i, o := range idx {
			out[i] = items[o]
		}
		return out, nil
	}))

	Register("group-by", "group rows by a column into a {value: rows} dictionary — history | group-by project | keys",
		CommandMeta{Command: true, MaxArgs: -1, Stage: true, Usage: "key"})
	env.Set("group-by", BuiltinFunc(func(args []Value, env *Environment) (Value, error) {
		if len(args) != 2 {
			return nil, errors.New("group-by expects (group-by key input) — pipe rows in: history | group-by project")
		}
		key, ok := keyName(args[0])
		if !ok {
			return nil, errors.New("group-by: key must be a column name (string, keyword, or symbol)")
		}
		items, err := rankInput("group-by", args[1], approval)
		if err != nil {
			return nil, err
		}
		groups := Dictionary{}
		for _, item := range items {
			v, present, err := fieldValue("group-by", item, key)
			if err != nil {
				return nil, err
			}
			name := ""
			if present {
				name = textCell(v)
			}
			members, _ := groups[name].([]Value)
			groups[name] = append(members, item)
		}
		return groups, nil
	}))

	Register("count-by", "count rows per column value — history | count-by project",
		CommandMeta{Command: true, MaxArgs: -1, Stage: true, Usage: "[key]"})
	env.Set("count-by", BuiltinFunc(func(args []Value, env *Environment) (Value, error) {
		if len(args) < 1 || len(args) > 2 {
			return nil, errors.New("count-by expects ([key] input) — pipe rows in: history | count-by project")
		}
		key, column := "", "value"
		input := args[len(args)-1]
		if len(args) == 2 {
			k, ok := keyName(args[0])
			if !ok {
				return nil, errors.New("count-by: key must be a column name (string, keyword, or symbol)")
			}
			key, column = k, k
		}
		items, err := rankInput("count-by", input, approval)
		if err != nil {
			return nil, err
		}
		counts := map[string]int{}
		first := map[string]Value{}
		var order []string
		for _, item := range items {
			v := item
			if key != "" {
				var present bool
				if v, present, err = fieldValue("count-by", item, key); err != nil {
					return nil, err
				}
				if !present {
					v = String("")
				}
			}
			name := textCell(v)
			if counts[name] == 0 {
				first[name] = v
				order = append(order, name)
			}
			counts[name]++
		}
		sort.SliceStable(order, func(i, j int) bool {
			if counts[order[i]] != counts[order[j]] {
				return counts[order[i]] > counts[order[j]]
			}
			return order[i] < order[j]
		})
		rows := make([]Value, len(order))
		for i, name := range order {
			rows[i] = Dictionary{column: first[name], "count": Integer(counts[name])}
		}
		return rows, nil
	}))

	Register("pick", "keep only the named columns — ls | pick name size",
		CommandMeta{Command: true, MaxArgs: -1, Stage: true, Usage: "key ..."})
	env.Set("pick", BuiltinFunc(func(args []Value, env *Environment) (Value, error) {
		if len(args) < 2 {
			return nil, errors.New("pick expects (pick key... input) — pipe rows in: ls | pick name size")
		}
		names := make([]string, len(args)-1)
		for i, a := range args[:len(args)-1] {
			k, ok := keyName(a)
			if !ok {
				return nil, errors.New("pick: keys must be column names (strings, keywords, or symbols)")
			}
			names[i] = k
		}
		pickRow := func(item Value) (Value, error) {
			dict, isDict := item.(Dictionary)
			if !isDict {
				return nil, fmt.Errorf("pick: item has no columns (not a row): %s", PrintValue(item))
			}
			out := make(Dictionary, len(names))
			for _, name := range names {
				if v, ok := dictPath(dict, name); ok {
					out[name] = v
				}
			}
			for k, v := range dict {
				if strings.HasPrefix(k, "_") {
					out[k] = v
				}
			}
			return out, nil
		}
		switch in := normalizeList(args[len(args)-1]).(type) {
		case Dictionary:
			return pickRow(in)
		case *Stream:
			return mapStream(in, pickRow), nil
		case []Value:
			out := make([]Value, len(in))
			for i, item := range in {
				row, err := pickRow(item)
				if err != nil {
					return nil, err
				}
				out[i] = row
			}
			return out, nil
		}
		return nil, errors.New("pick expects rows last — pipe rows in: ls | pick name size")
	}))
}

type sortKey struct {
	missing bool
	rank    int
	num     float64
	text    string
}

func sortKeyFor(v Value, present bool) sortKey {
	if !present {
		return sortKey{missing: true}
	}
	if n, ok := looseNumber(v); ok {
		return sortKey{rank: 0, num: n}
	}
	if s, ok := v.(String); ok {
		return sortKey{rank: 1, text: string(s)}
	}
	return sortKey{rank: 2, text: PrintValue(v)}
}

func sortKeyLess(a, b sortKey) bool {
	switch {
	case a.missing || b.missing:
		return b.missing && !a.missing
	case a.rank != b.rank:
		return a.rank < b.rank
	case a.rank == 0:
		return a.num < b.num
	}
	return a.text < b.text
}
