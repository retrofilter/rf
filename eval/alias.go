package eval

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
)

type aliasTable struct {
	mu sync.RWMutex
	m  map[string]string
}

// LookupAlias returns the expansion for a command-mode alias.
func (e *Evaluator) LookupAlias(name string) (string, bool) {
	e.aliases.mu.RLock()
	defer e.aliases.mu.RUnlock()
	exp, ok := e.aliases.m[name]
	return exp, ok
}

// AliasNames returns every defined command-mode alias name, unsorted —
// the shell's tab completer merges them with other candidate sets.
func (e *Evaluator) AliasNames() []string {
	e.aliases.mu.RLock()
	defer e.aliases.mu.RUnlock()
	names := make([]string, 0, len(e.aliases.m))
	for name := range e.aliases.m {
		names = append(names, name)
	}
	return names
}

const aliasWordMeta = "*?[]{}()<>|&;$`\\'\"" + " \t"

func aliasName(v Value, form string) (string, error) {
	var name string
	switch n := v.(type) {
	case Symbol:
		name = string(n)
	case String:
		name = string(n)
	default:
		return "", fmt.Errorf("%s expects a name, got %s", form, PrintValue(v))
	}
	if name == "" || strings.ContainsAny(name, aliasWordMeta) {
		return "", fmt.Errorf("alias name %q must be a plain command word", name)
	}
	return name, nil
}

func aliasBuiltins(env *Environment, ev *Evaluator) {
	Register("alias", "define a command-mode alias — (alias ll \"ls -la\")", CommandMeta{})
	env.SetSpecialForm("alias", func(args []Value, env *Environment, e *Evaluator) (Value, error) {
		if err := ev.RequireUser("alias"); err != nil {
			return nil, err
		}
		if len(args) != 2 {
			return nil, errors.New("alias expects 2 arguments: (alias name \"expansion\")")
		}
		name, err := aliasName(args[0], "alias")
		if err != nil {
			return nil, err
		}
		expVal, err := e.Eval(args[1], env)
		if err != nil {
			return nil, err
		}
		exp, ok := expVal.(String)
		if !ok {
			return nil, errors.New("alias expects a string expansion")
		}
		expansion := strings.TrimSpace(string(exp))
		if expansion == "" {
			return nil, errors.New("alias expansion cannot be empty")
		}
		if strings.HasPrefix(expansion, "(") || strings.HasPrefix(expansion, "!") {
			return nil, fmt.Errorf("alias expansion %q is command text, not Scheme — define a function instead", expansion)
		}
		ev.aliases.mu.Lock()
		defer ev.aliases.mu.Unlock()
		ev.aliases.m[name] = expansion
		return nil, nil
	})

	Register("unalias", "remove a command-mode alias", CommandMeta{Command: true, MinArgs: 1, MaxArgs: 1, Usage: "name"})
	env.SetSpecialForm("unalias", func(args []Value, env *Environment, e *Evaluator) (Value, error) {
		if err := ev.RequireUser("unalias"); err != nil {
			return nil, err
		}
		if len(args) != 1 {
			return nil, errors.New("unalias expects 1 argument: (unalias name)")
		}
		name, err := aliasName(args[0], "unalias")
		if err != nil {
			return nil, err
		}
		ev.aliases.mu.Lock()
		defer ev.aliases.mu.Unlock()
		if _, exists := ev.aliases.m[name]; !exists {
			return nil, fmt.Errorf("unalias: %s is not an alias", name)
		}
		delete(ev.aliases.m, name)
		return nil, nil
	})

	Register("aliases", "command-mode aliases as rows", CommandMeta{Command: true})
	env.Set("aliases", BuiltinFunc(func(args []Value, env *Environment) (Value, error) {
		if len(args) != 0 {
			return nil, errors.New("aliases expects no arguments")
		}
		ev.aliases.mu.RLock()
		defer ev.aliases.mu.RUnlock()
		names := make([]string, 0, len(ev.aliases.m))
		for name := range ev.aliases.m {
			names = append(names, name)
		}
		sort.Strings(names)
		rows := make([]Value, len(names))
		for i, name := range names {
			rows[i] = Dictionary{"name": String(name), "expansion": String(ev.aliases.m[name])}
		}
		return rows, nil
	}))
}
