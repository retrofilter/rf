package eval

import (
	"errors"
	"fmt"
	"math"
	"os"
	"os/exec"
	"reflect"
	"strings"
	"sync/atomic"
	"time"

	"github.com/jmoiron/sqlx"
	"github.com/retrofilter/rf/core"
)

// ErrInterrupted is returned by Eval when the user presses ctrl+c while a
// form is running (the shell's SIGINT handler calls Interrupt). Callers
// treat it like bash treats ^C: abandon the command, keep the shell.
var ErrInterrupted = errors.New("interrupted")

var interrupted atomic.Bool

// Interrupt requests that any running evaluation stop: the eval loop (and
// long-blocking builtins like sleep) poll the flag and return ErrInterrupted.
func Interrupt() { interrupted.Store(true) }

// ClearInterrupt re-arms evaluation after an interrupt has been handled;
// the shell calls it before dispatching each new line.
func ClearInterrupt() { interrupted.Store(false) }

// Interrupted reports whether an interrupt is pending.
func Interrupted() bool { return interrupted.Load() }

type Value interface{}

type Symbol string

type Number float64

type String string

type Keyword string

// Char is a Scheme character — one Unicode code point. Reader syntax
// #\a, #\newline, #\x41. Self-evaluating.
type Char rune

// Vector is a Scheme vector, reader syntax #(1 2 3).
type Vector []Value

// Bytevector is a Scheme bytevector, reader syntax #u8(0 255). Self-evaluating.
type Bytevector []byte

// Dictionary is a map from Value to Value
// (for simplicity, keys are usually Keyword or Symbol)
type Dictionary map[string]Value

type BuiltinFunc func(args []Value, env *Environment) (Value, error)

type MacroFunc func(args []Value) (Value, error)

// SpecialFormFunc receives the evaluator itself rather than an eval callback:
// passing the bound method allocated a closure per dispatch, and forms need e
// to enter tail calls anyway.
type SpecialFormFunc func(args []Value, env *Environment, e *Evaluator) (Value, error)

// Environment is goroutine-confined: every read and write happens on the
// goroutine driving the evaluator, so lookups take no locks. New cross-
// goroutine paths must bring their own confinement, not a lock here.
type Environment struct {
	paramNames []Symbol
	vals       []Value

	bindings map[string]*cell
	forms    map[string]formEntry
	parent   *Environment

	ev *Evaluator

	formShadows int

	macroEnvs map[uint64]*Environment

	libraries map[string]*librarySpec

	captured bool
	fresh    bool

	shadow *Environment
}

func (root *Environment) markFallbackEnv(name string) (string, *Environment) {
	orig, envID, ok := unmark(name)
	if !ok {
		return "", nil
	}
	if envID == 0 {
		return orig, root
	}
	return orig, root.macroEnvs[envID]
}

func NewEnvironment(parent *Environment) *Environment {
	return &Environment{parent: parent}
}

func (env *Environment) Lookup(name string) (Value, error) {
	for i, p := range env.paramNames {
		if string(p) == name {
			return env.vals[i], nil
		}
	}
	if env.bindings != nil {
		if c, ok := env.bindings[name]; ok {
			return c.val, nil
		}
	}
	if env.parent != nil {
		return env.parent.Lookup(name)
	}
	if orig, fb := env.markFallbackEnv(name); fb != nil {
		return fb.Lookup(orig)
	}
	return nil, fmt.Errorf("unbound symbol: %s", symBase(name))
}

func (env *Environment) Set(name string, val Value) {
	for i, p := range env.paramNames {
		if string(p) == name {
			env.vals[i] = val
			return
		}
	}
	if c, existed := env.bindings[name]; existed {
		c.val = val
		return
	}
	if env.bindings == nil {
		env.bindings = make(map[string]*cell)
	}
	if _, inForms := env.forms[name]; inForms {
		env.formShadows++
	}
	env.bindings[name] = &cell{val: val}
}

func (env *Environment) SetScoped(name string, val Value) error {
	for i, p := range env.paramNames {
		if string(p) == name {
			env.vals[i] = val
			return nil
		}
	}
	if c, ok := env.bindings[name]; ok {
		c.val = val
		return nil
	}
	if env.parent != nil {
		return env.parent.SetScoped(name, val)
	}
	if orig, fb := env.markFallbackEnv(name); fb != nil {
		return fb.SetScoped(orig, val)
	}
	return fmt.Errorf("unbound variable: %s", symBase(name))
}

type formEntry struct {
	sf  SpecialFormFunc
	mac MacroFunc
	cf  compileFunc
	tag string
}

func (f formEntry) isSpecialForm() bool { return f.cf != nil || f.sf != nil }

func (env *Environment) LookupMacro(name string) (MacroFunc, bool) {
	if entry, ok := env.forms[name]; ok && entry.mac != nil {
		return entry.mac, true
	}
	if env.parent != nil {
		return env.parent.LookupMacro(name)
	}
	return nil, false
}

func (env *Environment) SetMacro(name string, macro MacroFunc) {
	env.setForm(name, func(entry *formEntry) { entry.mac = macro })
}

func (env *Environment) setForm(name string, update func(*formEntry)) {
	if env.forms == nil {
		env.forms = make(map[string]formEntry)
	}
	entry, existed := env.forms[name]
	if !existed {
		if _, bound := env.bindings[name]; bound {
			env.formShadows++
		}
	}
	update(&entry)
	env.forms[name] = entry
}

func (env *Environment) LookupSpecialForm(name string) (SpecialFormFunc, bool) {
	if entry, ok := env.forms[name]; ok && entry.isSpecialForm() {
		if entry.sf != nil {
			return entry.sf, true
		}
		return func(args []Value, env *Environment, e *Evaluator) (Value, error) {
			form := append([]Value{Symbol(name)}, args...)
			return e.Eval(form, env)
		}, true
	}
	if env.parent != nil {
		return env.parent.LookupSpecialForm(name)
	}
	return nil, false
}

// SetSpecialForm registers a fallback special form. Registering over a
// natively compiled name replaces the compiler: the latest registration
// wins.
func (env *Environment) SetSpecialForm(name string, sf SpecialFormFunc) {
	env.setForm(name, func(entry *formEntry) {
		entry.sf = sf
		entry.cf = nil
		entry.tag = ""
	})
}

// Evaluator holds the global environment and evaluation settings
// (timeout, resource limits, etc.)
type Evaluator struct {
	globalEnv *Environment
	timeout   time.Duration
	db        *sqlx.DB
	gs        *core.GraphStore
	approval  *approvalGate
	aliases   *aliasTable
	caller    string // CallerUser (default) or CallerAssistant; see SetCaller

	inFlightHistory int64

	sessionID string

	foreground ForegroundRunner

	confirm ApprovalFunc

	tc TailCall

	frames *framePool

	ccache *compileCache

	handlers []Value
}

func (e *Evaluator) tail(expr Value, env *Environment) Value {
	e.tc.Node = nil
	e.tc.Expr = expr
	e.tc.Env = env
	return &e.tc
}

func (e *Evaluator) tailFrame(n node, env *Environment) Value {
	env.fresh = true
	e.tc.Node = n
	e.tc.Expr = nil
	e.tc.Env = env
	return &e.tc
}

func NewEvaluator() *Evaluator {
	return NewEvaluatorWithEnvironment(nil, nil)
}

func NewEvaluatorWithEnvironment(db *sqlx.DB, gs *core.GraphStore) *Evaluator {
	global := NewEnvironment(nil)
	approval := &approvalGate{}
	global.Set("*1", nil)
	global.Set("*2", nil)
	global.Set("*3", nil)
	addBuiltins(global)
	pairBuiltins(global)
	listBuiltins(global)
	addSpecialForms(global)
	mathBuiltins(global)
	stdlibBuiltins(global)
	charBuiltins(global)
	vectorBuiltins(global)
	bytevectorBuiltins(global)
	mutstringBuiltins(global)
	recordBuiltins(global)
	promiseBuiltins(global)
	valuesBuiltins(global)
	controlBuiltins(global)
	syntaxBuiltins(global)
	libraryBuiltins(global)
	shellBuiltins(global, approval)
	jobBuiltins(global, approval)
	psBuiltins(global)
	projectBuiltins(global, approval)
	similarBuiltins(global, approval)
	bm25Builtins(global, approval)
	hybridBuiltins(global, approval)
	chunkBuiltins(global, approval)
	serializeBuiltins(global)
	deserializeBuiltins(global, approval)
	rowsBuiltins(global, approval)
	diffBuiltins(global, approval)
	fetchBuiltins(global, approval)
	portBuiltins(global, approval)
	helpBuiltins(global)
	fastBuiltins(global)
	// Native compilers for the core forms (compile.go).
	compileForms(global)

	ev := &Evaluator{
		globalEnv: global,
		timeout:   5 * time.Second,
		db:        db,
		gs:        gs,
		approval:  approval,
		aliases:   &aliasTable{m: make(map[string]string)},
		frames:    &framePool{},
		ccache:    &compileCache{m: make(map[astKey][]compiledEntry)},
	}
	global.ev = ev
	if db != nil {
		historyBuiltins(global, ev, db)
		messagesBuiltins(global, db)
	}
	if gs != nil {
		graphBuiltins(global, ev, gs)
		registryBuiltins(global, gs)
		taskBuiltins(global, ev)
	}
	execBuiltin(global, ev)
	envBuiltin(global, ev)
	evalBuiltins(global, ev)
	processContextBuiltins(global, ev)
	aliasBuiltins(global, ev)
	inspectBuiltins(global, ev)
	exceptionBuiltins(global, ev)
	approvalForms(global, ev)
	return ev
}

// SetInFlightHistory records which history row holds the line currently
// executing.
func (e *Evaluator) SetInFlightHistory(id int64) {
	e.inFlightHistory = id
}

// SetSessionID records the shell session's id for provenance stamps (the
// task builtin). The shell loop sets it once at startup, sharing the id
// its history and session rows carry.
func (e *Evaluator) SetSessionID(id string) {
	e.sessionID = id
}

// SetApprover installs (or, with nil, removes) the function consulted by
// destructive builtins (rm, mv, sh) before they act. With no approver set,
// those builtins run ungated.
func (e *Evaluator) SetApprover(fn ApprovalFunc) {
	e.approval.fn = fn
}

// Approver returns the currently installed approval function (nil when
// ungated).
func (e *Evaluator) Approver() ApprovalFunc {
	return e.approval.fn
}

// SetConfirmer installs the function irreversible builtins (delete-graph)
// consult for an explicit go-ahead.
func (e *Evaluator) SetConfirmer(fn ApprovalFunc) {
	e.confirm = fn
}

// Confirmer returns the currently installed confirmation function (nil
// when none — fail closed, never prompt-free).
func (e *Evaluator) Confirmer() ApprovalFunc {
	return e.confirm
}

// ForegroundRunner runs a terminal-attached child as the foreground process
// group and returns its exit code; the shell installs one so exec joins its
// job control. line is the display form for job listings.
type ForegroundRunner func(cmd *exec.Cmd, line string) (int, error)

// SetForegroundRunner installs (or, with nil, removes) the shell's
// foreground-child runner.
func (e *Evaluator) SetForegroundRunner(fn ForegroundRunner) {
	e.foreground = fn
}

// Caller identities for SetCaller/RequireUser.
const (
	CallerUser      = "user"
	CallerAssistant = "assistant"
)

// SetCaller records who initiated the current evaluation. The zero value
// means CallerUser: code typed at the prompt never has to set it.
func (e *Evaluator) SetCaller(caller string) {
	e.caller = caller
}

// Caller returns the current caller identity.
func (e *Evaluator) Caller() string {
	if e.caller == "" {
		return CallerUser
	}
	return e.caller
}

// RequireUser errors unless the current caller is the user — for
// interactive builtins (configure, resume, compact) that only the person at
// the terminal may run.
func (e *Evaluator) RequireUser(name string) error {
	if e.Caller() != CallerUser {
		return fmt.Errorf("%s is interactive and can only be run by the user at the prompt", name)
	}
	return nil
}

// Lambda represents a user-defined function (closure).
type Lambda struct {
	params []Symbol
	rest   Symbol
	frame  []Symbol
	body   node
	env    *Environment
	src    []Value
	shadow *Environment
}

func parseFormals(v Value) (params []Symbol, rest Symbol, err error) {
	switch f := v.(type) {
	case Symbol:
		return nil, f, nil
	case []Value:
		params = make([]Symbol, len(f))
		for i, p := range f {
			sym, ok := p.(Symbol)
			if !ok {
				return nil, "", errors.New("lambda: all parameters must be symbols")
			}
			params[i] = sym
		}
		return params, "", nil
	case *Pair:
		cur := Value(f)
		for {
			p, ok := cur.(*Pair)
			if !ok {
				break
			}
			sym, ok := p.Car.(Symbol)
			if !ok {
				return nil, "", errors.New("lambda: all parameters must be symbols")
			}
			params = append(params, sym)
			cur = p.Cdr
		}
		sym, ok := cur.(Symbol)
		if !ok {
			return nil, "", errors.New("lambda: dotted formals must end in a rest symbol")
		}
		return params, sym, nil
	}
	return nil, "", errors.New("lambda: first argument must be a list of symbols")
}

// IsUserFunction reports whether a value is a user-defined function (a
// closure from define/lambda), as opposed to a Go-registered builtin.
func IsUserFunction(v Value) bool {
	_, ok := v.(*Lambda)
	return ok
}

// UserFunctionNames returns the names bound to user-defined functions
// (define/lambda closures) anywhere in this environment chain — the words
// command mode would dispatch via tryUserCommand.
func (env *Environment) UserFunctionNames() []string {
	var names []string
	for e := env; e != nil; e = e.parent {
		for i, p := range e.paramNames {
			if IsUserFunction(e.vals[i]) && !strings.ContainsRune(string(p), markByte) {
				names = append(names, string(p))
			}
		}
		for name, c := range e.bindings {
			if IsUserFunction(c.val) && !strings.ContainsRune(name, markByte) {
				names = append(names, name)
			}
		}
	}
	return names
}

// TailCall is the value a tail-position call returns to the run loop:
// Node runs in Env. A nil Node carries an uncompiled Expr — the shape
// fallback special forms produce through tail — compiled on arrival.
type TailCall struct {
	Node node
	Expr Value
	Env  *Environment
}

// Eval compiles expr against env's scope and runs it (compile.go). The
// shell and the LLM tool loop enter here per form; fallback special forms
// enter here per subform.
func (e *Evaluator) Eval(expr Value, env *Environment) (Value, error) {
	n, err := e.compileIn(expr, env, true)
	if err != nil {
		return nil, err
	}
	return e.run(n, env)
}

func notCallable(v Value) error {
	s, _ := PrintValueCapped(v, 200)
	if _, isDict := v.(Dictionary); isDict {
		return fmt.Errorf("not a function: %s — dictionaries aren't callable; index with (get :key dict)", s)
	}
	return fmt.Errorf("not a function: %s", s)
}

func addBuiltins(env *Environment) {
	env.Set("true", true)
	env.Set("false", false)
	env.SetBuiltin("+", "the sum of its arguments", BuiltinFunc(func(args []Value, env *Environment) (Value, error) {
		var sum Value = Integer(0)
		for _, arg := range args {
			next, ok := numAdd2(sum, arg)
			if !ok {
				return nil, errors.New("+ expects numbers")
			}
			sum = next
		}
		return sum, nil
	}))
	env.SetBuiltin("-", "subtract successive arguments; (- x) negates", BuiltinFunc(func(args []Value, env *Environment) (Value, error) {
		if len(args) == 0 {
			return nil, errors.New("- expects at least one argument")
		}
		if !isNumber(args[0]) {
			return nil, errors.New("- expects numbers")
		}
		if len(args) == 1 {
			neg, _ := numSub2(Integer(0), args[0])
			return neg, nil
		}
		res := args[0]
		for _, arg := range args[1:] {
			next, ok := numSub2(res, arg)
			if !ok {
				return nil, errors.New("- expects numbers")
			}
			res = next
		}
		return res, nil
	}))
	env.SetBuiltin("*", "the product of its arguments", BuiltinFunc(func(args []Value, env *Environment) (Value, error) {
		var prod Value = Integer(1)
		for _, arg := range args {
			next, ok := numMul2(prod, arg)
			if !ok {
				return nil, errors.New("* expects numbers")
			}
			prod = next
		}
		return prod, nil
	}))
	env.SetBuiltin("/", "division across arguments; exact when it divides evenly; (/ x) is the reciprocal", BuiltinFunc(func(args []Value, env *Environment) (Value, error) {
		if len(args) == 0 {
			return nil, errors.New("/ expects at least one argument")
		}
		if !isNumber(args[0]) {
			return nil, errors.New("/ expects numbers")
		}
		if len(args) == 1 {
			return numDiv2(Integer(1), args[0])
		}
		res := args[0]
		for _, arg := range args[1:] {
			if !isNumber(arg) {
				return nil, errors.New("/ expects numbers")
			}
			next, err := numDiv2(res, arg)
			if err != nil {
				return nil, err
			}
			res = next
		}
		return res, nil
	}))
	env.SetBuiltin("car", "the first element of a pair or list", BuiltinFunc(func(args []Value, env *Environment) (Value, error) {
		if len(args) != 1 {
			return nil, errors.New("car expects 1 argument")
		}
		if p, ok := args[0].(*Pair); ok {
			return p.Car, nil
		}
		lst, ok, err := AsList(args[0])
		if err != nil {
			return nil, err
		}
		if !ok || len(lst) == 0 {
			return nil, errors.New("car expects a non-empty list")
		}
		return lst[0], nil
	}))
	env.SetBuiltin("cdr", "the rest of a pair or list", BuiltinFunc(func(args []Value, env *Environment) (Value, error) {
		if len(args) != 1 {
			return nil, errors.New("cdr expects 1 argument")
		}
		if p, ok := args[0].(*Pair); ok {
			return p.Cdr, nil
		}
		lst, ok, err := AsList(args[0])
		if err != nil {
			return nil, err
		}
		if !ok || len(lst) == 0 {
			return nil, errors.New("cdr expects a non-empty list")
		}
		return lst[1:], nil
	}))
	env.SetBuiltin("list", "a fresh (mutable) list of its arguments", BuiltinFunc(func(args []Value, env *Environment) (Value, error) {
		return listFromSlice(args), nil
	}))
	env.SetBuiltin("=", "numeric equality; use equal? for other values", BuiltinFunc(func(args []Value, env *Environment) (Value, error) {
		if len(args) < 2 {
			return nil, errors.New("= expects at least 2 arguments")
		}
		if !isNumber(args[0]) {
			return nil, fmt.Errorf("= expects numbers, got %s — use equal? for other values", PrintValue(args[0]))
		}
		result := true
		for i, arg := range args[1:] {
			eq, ok := numEqual2(args[i], arg)
			if !ok {
				return nil, fmt.Errorf("= expects numbers, got %s — use equal? for other values", PrintValue(arg))
			}
			if !eq {
				result = false
			}
		}
		return result, nil
	}))
	env.SetBuiltin("<", "numeric less-than across arguments", numericCompare("<", func(cmp int) bool { return cmp < 0 }))
	env.SetBuiltin(">", "numeric greater-than across arguments", numericCompare(">", func(cmp int) bool { return cmp > 0 }))
	env.SetBuiltin("<=", "numeric less-or-equal across arguments", numericCompare("<=", func(cmp int) bool { return cmp <= 0 }))
	env.SetBuiltin(">=", "numeric greater-or-equal across arguments", numericCompare(">=", func(cmp int) bool { return cmp >= 0 }))
	env.SetBuiltin("not", "true for false or nil, false for everything else", BuiltinFunc(func(args []Value, env *Environment) (Value, error) {
		if len(args) != 1 {
			return nil, errors.New("not expects 1 argument")
		}
		v := args[0]
		if v == nil {
			return true, nil
		}
		if b, ok := v.(bool); ok {
			return !b, nil
		}
		return false, nil
	}))
	Register("and", "short-circuit: false at the first falsy argument, else the last value; (and) is true", CommandMeta{})
	Register("or", "short-circuit: the first truthy value, else false; (or) is false", CommandMeta{})
	env.SetBuiltin("assert", "error unless a condition holds: (assert cond [message])", BuiltinFunc(func(args []Value, env *Environment) (Value, error) {
		if len(args) < 1 || len(args) > 2 {
			return nil, errors.New("assert expects 1 or 2 arguments: (assert condition [message])")
		}
		if isFalsy(args[0]) {
			msg := "assertion failed"
			if len(args) == 2 {
				if s, ok := args[1].(String); ok {
					msg = string(s)
				} else if s, ok := args[1].(Symbol); ok {
					msg = string(s)
				}
			}
			return nil, errors.New(msg)
		}
		return true, nil
	}))
	// Type predicates
	env.SetBuiltin("null?", "true when the value is an empty list", BuiltinFunc(func(args []Value, env *Environment) (Value, error) {
		if len(args) != 1 {
			return nil, errors.New("null? expects 1 argument")
		}
		lst, ok := args[0].([]Value)
		return ok && len(lst) == 0, nil
	}))
	env.SetBuiltin("list?", "true when the value is a proper list (cycle-safe)", BuiltinFunc(func(args []Value, env *Environment) (Value, error) {
		if len(args) != 1 {
			return nil, errors.New("list? expects 1 argument")
		}
		return isProperList(args[0]), nil
	}))
	env.SetBuiltin("stream?", "true when the value is a stream", BuiltinFunc(func(args []Value, env *Environment) (Value, error) {
		if len(args) != 1 {
			return nil, errors.New("stream? expects 1 argument")
		}
		_, ok := args[0].(*Stream)
		return ok, nil
	}))
	env.SetBuiltin("number?", "true when the value is a number", BuiltinFunc(func(args []Value, env *Environment) (Value, error) {
		if len(args) != 1 {
			return nil, errors.New("number? expects 1 argument")
		}
		return isNumber(args[0]), nil
	}))
	env.SetBuiltin("symbol?", "true when the value is a symbol", BuiltinFunc(func(args []Value, env *Environment) (Value, error) {
		if len(args) != 1 {
			return nil, errors.New("symbol? expects 1 argument")
		}
		_, ok := args[0].(Symbol)
		return ok, nil
	}))
	env.SetBuiltin("string?", "true when the value is a string", BuiltinFunc(func(args []Value, env *Environment) (Value, error) {
		if len(args) != 1 {
			return nil, errors.New("string? expects 1 argument")
		}
		return isString(args[0]), nil
	}))
	env.SetBuiltin("boolean?", "true when the value is a boolean", BuiltinFunc(func(args []Value, env *Environment) (Value, error) {
		if len(args) != 1 {
			return nil, errors.New("boolean? expects 1 argument")
		}
		_, ok := args[0].(bool)
		return ok, nil
	}))

	// Equality
	env.SetBuiltin("eq?", "identity equality on atoms; use equal? for lists", BuiltinFunc(func(args []Value, env *Environment) (Value, error) {
		if len(args) != 2 {
			return nil, errors.New("eq? expects 2 arguments")
		}
		return identityEq(args[0], args[1]), nil
	}))
	env.SetBuiltin("eqv?", "identity equality, with numbers and characters compared by value and exactness", BuiltinFunc(func(args []Value, env *Environment) (Value, error) {
		if len(args) != 2 {
			return nil, errors.New("eqv? expects 2 arguments")
		}
		return eqvEqual(args[0], args[1]), nil
	}))
	env.SetBuiltin("equal?", "deep structural equality", BuiltinFunc(func(args []Value, env *Environment) (Value, error) {
		if len(args) != 2 {
			return nil, errors.New("equal? expects 2 arguments")
		}
		exhausted := false
		eq := deepEqualEx(args[0], args[1], &exhausted)
		if exhausted {
			return nil, errors.New("equal?: list too long or circular to compare (step cap exceeded)")
		}
		return eq, nil
	}))
	env.SetBuiltin("boolean=?", "whether all booleans are the same", BuiltinFunc(func(args []Value, env *Environment) (Value, error) {
		if len(args) < 2 {
			return nil, errors.New("boolean=? expects at least 2 arguments")
		}
		first, ok := args[0].(bool)
		if !ok {
			return nil, errors.New("boolean=? expects booleans")
		}
		result := true
		for _, arg := range args[1:] {
			b, ok := arg.(bool)
			if !ok {
				return nil, errors.New("boolean=? expects booleans")
			}
			if b != first {
				result = false
			}
		}
		return result, nil
	}))
	env.SetBuiltin("symbol=?", "whether all symbols are the same", BuiltinFunc(func(args []Value, env *Environment) (Value, error) {
		if len(args) < 2 {
			return nil, errors.New("symbol=? expects at least 2 arguments")
		}
		first, ok := args[0].(Symbol)
		if !ok {
			return nil, errors.New("symbol=? expects symbols")
		}
		result := true
		for _, arg := range args[1:] {
			s, ok := arg.(Symbol)
			if !ok {
				return nil, errors.New("symbol=? expects symbols")
			}
			if s != first {
				result = false
			}
		}
		return result, nil
	}))

	// List utilities
	Register("length", "the number of elements", CommandMeta{Stage: true})
	env.Set("length", BuiltinFunc(func(args []Value, env *Environment) (Value, error) {
		if len(args) != 1 {
			return nil, errors.New("length expects 1 argument")
		}
		if s, isStream := args[0].(*Stream); isStream {
			lst, _, err := AsList(s)
			if err != nil {
				return nil, err
			}
			return Integer(len(lst)), nil
		}
		n, err := properListLength(args[0])
		if err != nil {
			return nil, err
		}
		return Integer(n), nil
	}))
	env.SetBuiltin("append", "concatenate lists; the last argument may be any tail value", BuiltinFunc(func(args []Value, env *Environment) (Value, error) {
		if len(args) == 0 {
			return []Value{}, nil
		}
		var prefix []Value
		for _, arg := range args[:len(args)-1] {
			lst, ok, err := AsList(arg)
			if err != nil {
				return nil, err
			}
			if !ok {
				return nil, errors.New("append expects all arguments but the last to be lists")
			}
			prefix = append(prefix, lst...)
		}
		last := args[len(args)-1]
		if len(prefix) == 0 {
			return last, nil
		}
		if lst, ok := last.([]Value); ok {
			return append(prefix, lst...), nil
		}
		var tail Value = last
		for i := len(prefix) - 1; i >= 0; i-- {
			tail = &Pair{Car: prefix[i], Cdr: tail}
		}
		return tail, nil
	}))
	env.SetBuiltin("list-ref", "the element at an index: (list-ref lst i)", BuiltinFunc(func(args []Value, env *Environment) (Value, error) {
		if len(args) != 2 {
			return nil, errors.New("list-ref expects 2 arguments: (list-ref list index)")
		}
		lst, ok, err := AsList(args[0])
		if err != nil {
			return nil, err
		}
		if !ok {
			return nil, errors.New("list-ref expects a list as first argument")
		}
		index, ok := numIndex(args[1])
		if !ok {
			return nil, errors.New("list-ref expects a number as second argument")
		}
		if index < 0 || index >= len(lst) {
			return nil, errors.New("list-ref: index out of bounds")
		}
		return lst[index], nil
	}))
	env.SetBuiltin("filter", "keep elements where the predicate returns true: (filter fn lst)", BuiltinFunc(func(args []Value, env *Environment) (Value, error) {
		if len(args) != 2 {
			return nil, errors.New("filter expects 2 arguments: (filter fn list)")
		}
		lst, ok, err := AsList(args[1])
		if err != nil {
			return nil, err
		}
		if !ok {
			return nil, errors.New("filter expects a list as second argument")
		}
		var result []Value
		for _, v := range lst {
			keep, err := callFunction(args[0], []Value{v}, env)
			if err != nil {
				return nil, err
			}
			if b, ok := keep.(bool); ok && b {
				result = append(result, v)
			}
		}
		return result, nil
	}))

	intDivOp := func(name string, op func(a, b int64) int64) BuiltinFunc {
		return func(args []Value, env *Environment) (Value, error) {
			if len(args) != 2 {
				return nil, fmt.Errorf("%s expects 2 arguments", name)
			}
			a, aExact, ok1 := numInt64(args[0])
			b, bExact, ok2 := numInt64(args[1])
			if !ok1 || !ok2 {
				return nil, fmt.Errorf("%s expects integers", name)
			}
			if b == 0 {
				return nil, fmt.Errorf("%s by zero", name)
			}
			res := op(a, b)
			if aExact && bExact {
				return Integer(res), nil
			}
			return Number(float64(res)), nil
		}
	}
	env.SetBuiltin("modulo", "modulo; result takes the sign of the divisor", intDivOp("modulo", func(a, b int64) int64 {
		res := a % b
		if (res < 0 && b > 0) || (res > 0 && b < 0) {
			res += b
		}
		return res
	}))
	env.SetBuiltin("remainder", "integer remainder; result takes the sign of the dividend", intDivOp("remainder", func(a, b int64) int64 {
		return a % b
	}))
	env.SetBuiltin("quotient", "truncated integer division", intDivOp("quotient", func(a, b int64) int64 {
		return a / b
	}))
	env.SetBuiltin("abs", "the absolute value of a number", BuiltinFunc(func(args []Value, env *Environment) (Value, error) {
		if len(args) != 1 {
			return nil, errors.New("abs expects 1 argument")
		}
		switch n := args[0].(type) {
		case Integer:
			if n < 0 {
				neg, _ := numSub2(Integer(0), n) // MinInt64 promotes
				return neg, nil
			}
			return n, nil
		case Number:
			return Number(math.Abs(float64(n))), nil
		}
		return nil, errors.New("abs expects a number")
	}))
	extremum := func(name string, better func(cmp int) bool) BuiltinFunc {
		return func(args []Value, env *Environment) (Value, error) {
			if len(args) == 0 {
				return nil, fmt.Errorf("%s expects at least 1 argument", name)
			}
			best := args[0]
			if !isNumber(best) {
				return nil, fmt.Errorf("%s expects numbers", name)
			}
			anyInexact := false
			for _, arg := range args {
				if _, inexact := arg.(Number); inexact {
					anyInexact = true
				}
			}
			for _, arg := range args[1:] {
				cmp, unordered, ok := numOrder2(arg, best)
				if !ok {
					return nil, fmt.Errorf("%s expects numbers", name)
				}
				if !unordered && better(cmp) {
					best = arg
				}
			}
			if anyInexact {
				inexact, _ := toInexact(best)
				return inexact, nil
			}
			return best, nil
		}
	}
	env.SetBuiltin("max", "the largest of its arguments", extremum("max", func(cmp int) bool { return cmp > 0 }))
	env.SetBuiltin("min", "the smallest of its arguments", extremum("min", func(cmp int) bool { return cmp < 0 }))

	// Type predicates: pair?, procedure?
	env.SetBuiltin("pair?", "true when the value is a pair (a cons cell or non-empty list)", BuiltinFunc(func(args []Value, env *Environment) (Value, error) {
		if len(args) != 1 {
			return nil, errors.New("pair? expects 1 argument")
		}
		if _, ok := args[0].(*Pair); ok {
			return true, nil
		}
		lst, ok := args[0].([]Value)
		return ok && len(lst) > 0, nil
	}))
	env.SetBuiltin("procedure?", "true when the value is callable (builtin, lambda, case-lambda, continuation, parameter)", BuiltinFunc(func(args []Value, env *Environment) (Value, error) {
		if len(args) != 1 {
			return nil, errors.New("procedure? expects 1 argument")
		}
		return isCallable(args[0]), nil
	}))

	Register("reverse", "a new list with the elements in reverse order", CommandMeta{})
	env.Set("reverse", BuiltinFunc(func(args []Value, env *Environment) (Value, error) {
		if len(args) != 1 {
			return nil, errors.New("reverse expects 1 argument")
		}
		lst, ok, err := AsList(args[0])
		if err != nil {
			return nil, err
		}
		if !ok {
			return nil, errors.New("reverse expects a list")
		}
		res := make([]Value, len(lst))
		for i, v := range lst {
			res[len(lst)-1-i] = v
		}
		return res, nil
	}))

	// apply
	env.SetBuiltin("apply", "call fn with a list as its trailing arguments: (apply fn lst)", BuiltinFunc(func(args []Value, env *Environment) (Value, error) {
		if len(args) < 2 {
			return nil, errors.New("apply expects at least 2 arguments: (apply fn arg1 ... list)")
		}
		fn := args[0]
		var callArgs []Value
		for _, arg := range args[1 : len(args)-1] {
			callArgs = append(callArgs, arg)
		}
		lst, ok, err := AsList(args[len(args)-1])
		if err != nil {
			return nil, err
		}
		if !ok {
			return nil, errors.New("apply: last argument must be a list")
		}
		callArgs = append(callArgs, lst...)
		if !isCallable(fn) {
			return nil, errors.New("apply: first argument must be a function")
		}
		if ev := rootOf(env).ev; ev != nil {
			if tc, ok, err := ev.tailApply(fn, callArgs); ok || err != nil {
				return tc, err
			}
		}
		return callFunction(fn, callArgs, env)
	}))

	// File reading builtin: (file "filename")
	env.SetBuiltin("file", "the entire contents of a file as one string", BuiltinFunc(func(args []Value, env *Environment) (Value, error) {
		if len(args) != 1 {
			return nil, errors.New("file expects 1 argument: (file \"filename\")")
		}
		filename, ok := args[0].(String)
		if !ok {
			return nil, errors.New("file expects a string as argument")
		}
		data, err := os.ReadFile(string(filename))
		if err != nil {
			return nil, err
		}
		return String(data), nil
	}))

	// let macro
	Register("let", "bind local variables: (let ((x 1)) body...); named let loops", CommandMeta{})
	env.SetMacro("let", func(args []Value) (Value, error) {
		if len(args) < 2 {
			return nil, errors.New("let: invalid syntax. Expected (let (bindings) body...)")
		}
		name, named := args[0].(Symbol)
		if named {
			args = args[1:]
			if len(args) < 2 {
				return nil, errors.New("let: invalid syntax. Expected (let name (bindings) body...)")
			}
		}
		bindings, ok := args[0].([]Value)
		if !ok {
			return nil, errors.New("let: first argument must be a list of bindings, or a name for named let")
		}
		body := args[1:]

		vars := make([]Value, len(bindings))
		vals := make([]Value, len(bindings))

		for i, binding := range bindings {
			pair, ok := binding.([]Value)
			if !ok || len(pair) != 2 {
				return nil, errors.New("let: each binding must be a (symbol value) pair")
			}
			vars[i] = pair[0]
			vals[i] = pair[1]
		}

		lambda := []Value{Symbol("lambda"), vars}
		lambda = append(lambda, body...)

		if named {
			call := append([]Value{name}, vals...)
			return []Value{Symbol("letrec"), []Value{[]Value{name, lambda}}, call}, nil
		}

		call := append([]Value{lambda}, vals...)
		return call, nil
	})

	Register("cond", "multi-branch conditional: (cond (test expr...) (else ...))", CommandMeta{})
	env.SetMacro("cond", func(args []Value) (Value, error) {
		if len(args) == 0 {
			return nil, nil
		}

		var expander func(clauses []Value) (Value, error)
		expander = func(clauses []Value) (Value, error) {
			if len(clauses) == 0 {
				return nil, nil
			}

			clause, ok := clauses[0].([]Value)
			if !ok {
				return nil, errors.New("cond: clause must be a list")
			}
			if len(clause) == 0 {
				return nil, errors.New("cond: clause cannot be empty")
			}

			// (else ...) clause
			if symIsBase(clause[0], "else") {
				if len(clauses) > 1 {
					return nil, errors.New("cond: else must be the last clause")
				}
				body := clause[1:]
				if len(body) == 0 {
					return nil, errors.New("cond: else clause must have a body")
				}
				if len(body) == 1 {
					return body[0], nil
				}
				return append([]Value{Symbol("begin")}, body...), nil
			}

			// (test => recipient) clause
			if len(clause) == 3 {
				if symIsBase(clause[1], "=>") {
					test := clause[0]
					recipient := clause[2]
					temp := gensym("cond")
					elsePart, err := expander(clauses[1:])
					if err != nil {
						return nil, err
					}
					return []Value{
						Symbol("let"),
						[]Value{[]Value{temp, test}},
						[]Value{
							Symbol("if"), temp,
							[]Value{recipient, temp},
							elsePart,
						},
					}, nil
				}
			}

			test := clause[0]
			body := clause[1:]

			if len(body) == 0 {
				return nil, errors.New("cond: clause requires at least one expression")
			}

			var thenPart Value
			if len(body) == 1 {
				thenPart = body[0]
			} else {
				thenPart = append([]Value{Symbol("begin")}, body...)
			}

			elsePart, err := expander(clauses[1:])
			if err != nil {
				return nil, err
			}

			return []Value{Symbol("if"), test, thenPart, elsePart}, nil
		}

		return expander(args)
	})

	// do macro (Scheme-style)
	Register("do", "loop: (do ((var init step)...) (test result...) body...)", CommandMeta{})
	env.SetMacro("do", func(args []Value) (Value, error) {
		// (do ((var1 init1 step1) ...) (test expr ...) body ...)
		if len(args) < 2 {
			return nil, errors.New("do: invalid syntax. Expected (do ((var init step) ...) (test expr ...) body ...)")
		}
		bindings, ok := args[0].([]Value)
		if !ok {
			return nil, errors.New("do: first argument must be a list of bindings")
		}
		testClause, ok := args[1].([]Value)
		if !ok || len(testClause) < 1 {
			return nil, errors.New("do: second argument must be a list (test expr ...)")
		}
		body := args[2:]

		// Prepare variable names, inits, steps
		varNames := make([]Value, len(bindings))
		inits := make([]Value, len(bindings))
		steps := make([]Value, len(bindings))
		for i, b := range bindings {
			bind, ok := b.([]Value)
			if !ok || (len(bind) != 2 && len(bind) != 3) {
				return nil, errors.New("do: each binding must be (var init [step])")
			}
			varNames[i] = bind[0]
			inits[i] = bind[1]
			if len(bind) == 3 {
				steps[i] = bind[2]
			} else {
				steps[i] = bind[0] // default: var itself (no change)
			}
		}

		// Build the recursive loop as a named let
		loopName := gensym("do-loop")
		letBindings := make([]Value, len(varNames))
		for i := range varNames {
			letBindings[i] = []Value{varNames[i], inits[i]}
		}

		// (if test (begin expr ...) (begin body ... (loop step ...)))
		var stepVals []Value
		for i := range steps {
			stepVals = append(stepVals, steps[i])
		}
		var testExpr Value = testClause[0]
		var resultExprs []Value
		if len(testClause) > 1 {
			resultExprs = testClause[1:]
		} else {
			resultExprs = []Value{nil}
		}
		var testBranch Value
		if len(resultExprs) == 1 {
			testBranch = resultExprs[0]
		} else {
			testBranch = append([]Value{Symbol("begin")}, resultExprs...)
		}
		var elseBranch Value
		if len(body) == 0 {
			elseBranch = append([]Value{loopName}, stepVals...)
		} else {
			elseBranch = append([]Value{Symbol("begin")}, append(body, Value(append([]Value{loopName}, stepVals...)))...)
		}
		loopBody := []Value{Symbol("if"), testExpr, testBranch, elseBranch}

		letrec := []Value{
			Symbol("letrec"),
			[]Value{
				[]Value{loopName, []Value{Symbol("lambda"), varNames, loopBody}},
			},
			append([]Value{loopName}, inits...),
		}
		return letrec, nil
	})

	letrecMacro := func(args []Value) (Value, error) {
		// (letrec ((var1 val1) ...) body ...)
		if len(args) < 2 {
			return nil, errors.New("letrec: invalid syntax. Expected (letrec (bindings) body ...)")
		}
		bindings, ok := args[0].([]Value)
		if !ok {
			return nil, errors.New("letrec: first argument must be a list of bindings")
		}
		body := args[1:]

		// Prepare variable names and values
		varNames := make([]Value, len(bindings))
		vals := make([]Value, len(bindings))
		for i, binding := range bindings {
			pair, ok := binding.([]Value)
			if !ok || len(pair) != 2 {
				return nil, errors.New("letrec: each binding must be a (symbol value) pair")
			}
			varNames[i] = pair[0]
			vals[i] = pair[1]
		}

		letBindings := make([]Value, len(varNames))
		for i := range varNames {
			letBindings[i] = []Value{varNames[i], nil}
		}
		setForms := make([]Value, len(varNames))
		for i := range varNames {
			setForms[i] = []Value{Symbol("set!"), varNames[i], vals[i]}
		}
		allBody := append(setForms, body...)
		letForm := []Value{Symbol("let"), letBindings}
		letForm = append(letForm, allBody...)
		return letForm, nil
	}
	Register("letrec", "bind mutually recursive locals: (letrec ((f (lambda ...))) body)", CommandMeta{})
	env.SetMacro("letrec", letrecMacro)
	Register("letrec*", "bind mutually recursive locals sequentially: (letrec* ((f ...)) body)", CommandMeta{})
	env.SetMacro("letrec*", letrecMacro)
}

func isFalsy(v Value) bool {
	if b, ok := v.(bool); ok {
		return !b // Only false bool is falsy
	}
	return false // Everything else is truthy
}

func numericCompare(name string, admit func(cmp int) bool) BuiltinFunc {
	return func(args []Value, env *Environment) (Value, error) {
		if len(args) < 2 {
			return nil, fmt.Errorf("%s expects at least 2 arguments", name)
		}
		if !isNumber(args[0]) {
			return nil, fmt.Errorf("%s expects numbers, got %s", name, PrintValue(args[0]))
		}
		prev := args[0]
		result := true
		for _, arg := range args[1:] {
			cmp, unordered, ok := numOrder2(prev, arg)
			if !ok {
				return nil, fmt.Errorf("%s expects numbers, got %s", name, PrintValue(arg))
			}
			if unordered || !admit(cmp) {
				result = false
			}
			prev = arg
		}
		return result, nil
	}
}

func deepEqual(a, b Value) bool {
	return deepEqualEx(a, b, nil)
}

func deepEqualEx(a, b Value, exhausted *bool) bool {
	switch aVal := a.(type) {
	case Integer:
		bVal, ok := b.(Integer)
		return ok && aVal == bVal
	case Number:
		bVal, ok := b.(Number)
		return ok && aVal == bVal
	case bool:
		bVal, ok := b.(bool)
		return ok && aVal == bVal
	case Symbol:
		bVal, ok := b.(Symbol)
		return ok && aVal == bVal
	case String:
		bs, ok := stringText(b)
		return ok && string(aVal) == bs
	case *MutableString:
		bs, ok := stringText(b)
		return ok && string(aVal.runes) == bs
	case *Pair:
		switch b.(type) {
		case *Pair, []Value:
			return listEqual(a, b, exhausted)
		}
		return false
	case []Value:
		if _, isPair := b.(*Pair); isPair {
			return listEqual(a, b, exhausted)
		}
		bList, ok := b.([]Value)
		if !ok || len(aVal) != len(bList) {
			return false
		}
		for i := range aVal {
			if !deepEqualEx(aVal[i], bList[i], exhausted) {
				return false
			}
		}
		return true
	case Keyword:
		bVal, ok := b.(Keyword)
		return ok && aVal == bVal
	case Char:
		bVal, ok := b.(Char)
		return ok && aVal == bVal
	case Vector:
		bVal, ok := b.(Vector)
		if !ok || len(aVal) != len(bVal) {
			return false
		}
		for i := range aVal {
			if !deepEqualEx(aVal[i], bVal[i], exhausted) {
				return false
			}
		}
		return true
	case Bytevector:
		bVal, ok := b.(Bytevector)
		if !ok || len(aVal) != len(bVal) {
			return false
		}
		for i := range aVal {
			if aVal[i] != bVal[i] {
				return false
			}
		}
		return true
	case Dictionary:
		bVal, ok := b.(Dictionary)
		if !ok || len(aVal) != len(bVal) {
			return false
		}
		for k, av := range aVal {
			bv, ok := bVal[k]
			if !ok || !deepEqualEx(av, bv, exhausted) {
				return false
			}
		}
		return true
	case *MultipleValues:
		bVal, ok := b.(*MultipleValues)
		if !ok || len(aVal.Vals) != len(bVal.Vals) {
			return false
		}
		for i := range aVal.Vals {
			if !deepEqualEx(aVal.Vals[i], bVal.Vals[i], exhausted) {
				return false
			}
		}
		return true
	case *Record:
		return a == b
	case *RecordType:
		return a == b
	case *Promise:
		return a == b
	case *EOFObject:
		_, ok := b.(*EOFObject)
		return ok
	case nil:
		return b == nil
	}
	return false
}

func eqvEqual(a, b Value) bool {
	switch av := a.(type) {
	case Integer:
		bv, ok := b.(Integer)
		return ok && av == bv
	case Number:
		bv, ok := b.(Number)
		return ok && av == bv
	case Char:
		bv, ok := b.(Char)
		return ok && av == bv
	}
	return identityEq(a, b)
}

func identityEq(a, b Value) bool {
	ta, tb := reflect.TypeOf(a), reflect.TypeOf(b)
	if ta != tb {
		return false
	}
	if ta != nil && !ta.Comparable() {
		va, vb := reflect.ValueOf(a), reflect.ValueOf(b)
		switch va.Kind() {
		case reflect.Slice:
			return va.Len() == vb.Len() && (va.Len() == 0 || va.Pointer() == vb.Pointer())
		case reflect.Map, reflect.Func:
			return va.Pointer() == vb.Pointer()
		}
		return false
	}
	return a == b
}

func (e *Evaluator) GlobalEnv() *Environment {
	return e.globalEnv
}

// RecordResult shifts the last-result bindings in the global environment: *1
// becomes v, the previous *1 moves to *2 and *2 to *3 — so a value just
// printed at the REPL stays reachable, e.g.
func (e *Evaluator) RecordResult(v Value) {
	if v == nil {
		return
	}
	old, _ := e.globalEnv.Lookup("*2")
	e.globalEnv.Set("*3", old)
	old, _ = e.globalEnv.Lookup("*1")
	e.globalEnv.Set("*2", old)
	e.globalEnv.Set("*1", v)
}

// EvalAll evaluates a sequence of expressions, returning the result of the last one.
// It skips nils (which may represent comments or empty lines).
func (e *Evaluator) EvalAll(exprs []Value, env *Environment) (Value, error) {
	var result Value
	for _, expr := range exprs {
		if expr == nil {
			continue // skip nils (comments, empty lines)
		}
		res, err := e.Eval(expr, env)
		if err != nil {
			return nil, err
		}
		result = res
	}
	return result, nil
}

func addSpecialForms(env *Environment) {
	Register("define", "define a variable or function: (define (f x) body...)", CommandMeta{})
	Register("set!", "assign to an existing binding: (set! name value)", CommandMeta{})
	Register("if", "conditional: (if test then [else])", CommandMeta{})
	Register("quote", "return the expression unevaluated", CommandMeta{})
	Register("lambda", "an anonymous function: (lambda (x) body...)", CommandMeta{})
	Register("begin", "evaluate expressions in order, returning the last", CommandMeta{})

	Register("pipe", "thread a value through stages, last-arg or at _: (pipe \"md\" (grep _ (ls)))", CommandMeta{})
}

var gensymCounter atomic.Uint64

func gensym(prefix string) Symbol {
	return Symbol(fmt.Sprintf("%%%s-%d", prefix, gensymCounter.Add(1)))
}

func mentionsSymbol(form Value, name string) bool {
	switch v := form.(type) {
	case Symbol:
		return string(v) == name
	case []Value:
		for _, elem := range v {
			if mentionsSymbol(elem, name) {
				return true
			}
		}
	}
	return false
}
