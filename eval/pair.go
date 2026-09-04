package eval

import (
	"errors"
	"fmt"
)

// Pair is a real cons cell (SCHEME.md D2 stage one): the canonical *data*
// list type, existing alongside rf's slice-backed lists.
type Pair struct {
	Car, Cdr Value
}

const maxListSteps = 1 << 24

func isEmptyList(v Value) bool {
	s, ok := v.([]Value)
	return ok && len(s) == 0
}

func listFromSlice(elems []Value) Value {
	if len(elems) == 0 {
		return []Value{}
	}
	var tail Value = []Value{}
	for i := len(elems) - 1; i >= 0; i-- {
		tail = &Pair{Car: elems[i], Cdr: tail}
	}
	return tail
}

func walkPairs(p *Pair, visit func(*Pair)) (tail Value, err error) {
	slow := p
	for i := 0; ; i++ {
		visit(p)
		next, ok := p.Cdr.(*Pair)
		if !ok {
			return p.Cdr, nil
		}
		p = next
		if i%2 == 1 {
			slow = slow.Cdr.(*Pair)
		}
		if p == slow {
			return nil, errors.New("circular list")
		}
	}
}

func pairToSlice(p *Pair) ([]Value, error) {
	out := []Value{}
	tail, err := walkPairs(p, func(c *Pair) { out = append(out, c.Car) })
	if err != nil {
		return nil, err
	}
	if s, ok := tail.([]Value); ok {
		return append(out, s...), nil
	}
	return nil, fmt.Errorf("improper list (ends in %s)", PrintValue(tail))
}

func seqNext(v Value) (car, rest Value, ok bool) {
	switch n := v.(type) {
	case *Pair:
		return n.Car, n.Cdr, true
	case []Value:
		if len(n) == 0 {
			return nil, nil, false
		}
		return n[0], n[1:], true
	}
	return nil, nil, false
}

func listEqual(a, b Value, exhausted *bool) bool {
	for steps := 0; steps < maxListSteps; steps++ {
		ac, arest, aok := seqNext(a)
		bc, brest, bok := seqNext(b)
		if !aok || !bok {
			if aok != bok {
				return false
			}
			// Both ended: empty lists, or improper tails compared as values.
			return deepEqualEx(a, b, exhausted)
		}
		if !deepEqualEx(ac, bc, exhausted) {
			return false
		}
		a, b = arest, brest
	}
	if exhausted != nil {
		*exhausted = true
	}
	return false
}

func properListLength(v Value) (int, error) {
	switch node := v.(type) {
	case []Value:
		return len(node), nil
	case *Pair:
		n := 0
		tail, err := walkPairs(node, func(*Pair) { n++ })
		if err != nil {
			return 0, fmt.Errorf("length: %v", err)
		}
		if s, ok := tail.([]Value); ok {
			return n + len(s), nil
		}
		return 0, fmt.Errorf("length expects a proper list (ends in %s)", PrintValue(tail))
	}
	return 0, errors.New("length expects a list")
}

func isProperList(v Value) bool {
	switch node := v.(type) {
	case []Value:
		return true
	case *Pair:
		tail, err := walkPairs(node, func(*Pair) {})
		if err != nil {
			return false // circular
		}
		_, ok := tail.([]Value)
		return ok
	}
	return false
}

func normalizeList(v Value) Value {
	if p, ok := v.(*Pair); ok {
		if s, err := pairToSlice(p); err == nil {
			return s
		}
	}
	return v
}

func pairBuiltins(env *Environment) {
	env.SetBuiltin("cons", "a fresh pair: (cons car cdr) — improper (cons 1 2) allowed", BuiltinFunc(func(args []Value, env *Environment) (Value, error) {
		if len(args) != 2 {
			return nil, errors.New("cons expects 2 arguments")
		}
		return &Pair{Car: args[0], Cdr: args[1]}, nil
	}))
	env.SetBuiltin("set-car!", "replace a pair's car in place", BuiltinFunc(func(args []Value, env *Environment) (Value, error) {
		if len(args) != 2 {
			return nil, errors.New("set-car! expects 2 arguments")
		}
		p, ok := args[0].(*Pair)
		if !ok {
			return nil, errors.New("set-car! expects a mutable pair (from cons or list; literal lists are immutable)")
		}
		p.Car = args[1]
		return nil, nil
	}))
	env.SetBuiltin("set-cdr!", "replace a pair's cdr in place", BuiltinFunc(func(args []Value, env *Environment) (Value, error) {
		if len(args) != 2 {
			return nil, errors.New("set-cdr! expects 2 arguments")
		}
		p, ok := args[0].(*Pair)
		if !ok {
			return nil, errors.New("set-cdr! expects a mutable pair (from cons or list; literal lists are immutable)")
		}
		p.Cdr = args[1]
		return nil, nil
	}))
}
