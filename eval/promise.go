package eval

import (
	"errors"
	"fmt"
)

// Promise is a delayed computation.
type Promise struct {
	done    bool
	val     Value
	compute func() (Value, error)
	lazy    bool
}

func forceValue(v Value) (Value, error) {
	for {
		if interrupted.Load() {
			return nil, ErrInterrupted
		}
		p, ok := v.(*Promise)
		if !ok {
			return v, nil
		}
		if p.done {
			v = p.val
			continue
		}
		res, err := p.compute()
		if err != nil {
			return nil, err
		}
		if p.done {
			v = p.val
			continue
		}
		if p.lazy {
			q, ok := res.(*Promise)
			if !ok {
				return nil, fmt.Errorf("delay-force expects the body to yield a promise, got %s", PrintValue(res))
			}
			p.done, p.val, p.compute, p.lazy = q.done, q.val, q.compute, q.lazy
			continue
		}
		p.done, p.val, p.compute = true, res, nil
		v = res
	}
}

func promiseBuiltins(env *Environment) {
	Register("delay", "a promise to evaluate an expression later: (delay expr)", CommandMeta{})
	env.setCompiler("delay", "delay", cfDelay) // compile_forms.go
	Register("delay-force", "a promise chaining to the promise its body yields (SRFI-45 lazy)", CommandMeta{})
	env.setCompiler("delay-force", "delay-force", cfDelayForce) // compile_forms.go
	env.SetBuiltin("force", "the value of a promise, computing and memoizing it on first use", BuiltinFunc(func(args []Value, env *Environment) (Value, error) {
		if len(args) != 1 {
			return nil, errors.New("force expects 1 argument")
		}
		return forceValue(args[0])
	}))
	env.SetBuiltin("make-promise", "wrap a value as an already-forced promise (promises pass through)", BuiltinFunc(func(args []Value, env *Environment) (Value, error) {
		if len(args) != 1 {
			return nil, errors.New("make-promise expects 1 argument")
		}
		if p, ok := args[0].(*Promise); ok {
			return p, nil
		}
		return &Promise{done: true, val: args[0]}, nil
	}))
	env.SetBuiltin("promise?", "true when the value is a promise", BuiltinFunc(func(args []Value, env *Environment) (Value, error) {
		if len(args) != 1 {
			return nil, errors.New("promise? expects 1 argument")
		}
		_, ok := args[0].(*Promise)
		return ok, nil
	}))
}
