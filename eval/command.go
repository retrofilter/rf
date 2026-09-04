package eval

import "sync"

// CommandMeta describes how a builtin participates in command mode, as a
// command word, a pipeline stage word, or both; it is declared with Register
// beside the builtin and consumed by the shell's reader.
type CommandMeta struct {
	// Command makes the name a standalone command word: a line starting
	// with it runs the builtin instead of a system binary.
	Command bool

	// Stage makes the name a pipeline stage word (valid after a `|`).
	Stage bool

	// MinArgs/MaxArgs bound a standalone invocation's argument count; MaxArgs -1
	// means unbounded.
	MinArgs, MaxArgs int

	// Instruction passes the text after the name (and any leading flags) as one
	// verbatim string argument: `agent summarize the Makefile` runs (agent
	// "summarize the Makefile").
	Instruction bool

	// Globs marks a builtin whose path arguments accept glob expansion: an
	// unquoted *?[] token desugars to a (glob "pattern") form, so the builtin
	// must accept a list of paths wherever it accepts one.
	Globs bool

	// Operators lists dash-words the command-mode flag reader passes through as
	// positional arguments instead of parsing as flags — where's test(1)
	// comparisons (`where size -gt 100MB`).
	Operators []string

	// Usage is the positional synopsis help prints after the name —
	// "[pattern]", "\"query\" [input]" — with [flags] appended when the
	// command declares options.
	Usage string

	// Options declares the command's optional parameters — the single source for
	// command-mode flags, the Scheme options dict, and help.
	Options []Option
}

// Option is one declared optional parameter. Its Long name is both the
// command-mode --flag and the Scheme dict key, so translation between the
// two surfaces is mechanical.
type Option struct {
	Long        string     // --long flag and :long dict key
	Short       string     // one-letter -s spelling; "" = long-only
	Kind        OptionKind // value type; OptionBool takes no value
	Placeholder string     // help's display for the value (PATH, N, COL)
	Doc         string     // one-line description in help output
}

// OptionKind is the value type an option carries. Numeric kinds coerce
// numeric strings, matching command mode's everything-is-a-string words.
type OptionKind int

const (
	OptionBool OptionKind = iota
	OptionString
	OptionInt
	OptionNumber
)

type builtinInfo struct {
	doc  string
	meta CommandMeta
}

var (
	registryMu sync.RWMutex
	registry   = map[string]builtinInfo{}
)

// Register records a builtin's one-line documentation and command-mode
// capabilities, called beside its definition (Set/SetSpecialForm/ SetMacro).
func Register(name, doc string, meta CommandMeta) {
	registryMu.Lock()
	registry[name] = builtinInfo{doc: doc, meta: meta}
	registryMu.Unlock()
}

// SetBuiltin binds a builtin function and registers its documentation —
// the common case for builtins with no command-mode presence.
func (env *Environment) SetBuiltin(name, doc string, fn BuiltinFunc) {
	env.Set(name, fn)
	Register(name, doc, CommandMeta{})
}

// LookupCommand returns a name's registered command metadata. ok reports
// only that the name is registered; check the Command/Stage fields for
// what the word may do in command mode.
func LookupCommand(name string) (CommandMeta, bool) {
	registryMu.RLock()
	defer registryMu.RUnlock()
	info, ok := registry[name]
	return info.meta, ok
}

// BuiltinDoc returns a builtin's registered one-line documentation, or ""
// when the name is unregistered (or undocumented).
func BuiltinDoc(name string) string {
	registryMu.RLock()
	defer registryMu.RUnlock()
	return registry[name].doc
}

// CommandWords returns the names registered as standalone command words,
// unsorted — the shell's tab completer merges candidate sets and sorts.
func CommandWords() []string {
	return registeredNames(func(m CommandMeta) bool { return m.Command })
}

// StageWords returns the names registered as pipeline stage words,
// unsorted.
func StageWords() []string {
	return registeredNames(func(m CommandMeta) bool { return m.Stage })
}

// RegisteredWords returns every registered name — command words, stage
// words, and the plain Scheme library — unsorted.
func RegisteredWords() []string {
	return registeredNames(func(CommandMeta) bool { return true })
}

func registeredNames(pred func(CommandMeta) bool) []string {
	registryMu.RLock()
	defer registryMu.RUnlock()
	var names []string
	for name, info := range registry {
		if pred(info.meta) {
			names = append(names, name)
		}
	}
	return names
}
