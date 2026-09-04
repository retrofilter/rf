package eval

import (
	"errors"
	"fmt"
	"io/fs"
	"strings"
)

// ErrorObject is the condition created by (error msg irritants...) and the
// wrapper for Go errors caught at an exception boundary. Kind classifies
// file-error? ("file") and read-error? ("read") conditions.
type ErrorObject struct {
	Message   string
	Irritants []Value
	Kind      string
	goErr     error // original Go error when wrapped; nil for (error ...)
}

func (eo *ErrorObject) String() string {
	if len(eo.Irritants) == 0 {
		return eo.Message
	}
	parts := make([]string, len(eo.Irritants))
	for i, irr := range eo.Irritants {
		parts[i] = PrintValue(irr)
	}
	return eo.Message + " " + strings.Join(parts, " ")
}

type raisedError struct {
	value       Value
	continuable bool
}

func (r *raisedError) Error() string {
	if eo, ok := r.value.(*ErrorObject); ok {
		return eo.String()
	}
	return "uncaught exception: " + PrintValue(r.value)
}

type guardMarker struct{}

func isUnwindBarrier(err error) bool {
	if errors.Is(err, ErrInterrupted) {
		return true
	}
	var cu *contUnwind
	return errors.As(err, &cu)
}

func conditionValue(err error) Value {
	var re *raisedError
	if errors.As(err, &re) {
		return re.value
	}
	kind := ""
	var pe *fs.PathError
	if errors.As(err, &pe) {
		kind = "file"
	}
	return &ErrorObject{Message: err.Error(), Kind: kind, goErr: err}
}

func exceptionBuiltins(env *Environment, ev *Evaluator) {
	env.SetBuiltin("error", "raise an error object: (error \"message\" irritant...)", BuiltinFunc(func(args []Value, env *Environment) (Value, error) {
		if len(args) == 0 {
			return nil, errors.New("error expects a message argument")
		}
		msg, ok := stringText(args[0])
		if !ok {
			msg = PrintValue(args[0])
		}
		return nil, &raisedError{value: &ErrorObject{Message: msg, Irritants: args[1:]}}
	}))

	env.SetBuiltin("raise", "raise a value as an exception (non-continuable)", BuiltinFunc(func(args []Value, env *Environment) (Value, error) {
		if len(args) != 1 {
			return nil, errors.New("raise expects 1 argument")
		}
		return nil, &raisedError{value: args[0]}
	}))

	env.SetBuiltin("raise-continuable", "raise an exception whose handler's value resumes the computation", BuiltinFunc(func(args []Value, env *Environment) (Value, error) {
		if len(args) != 1 {
			return nil, errors.New("raise-continuable expects 1 argument")
		}
		if n := len(ev.handlers); n > 0 {
			if h := ev.handlers[n-1]; !isGuardMark(h) {
				saved := ev.handlers
				ev.handlers = saved[: n-1 : n-1]
				res, err := callFunction(h, []Value{args[0]}, env)
				ev.handlers = saved
				if err != nil {
					return nil, err
				}
				return res, nil
			}
		}
		return nil, &raisedError{value: args[0], continuable: true}
	}))

	env.SetBuiltin("with-exception-handler", "install an exception handler for a thunk's dynamic extent", BuiltinFunc(func(args []Value, env *Environment) (Value, error) {
		if len(args) != 2 || !isCallable(args[0]) || !isCallable(args[1]) {
			return nil, errors.New("with-exception-handler expects 2 procedures: (with-exception-handler handler thunk)")
		}
		handler, thunk := args[0], args[1]
		saved := ev.handlers
		ev.handlers = append(saved[:len(saved):len(saved)], handler)
		res, err := callFunction(thunk, nil, env)
		ev.handlers = saved
		if err == nil {
			return res, nil
		}
		if isUnwindBarrier(err) {
			return nil, err
		}
		if _, herr := callFunction(handler, []Value{conditionValue(err)}, env); herr != nil {
			return nil, herr
		}
		return nil, &raisedError{value: &ErrorObject{
			Message: "exception handler returned from an exception that cannot be resumed",
			goErr:   err,
		}}
	}))

	Register("guard", "catch exceptions from a body: (guard (var clause...) body...)", CommandMeta{})
	env.setCompiler("guard", "guard", cfGuard) // compile_forms.go

	env.SetBuiltin("error-object?", "true when the value is an error object (from error or a wrapped failure)", BuiltinFunc(func(args []Value, env *Environment) (Value, error) {
		if len(args) != 1 {
			return nil, errors.New("error-object? expects 1 argument")
		}
		_, ok := args[0].(*ErrorObject)
		return ok, nil
	}))
	env.SetBuiltin("error-object-message", "an error object's message string", BuiltinFunc(func(args []Value, env *Environment) (Value, error) {
		if len(args) != 1 {
			return nil, errors.New("error-object-message expects 1 argument")
		}
		eo, ok := args[0].(*ErrorObject)
		if !ok {
			return nil, errors.New("error-object-message expects an error object")
		}
		return String(eo.Message), nil
	}))
	env.SetBuiltin("error-object-irritants", "an error object's irritants as a list", BuiltinFunc(func(args []Value, env *Environment) (Value, error) {
		if len(args) != 1 {
			return nil, errors.New("error-object-irritants expects 1 argument")
		}
		eo, ok := args[0].(*ErrorObject)
		if !ok {
			return nil, errors.New("error-object-irritants expects an error object")
		}
		return listFromSlice(eo.Irritants), nil
	}))
	kindPred := func(name, kind string) BuiltinFunc {
		return func(args []Value, env *Environment) (Value, error) {
			if len(args) != 1 {
				return nil, fmt.Errorf("%s expects 1 argument", name)
			}
			eo, ok := args[0].(*ErrorObject)
			return ok && eo.Kind == kind, nil
		}
	}
	env.SetBuiltin("file-error?", "true for an error object raised by a file operation", kindPred("file-error?", "file"))
	env.SetBuiltin("read-error?", "true for an error object raised by read", kindPred("read-error?", "read"))

	Register("syntax-error", "signal a syntax error: (syntax-error \"message\" form...)", CommandMeta{})
	env.SetSpecialForm("syntax-error", func(args []Value, env *Environment, e *Evaluator) (Value, error) {
		if len(args) == 0 {
			return nil, errors.New("syntax-error expects a message")
		}
		msg, ok := stringText(args[0])
		if !ok {
			msg = PrintValue(args[0])
		}
		return nil, &raisedError{value: &ErrorObject{Message: msg, Irritants: args[1:]}}
	})
}

func isGuardMark(v Value) bool {
	_, ok := v.(guardMarker)
	return ok
}
