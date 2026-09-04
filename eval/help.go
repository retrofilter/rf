package eval

import (
	"errors"
	"fmt"
	"sort"
	"strings"
)

func helpBuiltins(env *Environment) {
	Register("help", "a command's usage and flags, or all commands as rows",
		CommandMeta{Command: true, MaxArgs: 1, Usage: "[command]"})
	env.Set("help", BuiltinFunc(func(args []Value, env *Environment) (Value, error) {
		if len(args) == 0 {
			return commandRows(), nil
		}
		if len(args) > 1 {
			return nil, errors.New("help expects at most one command name: (help \"history\")")
		}
		name, ok := args[0].(String)
		if !ok {
			return nil, errors.New("help expects a command name string: (help \"history\")")
		}
		text, found := CommandHelp(string(name))
		if !found {
			return nil, fmt.Errorf("help: no builtin named %q — `help` lists the command words, (builtins) everything", string(name))
		}
		return streamFromReader(strings.NewReader(text), nil), nil
	}))
}

// CommandHelp returns the text `help name` (and `name -h`) prints;
// ok=false for unregistered names. Exported for the handbook generator,
// which shares this renderer so generated docs cannot drift from -h.
func CommandHelp(name string) (string, bool) {
	registryMu.RLock()
	info, found := registry[name]
	registryMu.RUnlock()
	if !found {
		return "", false
	}
	return helpText(name, info), true
}

func commandRows() []Value {
	names := CommandWords()
	sort.Strings(names)
	rows := make([]Value, 0, len(names))
	for _, n := range names {
		rows = append(rows, Dictionary{"command": String(n), "doc": String(BuiltinDoc(n))})
	}
	return rows
}

func helpText(name string, info builtinInfo) string {
	var b strings.Builder
	b.WriteString(name)
	if info.doc != "" {
		b.WriteString(" — " + info.doc)
	}
	b.WriteString("\n\n")

	usage := name
	if info.meta.Usage != "" {
		usage += " " + info.meta.Usage
	}
	if len(info.meta.Options) > 0 {
		usage += " [flags]"
	}
	b.WriteString("usage:  " + usage + "\n")
	b.WriteString("scheme: " + schemeSpelling(name, info.meta) + "\n")

	if len(info.meta.Options) > 0 {
		b.WriteString("\n")
		rows := make([][2]string, 0, len(info.meta.Options)+1)
		for _, o := range info.meta.Options {
			rows = append(rows, [2]string{flagSpelling(o), o.Doc})
		}
		rows = append(rows, [2]string{"-h, --help", "this help"})
		width := 0
		for _, r := range rows {
			width = max(width, len(r[0]))
		}
		for _, r := range rows {
			b.WriteString(fmt.Sprintf("  %-*s  %s\n", width, r[0], r[1]))
		}
	}
	return strings.TrimSuffix(b.String(), "\n")
}

func flagSpelling(o Option) string {
	s := "    "
	if o.Short != "" {
		s = "-" + o.Short + ", "
	}
	s += "--" + o.Long
	if o.Kind != OptionBool && o.Placeholder != "" {
		s += " " + o.Placeholder
	}
	return s
}

func schemeSpelling(name string, meta CommandMeta) string {
	parts := []string{name}
	if meta.Usage != "" {
		parts = append(parts, meta.Usage)
	}
	if len(meta.Options) > 0 {
		kv := make([]string, len(meta.Options))
		for i, o := range meta.Options {
			val := o.Placeholder
			if o.Kind == OptionBool {
				val = "#t"
			} else if val == "" {
				val = "..."
			}
			kv[i] = ":" + o.Long + " " + val
		}
		parts = append(parts, "[{"+strings.Join(kv, " ")+"}]")
	}
	return "(" + strings.Join(parts, " ") + ")"
}

// Synopsis exposes a command's two usage spellings — the command-mode
// line and the parenthesized Scheme call — for renderers outside the
// package (the man pages). Same construction as helpText.
func Synopsis(name string) (usage, scheme string, ok bool) {
	registryMu.RLock()
	info, found := registry[name]
	registryMu.RUnlock()
	if !found {
		return "", "", false
	}
	usage = name
	if info.meta.Usage != "" {
		usage += " " + info.meta.Usage
	}
	if len(info.meta.Options) > 0 {
		usage += " [flags]"
	}
	return usage, schemeSpelling(name, info.meta), true
}

// FlagUsage is the exported flagSpelling — one option's "-d, --dir PATH"
// column, for the man pages' OPTIONS section.
func FlagUsage(o Option) string {
	return strings.TrimSpace(flagSpelling(o))
}
