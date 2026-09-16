package eval

import (
	"testing"

	"github.com/retrofilter/rf/core"
	"github.com/stretchr/testify/require"
)

func TestCommandRegistryCoherence(t *testing.T) {
	db := createTestDB(t)
	defer db.Close()
	ev := NewEvaluatorWithEnvironment(db, core.NewGraphStore(db))
	env := ev.globalEnv

	bound := func(name string) bool {
		if _, err := env.Lookup(name); err == nil {
			return true
		}
		if _, ok := env.LookupMacro(name); ok {
			return true
		}
		_, ok := env.LookupSpecialForm(name)
		return ok
	}

	lateNames := map[string]bool{
		"llm": true, "agent": true, "usage": true,
		"configure": true, "resume": true, "compact": true,
	}

	for _, name := range append(CommandWords(), StageWords()...) {
		if !bound(name) {
			t.Errorf("registered command/stage word %q is not bound in the environment", name)
		}
	}

	commands := map[string]bool{}
	for _, name := range CommandWords() {
		commands[name] = true
	}
	stages := map[string]bool{}
	for _, name := range StageWords() {
		stages[name] = true
	}
	for _, name := range []string{"if", "then", "else", "elif", "fi", "for", "while", "until",
		"do", "done", "case", "esac", "in", "function", "select", "time", "coproc"} {
		if meta, ok := LookupCommand(name); ok && (meta.Command || meta.Stage) {
			t.Errorf("%q is an sh reserved word and must not be a command or stage word", name)
		}
	}
	for _, name := range []string{
		"dir", "similar", "fetch", "chunk", "where", "sort-by",
		"group-by", "count-by", "pick", "project", "projects",
		"register-project", "unregister-project",
		"tree", "trees", "create-tree", "delete-tree",
		"task", "tasks",
		"history", "remember", "recall", "builtins", "aliases", "unalias",
		"inspect", "persist", "help",
	} {
		if !commands[name] {
			t.Errorf("%q must be registered as a command word", name)
		}
	}
	for _, name := range []string{
		"similar", "chunk", "get", "keys", "table", "length",
		"take", "write-file", "append-file", "json", "text",
	} {
		if !stages[name] {
			t.Errorf("%q must be registered as a stage word", name)
		}
	}

	for _, name := range []string{
		"ls", "cat", "stat", "cp", "mv", "rm", "mkdir", "which",
		"basename", "dirname", "pwd", "wc", "env", "grep", "sh",
		"ps", "head", "diff",
	} {
		if commands[name] || stages[name] {
			t.Errorf("%q shadows a system binary and must not be a command or stage word", name)
		}
	}
	for name := range lateNames {
		if commands[name] || stages[name] {
			t.Errorf("%q registers from its own package; drop it from lateNames", name)
		}
	}

	// Spot-check the metadata the reader leans on.
	meta, ok := LookupCommand("dir")
	require.True(t, ok)
	require.True(t, meta.Globs)
	require.Equal(t, 1, meta.MaxArgs)
	meta, _ = LookupCommand("remember")
	require.Equal(t, -1, meta.MaxArgs)
	meta, ok = LookupCommand("glob")
	require.False(t, ok && meta.Command, "glob must not be a command word (reaches /bin/sh)")
}

func TestBuiltinDocsCoverage(t *testing.T) {
	db := createTestDB(t)
	defer db.Close()
	ev := NewEvaluatorWithEnvironment(db, core.NewGraphStore(db))
	env := ev.globalEnv

	var names []string
	for name, c := range env.bindings {
		switch c.val.(type) {
		case BuiltinFunc, *FastBuiltin:
			names = append(names, name)
		}
	}
	for name := range env.forms {
		names = append(names, name)
	}

	require.NotEmpty(t, names)
	for _, name := range names {
		if BuiltinDoc(name) == "" {
			t.Errorf("builtin %q has no documentation string (use SetBuiltin, or Register beside its definition)", name)
		}
	}
}
