package eval

import (
	"errors"
)

type contToken struct{ live bool }

// Continuation is the escape procedure call/cc passes to its receiver.
// Callable everywhere a procedure is (compiled calls, Apply, apply builtin).
type Continuation struct{ tok *contToken }

type contUnwind struct {
	tok *contToken
	val Value
}

func (c *contUnwind) Error() string {
	return "escape continuation unwinding (uncaught — continuation invoked outside its capturing call/cc)"
}

func (c *Continuation) invoke(args []Value) error {
	if !c.tok.live {
		return errors.New("continuation invoked after its extent has exited (call/cc is escape-only)")
	}
	var val Value
	switch len(args) {
	case 0:
		val = &MultipleValues{}
	case 1:
		val = args[0]
	default:
		val = &MultipleValues{Vals: args}
	}
	return &contUnwind{tok: c.tok, val: val}
}

// Parameter is a make-parameter object: callable with no arguments to read
// its current value.
type Parameter struct {
	vals      []Value
	converter Value
}

func (p *Parameter) current() Value { return p.vals[len(p.vals)-1] }

func (p *Parameter) convert(v Value, env *Environment) (Value, error) {
	if p.converter == nil {
		return v, nil
	}
	return callFunction(p.converter, []Value{v}, env)
}

// CaseLambda dispatches to the first clause whose formals accept the
// argument count, in clause order per §4.2.9.
type CaseLambda struct {
	clauses []*Lambda
}

func (cl *CaseLambda) match(n int) *Lambda {
	for _, l := range cl.clauses {
		if l.rest == "" && n == len(l.params) {
			return l
		}
		if l.rest != "" && n >= len(l.params) {
			return l
		}
	}
	return nil
}

func isCallable(v Value) bool {
	switch v.(type) {
	case BuiltinFunc, *FastBuiltin, *Lambda, *CaseLambda, *Continuation, *Parameter:
		return true
	}
	return false
}

func controlBuiltins(env *Environment) {
	callcc := BuiltinFunc(func(args []Value, env *Environment) (Value, error) {
		if len(args) != 1 || !isCallable(args[0]) {
			return nil, errors.New("call/cc expects 1 argument: a procedure")
		}
		tok := &contToken{live: true}
		res, err := callFunction(args[0], []Value{&Continuation{tok: tok}}, env)
		tok.live = false
		if err != nil {
			var cu *contUnwind
			if errors.As(err, &cu) && cu.tok == tok {
				return cu.val, nil
			}
			return nil, err
		}
		return res, nil
	})
	env.SetBuiltin("call-with-current-continuation", "call a procedure with an escape continuation: (call/cc (lambda (k) ...))", callcc)
	env.SetBuiltin("call/cc", "call a procedure with an escape continuation: (call/cc (lambda (k) ...))", callcc)

	env.SetBuiltin("dynamic-wind", "run thunk between before/after thunks; after runs even on unwinds", BuiltinFunc(func(args []Value, env *Environment) (Value, error) {
		if len(args) != 3 {
			return nil, errors.New("dynamic-wind expects 3 arguments: (dynamic-wind before thunk after)")
		}
		before, thunk, after := args[0], args[1], args[2]
		if _, err := callFunction(before, nil, env); err != nil {
			return nil, err
		}
		res, terr := callFunction(thunk, nil, env)
		if terr != nil && errors.Is(terr, ErrInterrupted) {
			ClearInterrupt()
			_, _ = callFunction(after, nil, env)
			Interrupt()
			return nil, terr
		}
		_, aerr := callFunction(after, nil, env)
		if terr != nil {
			return nil, terr
		}
		if aerr != nil {
			return nil, aerr
		}
		return res, nil
	}))

	env.SetBuiltin("make-parameter", "a dynamic-binding parameter object: (make-parameter init [converter])", BuiltinFunc(func(args []Value, env *Environment) (Value, error) {
		if len(args) != 1 && len(args) != 2 {
			return nil, errors.New("make-parameter expects 1 or 2 arguments: (make-parameter init [converter])")
		}
		p := &Parameter{}
		if len(args) == 2 {
			if !isCallable(args[1]) {
				return nil, errors.New("make-parameter: converter must be a procedure")
			}
			p.converter = args[1]
		}
		init, err := p.convert(args[0], env)
		if err != nil {
			return nil, err
		}
		p.vals = []Value{init}
		return p, nil
	}))

	Register("parameterize", "bind parameters for a dynamic extent: (parameterize ((p v)...) body...)", CommandMeta{})
	env.setCompiler("parameterize", "parameterize", cfParameterize) // compile_forms.go

	Register("case-lambda", "a procedure dispatching on argument count: (case-lambda (formals body...) ...)", CommandMeta{})
	env.setCompiler("case-lambda", "case-lambda", cfCaseLambda) // compile_forms.go
}
