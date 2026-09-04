package eval

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

func serializeBuiltins(env *Environment) {
	Register("json", "serialize a value as JSON lines", CommandMeta{Stage: true})
	env.Set("json", BuiltinFunc(func(args []Value, env *Environment) (Value, error) {
		if len(args) != 1 {
			return nil, errors.New("json expects 1 argument: (json value)")
		}
		return serializeValue(args[0], jsonLine)
	}))

	Register("text", "serialize a value as plain text lines — rows become TSV", CommandMeta{Stage: true})
	env.Set("text", BuiltinFunc(func(args []Value, env *Environment) (Value, error) {
		if len(args) != 1 {
			return nil, errors.New("text expects 1 argument: (text value)")
		}
		return serializeValue(args[0], textLine)
	}))
}

func serializeValue(v Value, render func(items []Value, lazy *Stream) (Value, error)) (Value, error) {
	switch src := normalizeList(v).(type) {
	case *Stream:
		if src.Lines() {
			return render(nil, src)
		}
		lst, _, err := AsList(src)
		if err != nil {
			return nil, err
		}
		return render(lst, nil)
	case []Value:
		return render(src, nil)
	case String:
		items := make([]Value, 0)
		for line := range strings.SplitSeq(strings.TrimSuffix(string(src), "\n"), "\n") {
			items = append(items, String(line))
		}
		return render(items, nil)
	default:
		return render([]Value{v}, nil)
	}
}

func jsonLine(items []Value, lazy *Stream) (Value, error) {
	encode := func(v Value) (Value, error) {
		b, err := json.Marshal(jsonable(v))
		if err != nil {
			return nil, fmt.Errorf("json: %v", err)
		}
		return String(b), nil
	}
	if lazy != nil {
		return mapLineStream(lazy, encode), nil
	}
	out := make([]Value, len(items))
	for i, item := range items {
		line, err := encode(item)
		if err != nil {
			return nil, err
		}
		out[i] = line
	}
	return streamFromList(out, true), nil
}

func textLine(items []Value, lazy *Stream) (Value, error) {
	if lazy != nil {
		return lazy, nil
	}
	rows := make([]Dictionary, 0, len(items))
	allDicts := true
	for _, item := range items {
		dict, ok := item.(Dictionary)
		if !ok {
			allDicts = false
			break
		}
		rows = append(rows, dict)
	}
	out := make([]Value, len(items))
	if allDicts && len(rows) > 0 {
		columns := tableColumns(rows)
		for i, row := range rows {
			cells := make([]string, len(columns))
			for c, col := range columns {
				if v, ok := row[col]; ok {
					cells[c] = textCell(v)
				}
			}
			out[i] = String(strings.Join(cells, "\t"))
		}
	} else {
		for i, item := range items {
			out[i] = String(textCell(item))
		}
	}
	return streamFromList(out, true), nil
}

func textCell(v Value) string {
	if s, ok := v.(String); ok {
		return string(s)
	}
	return PrintValue(v)
}

func jsonable(v Value) any {
	switch val := v.(type) {
	case nil:
		return nil
	case *Pair:
		lst, err := pairToSlice(val)
		if err != nil {
			return PrintValue(val)
		}
		arr := make([]any, len(lst))
		for i, item := range lst {
			arr[i] = jsonable(item)
		}
		return arr
	case Integer:
		return int64(val)
	case Number:
		return float64(val)
	case String:
		return string(val)
	case Keyword:
		return string(val)
	case Symbol:
		return string(val)
	case bool:
		return val
	case Dictionary:
		m := make(map[string]any, len(val))
		for k, item := range val {
			m[k] = jsonable(item)
		}
		return m
	case []Value:
		arr := make([]any, len(val))
		for i, item := range val {
			arr[i] = jsonable(item)
		}
		return arr
	default:
		return PrintValue(val)
	}
}

func mapLineStream(src *Stream, f func(Value) (Value, error)) *Stream {
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
	out := newLineStream(next, src.Close)
	out.userCode = src.userCode
	return out
}
