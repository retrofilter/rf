package eval

import (
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"strconv"
	"strings"
	"unicode/utf8"
)

func parserText(op string, v Value, approval *approvalGate) (string, error) {
	switch in := normalizeList(v).(type) {
	case String:
		path := expandHome(string(in))
		if err := approval.requireRead(fmt.Sprintf("%s %q", op, path), path); err != nil {
			return "", err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return "", fmt.Errorf("%s: %v (the input string names a file — use (lines \"text\") to parse literal text)", op, err)
		}
		return string(data), nil
	case *Stream:
		if !in.Lines() {
			return "", fmt.Errorf("%s expects text, not rows — the input is already structured", op)
		}
		s, _, err := AsString(in)
		return s, err
	case []Value:
		parts := make([]string, len(in))
		for i, item := range in {
			s, ok := item.(String)
			if !ok {
				return "", fmt.Errorf("%s: list input must be strings (lines of text), got %s", op, PrintValue(item))
			}
			parts[i] = string(s)
		}
		return strings.Join(parts, "\n"), nil
	}
	return "", fmt.Errorf("%s: input must be a file path, a stream, or a list of strings, got %s", op, PrintValue(v))
}

func parserReader(op string, v Value, approval *approvalGate) (r io.Reader, closefn func() error, userCode bool, err error) {
	switch in := normalizeList(v).(type) {
	case String:
		path := expandHome(string(in))
		if err := approval.requireRead(fmt.Sprintf("%s %q", op, path), path); err != nil {
			return nil, nil, false, err
		}
		f, err := os.Open(path)
		if err != nil {
			return nil, nil, false, fmt.Errorf("%s: %v (the input string names a file — use (lines \"text\") to parse literal text)", op, err)
		}
		return f, f.Close, false, nil
	case *Stream:
		if !in.Lines() {
			return nil, nil, false, fmt.Errorf("%s expects text, not rows — the input is already structured", op)
		}
		return in.TextReader(), in.Close, in.userCode, nil
	case []Value:
		text, err := parserText(op, in, approval)
		if err != nil {
			return nil, nil, false, err
		}
		return strings.NewReader(text), nil, false, nil
	}
	return nil, nil, false, fmt.Errorf("%s: input must be a file path, a stream, or a list of strings, got %s", op, PrintValue(v))
}

func fromJSON(v any) Value {
	switch val := v.(type) {
	case map[string]any:
		dict := make(Dictionary, len(val))
		for k, item := range val {
			dict[k] = fromJSON(item)
		}
		return dict
	case []any:
		lst := make([]Value, len(val))
		for i, item := range val {
			lst[i] = fromJSON(item)
		}
		return lst
	case string:
		return String(val)
	case float64:
		if val == math.Trunc(val) && val >= math.MinInt64 && val <= math.MaxInt64 {
			return Integer(int64(val))
		}
		return Number(val)
	case bool:
		return val
	}
	return nil
}

func csvCell(s string) Value {
	if n, err := strconv.ParseInt(s, 10, 64); err == nil {
		if strconv.FormatInt(n, 10) == s {
			return Integer(n)
		}
	} else if f, err := strconv.ParseFloat(s, 64); err == nil {
		if strconv.FormatFloat(f, 'f', -1, 64) == s {
			return Number(f)
		}
	}
	return String(s)
}

func csvSep(v Value) (rune, error) {
	s, ok := keyName(v)
	if !ok {
		return 0, errors.New("from-csv: :sep expects a single-character string (or \"tab\")")
	}
	if s == "tab" || s == "\\t" {
		return '\t', nil
	}
	if utf8.RuneCountInString(s) != 1 {
		return 0, errors.New("from-csv: :sep expects a single-character string (or \"tab\")")
	}
	r, _ := utf8.DecodeRuneInString(s)
	return r, nil
}

func csvHeader(rec []string) []string {
	names := make([]string, len(rec))
	seen := map[string]bool{}
	for i, cell := range rec {
		name := strings.TrimSpace(cell)
		if i == 0 {
			name = strings.TrimPrefix(name, "\ufeff")
		}
		if name == "" {
			name = fmt.Sprintf("c%d", i+1)
		}
		base := name
		for n := 2; seen[name]; n++ {
			name = fmt.Sprintf("%s_%d", base, n)
		}
		seen[name] = true
		names[i] = name
	}
	return names
}

func deserializeBuiltins(env *Environment, approval *approvalGate) {
	Register("parse-json", "parse JSON (a file, stream, or lines) into Scheme values — fetch url | parse-json",
		CommandMeta{Command: true, MinArgs: 1, MaxArgs: 1, Stage: true, Usage: "[input]"})
	env.Set("parse-json", BuiltinFunc(func(args []Value, env *Environment) (Value, error) {
		if len(args) != 1 {
			return nil, errors.New("parse-json expects 1 argument: (parse-json input) — a file path, or pipe text in")
		}
		text, err := parserText("parse-json", args[0], approval)
		if err != nil {
			return nil, err
		}
		dec := json.NewDecoder(strings.NewReader(text))
		var vals []Value
		for {
			var raw any
			if err := dec.Decode(&raw); err == io.EOF {
				break
			} else if err != nil {
				return nil, fmt.Errorf("parse-json: %v", err)
			}
			vals = append(vals, fromJSON(raw))
		}
		if len(vals) == 0 {
			return nil, errors.New("parse-json: empty input")
		}
		if len(vals) == 1 {
			return vals[0], nil
		}
		return vals, nil
	}))

	Register("from-csv", "parse CSV into rows — from-csv data.csv --sep \";\"",
		CommandMeta{Command: true, MinArgs: 1, MaxArgs: 1, Stage: true, Usage: "[input]",
			Options: []Option{
				{Long: "sep", Short: "s", Kind: OptionString, Placeholder: "CHAR", Doc: "field separator: a character, or the name tab"},
				{Long: "noheader", Kind: OptionBool, Doc: "no header record — columns become c1, c2, ..."},
			}})
	env.Set("from-csv", BuiltinFunc(func(args []Value, env *Environment) (Value, error) {
		pos, opts, err := ParseOptions("from-csv", args)
		if err != nil {
			return nil, err
		}
		if len(pos) != 1 {
			return nil, errors.New("from-csv expects one input: (from-csv input [{:sep \";\" :noheader #t}]) — a file path, or pipe text in")
		}
		input := pos[0]
		sep := ','
		if v, ok := opts["sep"]; ok {
			if sep, err = csvSep(v); err != nil {
				return nil, err
			}
		}
		noheader := OptBool(opts, "noheader")
		reader, closefn, userCode, err := parserReader("from-csv", input, approval)
		if err != nil {
			return nil, err
		}
		release := func() error {
			if closefn != nil {
				return closefn()
			}
			return nil
		}
		r := csv.NewReader(reader)
		r.Comma = sep
		r.FieldsPerRecord = -1
		var columns []string
		if !noheader {
			rec, err := r.Read()
			if err == io.EOF {
				_ = release()
				return []Value{}, nil
			}
			if err != nil {
				_ = release()
				return nil, fmt.Errorf("from-csv: %v", err)
			}
			columns = csvHeader(rec)
		}
		next := func() (Value, bool, error) {
			rec, err := r.Read()
			if err == io.EOF {
				return nil, false, nil
			}
			if err != nil {
				return nil, false, fmt.Errorf("from-csv: %v", err)
			}
			row := make(Dictionary, len(rec))
			for i, cell := range rec {
				name := ""
				if i < len(columns) {
					name = columns[i]
				} else {
					name = fmt.Sprintf("c%d", i+1)
				}
				row[name] = csvCell(cell)
			}
			return row, true, nil
		}
		out := newStream(next, release)
		out.userCode = userCode
		return out, nil
	}))
}
