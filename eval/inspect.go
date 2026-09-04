package eval

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
)

func inspectName(v Value, form string) (string, error) {
	switch n := v.(type) {
	case Symbol:
		return string(n), nil
	case String:
		return string(n), nil
	default:
		return "", fmt.Errorf("%s expects a name, got %s", form, PrintValue(v))
	}
}

func inspectBuiltins(env *Environment, ev *Evaluator) {
	Register("inspect", "show a definition's source, reconstructed from the live closure", CommandMeta{Command: true, MinArgs: 1, MaxArgs: 1, Usage: "name"})
	env.SetSpecialForm("inspect", func(args []Value, env *Environment, e *Evaluator) (Value, error) {
		if len(args) != 1 {
			return nil, errors.New("inspect expects a name: (inspect fibonacci)")
		}
		name, err := inspectName(args[0], "inspect")
		if err != nil {
			return nil, err
		}
		v, err := env.Lookup(name)
		if err != nil {
			if _, ok := env.LookupSpecialForm(name); ok {
				return nil, fmt.Errorf("inspect: %s is a special form implemented in Go — no Scheme source", name)
			}
			if _, ok := env.LookupMacro(name); ok {
				return nil, fmt.Errorf("inspect: %s is a macro implemented in Go — no Scheme source", name)
			}
			return nil, err
		}
		switch val := v.(type) {
		case *Lambda:
			if val.env != ev.globalEnv {
				return nil, fmt.Errorf("inspect: %s closes over local state — its source alone would not reproduce it", name)
			}
			return String(formatDefineLambda(name, val)), nil
		case BuiltinFunc, *FastBuiltin:
			return nil, fmt.Errorf("inspect: %s is a builtin implemented in Go — no Scheme source", name)
		default:
			return String("(define " + name + " " + printSource(v) + ")"), nil
		}
	})

	Register("persist", "promote a definition to the prelude via a sub-agent", CommandMeta{Command: true, MinArgs: 1, MaxArgs: 1, Usage: "name"})
	env.SetMacro("persist", func(args []Value) (Value, error) {
		if len(args) != 1 {
			return nil, errors.New("persist expects a name: (persist fibonacci)")
		}
		name, err := inspectName(args[0], "persist")
		if err != nil {
			return nil, err
		}
		instruction := fmt.Sprintf(
			"Persist the following Scheme definition into ~/.rf.scm (the rf prelude, evaluated at every shell startup). "+
				"Read the file first if it exists. If it already contains a definition of %s, replace that definition with this one; "+
				"otherwise append it at the end of the file (create the file if missing). "+
				"Preserve all other content exactly. Reply with a one-line confirmation of what you did.", name)
		return []Value{
			Symbol("begin"),
			[]Value{
				Symbol("agent"),
				String(instruction),
				[]Value{Symbol("inspect"), args[0]},
			},
			Symbol("true"),
		}, nil
	})
}

func formatDefineLambda(name string, lam *Lambda) string {
	var sb strings.Builder
	sb.WriteString("(define (" + name)
	for _, p := range lam.params {
		sb.WriteString(" " + string(p))
	}
	if lam.rest != "" {
		sb.WriteString(" . " + string(lam.rest))
	}
	sb.WriteString(")")
	for _, form := range lam.src {
		sb.WriteString("\n  " + printSource(form))
	}
	sb.WriteString(")")
	return sb.String()
}

func printSource(v Value) string {
	switch val := v.(type) {
	case String:
		return strconv.Quote(string(val))
	case []Value:
		parts := make([]string, len(val))
		for i, elem := range val {
			parts[i] = printSource(elem)
		}
		return "(" + strings.Join(parts, " ") + ")"
	case Dictionary:
		parts := make([]string, 0, len(val))
		for k, v := range val {
			parts = append(parts, ":"+k+" "+printSource(v))
		}
		return "{" + strings.Join(parts, " ") + "}"
	default:
		return PrintValue(v)
	}
}
