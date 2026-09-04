package eval

import (
	"errors"
)

// MultipleValues carries the results of (values a b ...) to
// call-with-values. Zero or ≥2 values only; never one.
type MultipleValues struct {
	Vals []Value
}

// EOFObject is the disjoint end-of-file type: eof-object? answers true for
// it and nothing else. One singleton suffices — the ports work in Phase 6
// returns the same one.
type EOFObject struct{}

var theEOFObject = &EOFObject{}

func valuesBuiltins(env *Environment) {
	env.SetBuiltin("values", "return multiple values to a call-with-values consumer", BuiltinFunc(func(args []Value, env *Environment) (Value, error) {
		if len(args) == 1 {
			return args[0], nil
		}
		return &MultipleValues{Vals: args}, nil
	}))
	env.SetBuiltin("call-with-values", "apply a consumer to a producer's values: (call-with-values producer consumer)", BuiltinFunc(func(args []Value, env *Environment) (Value, error) {
		if len(args) != 2 {
			return nil, errors.New("call-with-values expects 2 arguments: (call-with-values producer consumer)")
		}
		produced, err := callFunction(args[0], nil, env)
		if err != nil {
			return nil, err
		}
		if mv, ok := produced.(*MultipleValues); ok {
			return callFunction(args[1], mv.Vals, env)
		}
		return callFunction(args[1], []Value{produced}, env)
	}))
	Register("define-values", "bind names to an expression's multiple values: (define-values (a b) expr)", CommandMeta{})
	env.setCompiler("define-values", "define-values", cfDefineValues) // compile_forms.go

	Register("let-values", "bind formals to each expression's values: (let-values (((a b) expr)) body...)", CommandMeta{})
	env.setCompiler("let-values", "let-values", cfLetValues)
	Register("let*-values", "let-values with each init seeing the previous bindings", CommandMeta{})
	env.setCompiler("let*-values", "let*-values", cfLetStarValues)

	env.SetBuiltin("eof-object", "the end-of-file object", BuiltinFunc(func(args []Value, env *Environment) (Value, error) {
		if len(args) != 0 {
			return nil, errors.New("eof-object expects no arguments")
		}
		return theEOFObject, nil
	}))
	env.SetBuiltin("eof-object?", "true when the value is the end-of-file object", BuiltinFunc(func(args []Value, env *Environment) (Value, error) {
		if len(args) != 1 {
			return nil, errors.New("eof-object? expects 1 argument")
		}
		_, ok := args[0].(*EOFObject)
		return ok, nil
	}))
}

func valuesOf(v Value) []Value {
	if mv, ok := v.(*MultipleValues); ok {
		return mv.Vals
	}
	return []Value{v}
}

func printValues(mv *MultipleValues) string {
	if len(mv.Vals) == 0 {
		return "#<no values>"
	}
	out := ""
	for i, v := range mv.Vals {
		if i > 0 {
			out += " "
		}
		out += PrintValue(v)
	}
	return out
}
