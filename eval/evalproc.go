package eval

import (
	"errors"
	"fmt"
)

func dataToAST(v Value) Value {
	switch d := v.(type) {
	case *Pair:
		var elems []Value
		tail, err := walkPairs(d, func(p *Pair) {
			elems = append(elems, dataToAST(p.Car))
		})
		if err != nil {
			return v
		}
		switch t := tail.(type) {
		case []Value:
			for _, x := range t {
				elems = append(elems, dataToAST(x))
			}
			return elems
		default:
			var res Value = dataToAST(tail)
			for i := len(elems) - 1; i >= 0; i-- {
				res = &Pair{Car: elems[i], Cdr: res}
			}
			return res
		}
	case []Value:
		out := make([]Value, len(d))
		for i, x := range d {
			out[i] = dataToAST(x)
		}
		return out
	}
	return v
}

func environmentArg(name string, args []Value, idx int, ev *Evaluator) (*Environment, error) {
	if len(args) <= idx {
		return ev.globalEnv, nil
	}
	target, ok := args[idx].(*Environment)
	if !ok {
		return nil, fmt.Errorf("%s expects an environment (from environment or interaction-environment), got %s", name, PrintValue(args[idx]))
	}
	return target, nil
}

func evalBuiltins(env *Environment, ev *Evaluator) {
	env.SetBuiltin("eval", "evaluate a datum as code: (eval expr [environment])", BuiltinFunc(func(args []Value, env *Environment) (Value, error) {
		if len(args) < 1 || len(args) > 2 {
			return nil, errors.New("eval expects 1 or 2 arguments: (eval expr [environment])")
		}
		target, err := environmentArg("eval", args, 1, ev)
		if err != nil {
			return nil, err
		}
		return ev.Eval(dataToAST(args[0]), target)
	}))

	env.SetBuiltin("environment", "the environment named by import sets — the shared global env under rf's thin library design", BuiltinFunc(func(args []Value, env *Environment) (Value, error) {
		for _, a := range args {
			set := normalizeList(a)
			if _, ok := formParts(set); !ok {
				return nil, fmt.Errorf("environment expects import-set lists like '(scheme base), got %s", PrintValue(a))
			}
			if _, _, err := resolveImportSet(ev.globalEnv, set); err != nil {
				return nil, err
			}
		}
		return ev.globalEnv, nil
	}))

	env.SetBuiltin("interaction-environment", "the live global environment user and LLM share", BuiltinFunc(func(args []Value, env *Environment) (Value, error) {
		if len(args) != 0 {
			return nil, errors.New("interaction-environment expects no arguments")
		}
		return ev.globalEnv, nil
	}))

	reportEnv := func(name string) BuiltinFunc {
		return func(args []Value, env *Environment) (Value, error) {
			if len(args) != 1 {
				return nil, fmt.Errorf("%s expects a version argument", name)
			}
			v, ok := args[0].(Integer)
			if !ok || (v != 5 && v != 7) {
				return nil, fmt.Errorf("%s: unsupported version %s", name, PrintValue(args[0]))
			}
			return ev.globalEnv, nil
		}
	}
	env.SetBuiltin("scheme-report-environment", "the R5RS report environment (the shared global env)", reportEnv("scheme-report-environment"))
	env.SetBuiltin("null-environment", "the R5RS syntax-only environment (the shared global env)", reportEnv("null-environment"))

	env.SetBuiltin("load", "read and evaluate a source file: (load \"file\" [environment])", BuiltinFunc(func(args []Value, env *Environment) (Value, error) {
		if len(args) < 1 || len(args) > 2 {
			return nil, errors.New("load expects 1 or 2 arguments: (load \"file\" [environment])")
		}
		filename, ok := stringText(args[0])
		if !ok {
			return nil, errors.New("load expects a filename string")
		}
		target, err := environmentArg("load", args, 1, ev)
		if err != nil {
			return nil, err
		}
		path := expandHome(filename)
		if err := ev.approval.requireRead(fmt.Sprintf("load %q", filename), path); err != nil {
			return nil, err
		}
		if _, err := evalSchemeFile(ev, target, filename, false); err != nil {
			return nil, err
		}
		return nil, nil
	}))
}
