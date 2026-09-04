package eval

import (
	"errors"
	"fmt"
)

// FastBuiltin wraps a builtin with allocation-free call paths for the arities
// that dominate real code.
type FastBuiltin struct {
	Fn  BuiltinFunc
	Fn1 func(a Value, env *Environment) (Value, error)
	Fn2 func(a, b Value, env *Environment) (Value, error)
}

func fastBuiltins(env *Environment) {
	upgrade := func(name string, fn1 func(Value, *Environment) (Value, error), fn2 func(Value, Value, *Environment) (Value, error)) {
		c, ok := env.bindings[name]
		if !ok {
			return
		}
		fn, ok := c.val.(BuiltinFunc)
		if !ok {
			return
		}
		c.val = &FastBuiltin{Fn: fn, Fn1: fn1, Fn2: fn2}
	}

	num2 := func(name string, op func(a, b Value) (Value, bool)) func(Value, Value, *Environment) (Value, error) {
		return func(a, b Value, _ *Environment) (Value, error) {
			res, ok := op(a, b)
			if !ok {
				return nil, fmt.Errorf("%s expects numbers", name)
			}
			return res, nil
		}
	}
	upgrade("+", nil, num2("+", numAdd2))
	upgrade("-", nil, num2("-", numSub2))
	upgrade("*", nil, num2("*", numMul2))
	upgrade("/", nil, func(a, b Value, _ *Environment) (Value, error) {
		if !isNumber(a) || !isNumber(b) {
			return nil, errors.New("/ expects numbers")
		}
		return numDiv2(a, b)
	})

	upgrade("=", nil, func(a, b Value, _ *Environment) (Value, error) {
		if !isNumber(a) {
			return nil, fmt.Errorf("= expects numbers, got %s — use equal? for other values", PrintValue(a))
		}
		eq, ok := numEqual2(a, b)
		if !ok {
			return nil, fmt.Errorf("= expects numbers, got %s — use equal? for other values", PrintValue(b))
		}
		return eq, nil
	})
	cmp2 := func(name string, admit func(cmp int) bool) func(Value, Value, *Environment) (Value, error) {
		return func(a, b Value, _ *Environment) (Value, error) {
			if !isNumber(a) {
				return nil, fmt.Errorf("%s expects numbers, got %s", name, PrintValue(a))
			}
			cmp, unordered, ok := numOrder2(a, b)
			if !ok {
				return nil, fmt.Errorf("%s expects numbers, got %s", name, PrintValue(b))
			}
			return !unordered && admit(cmp), nil
		}
	}
	upgrade("<", nil, cmp2("<", func(cmp int) bool { return cmp < 0 }))
	upgrade(">", nil, cmp2(">", func(cmp int) bool { return cmp > 0 }))
	upgrade("<=", nil, cmp2("<=", func(cmp int) bool { return cmp <= 0 }))
	upgrade(">=", nil, cmp2(">=", func(cmp int) bool { return cmp >= 0 }))

	upgrade("not", func(a Value, _ *Environment) (Value, error) {
		if a == nil {
			return true, nil
		}
		if b, ok := a.(bool); ok {
			return !b, nil
		}
		return false, nil
	}, nil)
	upgrade("null?", func(a Value, _ *Environment) (Value, error) {
		lst, ok := a.([]Value)
		return ok && len(lst) == 0, nil
	}, nil)
	upgrade("pair?", func(a Value, _ *Environment) (Value, error) {
		if _, ok := a.(*Pair); ok {
			return true, nil
		}
		lst, ok := a.([]Value)
		return ok && len(lst) > 0, nil
	}, nil)
	upgrade("zero?", func(a Value, _ *Environment) (Value, error) {
		switch n := a.(type) {
		case Integer:
			return n == 0, nil
		case Number:
			return float64(n) == 0, nil
		}
		return nil, errors.New("zero? expects a number")
	}, nil)

	listElem := func(name string, pick func(p *Pair) Value, pickList func(lst []Value) Value) func(Value, *Environment) (Value, error) {
		return func(a Value, _ *Environment) (Value, error) {
			if p, ok := a.(*Pair); ok {
				return pick(p), nil
			}
			lst, ok, err := AsList(a)
			if err != nil {
				return nil, err
			}
			if !ok || len(lst) == 0 {
				return nil, fmt.Errorf("%s expects a non-empty list", name)
			}
			return pickList(lst), nil
		}
	}
	upgrade("car", listElem("car",
		func(p *Pair) Value { return p.Car },
		func(lst []Value) Value { return lst[0] }), nil)
	upgrade("cdr", listElem("cdr",
		func(p *Pair) Value { return p.Cdr },
		func(lst []Value) Value { return lst[1:] }), nil)

	upgrade("cons", nil, func(a, b Value, _ *Environment) (Value, error) {
		return &Pair{Car: a, Cdr: b}, nil
	})
	setPart := func(name string, set func(p *Pair, v Value)) func(Value, Value, *Environment) (Value, error) {
		return func(a, b Value, _ *Environment) (Value, error) {
			p, ok := a.(*Pair)
			if !ok {
				return nil, fmt.Errorf("%s expects a mutable pair (from cons or list; literal lists are immutable)", name)
			}
			set(p, b)
			return nil, nil
		}
	}
	upgrade("set-car!", nil, setPart("set-car!", func(p *Pair, v Value) { p.Car = v }))
	upgrade("set-cdr!", nil, setPart("set-cdr!", func(p *Pair, v Value) { p.Cdr = v }))

	upgrade("eq?", nil, func(a, b Value, _ *Environment) (Value, error) {
		return identityEq(a, b), nil
	})
	upgrade("eqv?", nil, func(a, b Value, _ *Environment) (Value, error) {
		return eqvEqual(a, b), nil
	})
	upgrade("equal?", nil, func(a, b Value, _ *Environment) (Value, error) {
		exhausted := false
		eq := deepEqualEx(a, b, &exhausted)
		if exhausted {
			return nil, errors.New("equal?: list too long or circular to compare (step cap exceeded)")
		}
		return eq, nil
	})
}
