package eval

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/pmezard/go-difflib/difflib"
)

func diffBuiltins(env *Environment, approval *approvalGate) {
	// No command or stage capability: diff shadows a system binary.
	Register("diff", "compare two inputs as {:op :line :text} change rows (:text for unified diff)", CommandMeta{})
	env.Set("diff", BuiltinFunc(func(args []Value, env *Environment) (Value, error) {
		var inputs []Value
		asText := false
		for _, arg := range args {
			if kw, isKw := arg.(Keyword); isKw {
				if string(kw) != "text" {
					return nil, fmt.Errorf("diff: unknown option :%s (supported: :text)", string(kw))
				}
				asText = true
				continue
			}
			inputs = append(inputs, arg)
		}
		if len(inputs) != 2 {
			return nil, errors.New("diff expects 2 inputs: (diff old new [:text])")
		}
		// Both sides gate as one prompt — a two-file diff is one action.
		var paths []string
		for _, in := range inputs {
			if s, isStr := in.(String); isStr {
				paths = append(paths, expandHome(string(s)))
			}
		}
		if len(paths) > 0 {
			if err := approval.requireRead(readAction("diff", paths), paths...); err != nil {
				return nil, err
			}
		}
		a, aName, err := diffLines(inputs[0], "old")
		if err != nil {
			return nil, err
		}
		b, bName, err := diffLines(inputs[1], "new")
		if err != nil {
			return nil, err
		}
		if asText {
			return diffText(a, b, aName, bName)
		}
		return diffRows(a, b), nil
	}))
}

func diffLines(v Value, fallback string) ([]string, string, error) {
	switch in := normalizeList(v).(type) {
	case String:
		path := expandHome(string(in))
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, "", fmt.Errorf("diff: %v (the input string names a file — use (lines \"text\") to diff literal text)", err)
		}
		text := strings.TrimSuffix(string(data), "\n")
		if text == "" {
			return nil, path, nil
		}
		return strings.Split(text, "\n"), path, nil
	case *Stream:
		lst, _, err := AsList(in)
		if err != nil {
			return nil, "", err
		}
		return renderedLines(lst), fallback, nil
	case []Value:
		return renderedLines(in), fallback, nil
	default:
		return nil, "", fmt.Errorf("diff: input must be a file path, a stream, or a list, got %s", PrintValue(v))
	}
}

func renderedLines(lst []Value) []string {
	lines := make([]string, len(lst))
	for i, v := range lst {
		lines[i] = lineText(v)
	}
	return lines
}

func diffRows(a, b []string) Value {
	rows := []Value{}
	for _, oc := range difflib.NewMatcher(a, b).GetOpCodes() {
		if oc.Tag == 'e' {
			continue
		}
		for i := oc.I1; i < oc.I2; i++ {
			rows = append(rows, Dictionary{"op": String("-"), "line": Integer(i + 1), "text": String(a[i])})
		}
		for j := oc.J1; j < oc.J2; j++ {
			rows = append(rows, Dictionary{"op": String("+"), "line": Integer(j + 1), "text": String(b[j])})
		}
	}
	return rows
}

func diffText(a, b []string, aName, bName string) (Value, error) {
	terminate := func(lines []string) []string {
		out := make([]string, len(lines))
		for i, l := range lines {
			out[i] = l + "\n"
		}
		return out
	}
	s, err := difflib.GetUnifiedDiffString(difflib.UnifiedDiff{
		A:        terminate(a),
		FromFile: aName,
		B:        terminate(b),
		ToFile:   bName,
		Context:  3,
	})
	if err != nil {
		return nil, err
	}
	return String(s), nil
}
