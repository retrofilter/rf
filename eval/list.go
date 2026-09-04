package eval

import (
	"errors"
	"fmt"
)

func carOf(v Value) (Value, bool) {
	switch n := v.(type) {
	case *Pair:
		return n.Car, true
	case []Value:
		if len(n) > 0 {
			return n[0], true
		}
	}
	return nil, false
}

func cdrOf(v Value) (Value, bool) {
	switch n := v.(type) {
	case *Pair:
		return n.Cdr, true
	case []Value:
		if len(n) > 0 {
			return n[1:], true
		}
	}
	return nil, false
}

func listInput(v Value) (Value, bool, error) {
	if s, isStream := v.(*Stream); isStream {
		lst, ok, err := AsList(s)
		if err != nil {
			return nil, false, err
		}
		return lst, ok, nil
	}
	switch v.(type) {
	case *Pair, []Value:
		return v, true, nil
	}
	return nil, false, nil
}

func listBuiltins(env *Environment) {
	combos := []string{""}
	for level := 1; level <= 4; level++ {
		var next []string
		for _, c := range combos {
			next = append(next, c+"a", c+"d")
		}
		combos = next
		if level == 1 {
			continue // car/cdr already exist
		}
		for _, combo := range combos {
			name := "c" + combo + "r"
			ops := combo
			env.SetBuiltin(name, cxrDoc(ops), BuiltinFunc(func(args []Value, env *Environment) (Value, error) {
				if len(args) != 1 {
					return nil, fmt.Errorf("%s expects 1 argument", name)
				}
				v := args[0]
				for i := len(ops) - 1; i >= 0; i-- {
					var ok bool
					if ops[i] == 'a' {
						v, ok = carOf(v)
					} else {
						v, ok = cdrOf(v)
					}
					if !ok {
						return nil, fmt.Errorf("%s expects nested pairs", name)
					}
				}
				return v, nil
			}))
		}
	}

	memberWith := func(name string, fixedEq func(a, b Value) bool, allowCompare bool) BuiltinFunc {
		return func(args []Value, env *Environment) (Value, error) {
			if len(args) < 2 || len(args) > 3 || (len(args) == 3 && !allowCompare) {
				return nil, fmt.Errorf("%s expects 2 arguments: (%s obj list)", name, name)
			}
			var compare Value
			if len(args) == 3 {
				if !isCallable(args[2]) {
					return nil, fmt.Errorf("%s: comparator must be a procedure", name)
				}
				compare = args[2]
			}
			v, ok, err := listInput(args[1])
			if err != nil {
				return nil, err
			}
			if !ok {
				return nil, fmt.Errorf("%s expects a list as second argument", name)
			}
			for steps := 0; steps < maxListSteps; steps++ {
				car, rest, ok := seqNext(v)
				if !ok {
					return false, nil // end of list (or improper tail)
				}
				matched := false
				if compare != nil {
					res, err := callFunction(compare, []Value{args[0], car}, env)
					if err != nil {
						return nil, err
					}
					matched = !isFalsy(res)
				} else {
					matched = fixedEq(args[0], car)
				}
				if matched {
					return v, nil
				}
				v = rest
			}
			return nil, fmt.Errorf("%s: circular list", name)
		}
	}
	env.SetBuiltin("memq", "the sublist starting at the first eq? match, else false", memberWith("memq", identityEq, false))
	env.SetBuiltin("memv", "the sublist starting at the first eqv? match, else false", memberWith("memv", eqvEqual, false))
	Register("member", "the sublist starting at the first match, else false: (member obj list [compare])", CommandMeta{})
	env.Set("member", memberWith("member", deepEqual, true))

	assocWith := func(name string, fixedEq func(a, b Value) bool, allowCompare bool) BuiltinFunc {
		return func(args []Value, env *Environment) (Value, error) {
			if len(args) < 2 || len(args) > 3 || (len(args) == 3 && !allowCompare) {
				return nil, fmt.Errorf("%s expects 2 arguments: (%s obj alist)", name, name)
			}
			var compare Value
			if len(args) == 3 {
				if !isCallable(args[2]) {
					return nil, fmt.Errorf("%s: comparator must be a procedure", name)
				}
				compare = args[2]
			}
			v, ok, err := listInput(args[1])
			if err != nil {
				return nil, err
			}
			if !ok {
				return nil, fmt.Errorf("%s expects a list as second argument", name)
			}
			for steps := 0; steps < maxListSteps; steps++ {
				entry, rest, ok := seqNext(v)
				if !ok {
					return false, nil
				}
				key, _, ok := seqNext(entry)
				if !ok {
					return nil, fmt.Errorf("%s expects a list of pairs", name)
				}
				matched := false
				if compare != nil {
					res, err := callFunction(compare, []Value{args[0], key}, env)
					if err != nil {
						return nil, err
					}
					matched = !isFalsy(res)
				} else {
					matched = fixedEq(args[0], key)
				}
				if matched {
					return entry, nil
				}
				v = rest
			}
			return nil, fmt.Errorf("%s: circular list", name)
		}
	}
	env.SetBuiltin("assq", "the first alist entry whose key is eq?, else false", assocWith("assq", identityEq, false))
	env.SetBuiltin("assv", "the first alist entry whose key is eqv?, else false", assocWith("assv", eqvEqual, false))
	Register("assoc", "the first alist entry whose key matches, else false: (assoc obj alist [compare])", CommandMeta{})
	env.Set("assoc", assocWith("assoc", deepEqual, true))

	env.SetBuiltin("make-list", "a fresh mutable list: (make-list k [fill])", BuiltinFunc(func(args []Value, env *Environment) (Value, error) {
		if len(args) < 1 || len(args) > 2 {
			return nil, errors.New("make-list expects 1 or 2 arguments: (make-list k [fill])")
		}
		k, ok := numIndex(args[0])
		if !ok || k < 0 {
			return nil, errors.New("make-list expects a non-negative integer length")
		}
		var fill Value = false // unspecified per R7RS; #f is a printable choice
		if len(args) == 2 {
			fill = args[1]
		}
		elems := make([]Value, k)
		for i := range elems {
			elems[i] = fill
		}
		return listFromSlice(elems), nil // pair chain: list-set! works
	}))

	env.SetBuiltin("list-copy", "a fresh copy of a list's spine (elements shared)", BuiltinFunc(func(args []Value, env *Environment) (Value, error) {
		if len(args) != 1 {
			return nil, errors.New("list-copy expects 1 argument")
		}
		switch src := args[0].(type) {
		case []Value:
			if len(src) == 0 {
				return []Value{}, nil
			}
			return listFromSlice(src), nil
		case *Pair:
			head := &Pair{}
			cur := head
			v := Value(src)
			for steps := 0; steps < maxListSteps; steps++ {
				p, isPair := v.(*Pair)
				if !isPair {
					if s, isSlice := v.([]Value); isSlice && len(s) > 0 {
						v = listFromSlice(s)
						continue
					}
					cur.Cdr = v // empty-list or improper tail, shared
					return head.Cdr.(*Pair), nil
				}
				next := &Pair{Car: p.Car}
				cur.Cdr = next
				cur = next
				v = p.Cdr
			}
			return nil, errors.New("list-copy: circular list")
		}
		return args[0], nil // non-list: returned unchanged
	}))

	env.SetBuiltin("list-tail", "the sublist after k cdrs: (list-tail lst k)", BuiltinFunc(func(args []Value, env *Environment) (Value, error) {
		if len(args) != 2 {
			return nil, errors.New("list-tail expects 2 arguments: (list-tail lst k)")
		}
		k, ok := numIndex(args[1])
		if !ok || k < 0 {
			return nil, errors.New("list-tail expects a non-negative integer index")
		}
		v := args[0]
		for i := 0; i < k; i++ {
			rest, ok := cdrOf(v)
			if !ok {
				return nil, fmt.Errorf("list-tail: list has fewer than %d elements", k)
			}
			v = rest
		}
		return v, nil
	}))

	env.SetBuiltin("list-set!", "replace the element at an index in place: (list-set! lst k obj)", BuiltinFunc(func(args []Value, env *Environment) (Value, error) {
		if len(args) != 3 {
			return nil, errors.New("list-set! expects 3 arguments: (list-set! lst k obj)")
		}
		k, ok := numIndex(args[1])
		if !ok || k < 0 {
			return nil, errors.New("list-set! expects a non-negative integer index")
		}
		v := args[0]
		for i := 0; i < k; i++ {
			rest, ok := cdrOf(v)
			if !ok {
				return nil, fmt.Errorf("list-set!: list has fewer than %d elements", k+1)
			}
			v = rest
		}
		p, isPair := v.(*Pair)
		if !isPair {
			if _, isSlice := v.([]Value); isSlice {
				return nil, errors.New("list-set! expects a mutable list (from cons or list; literal lists are immutable)")
			}
			return nil, fmt.Errorf("list-set!: list has fewer than %d elements", k+1)
		}
		p.Car = args[2]
		return nil, nil
	}))

	mapCore := func(name string, collect bool) BuiltinFunc {
		return func(args []Value, env *Environment) (Value, error) {
			if len(args) < 2 {
				return nil, fmt.Errorf("%s expects at least 2 arguments: (%s fn list...)", name, name)
			}
			fn := args[0]
			if len(args) == 2 {
				lst, ok, err := AsList(args[1])
				if err != nil {
					return nil, err
				}
				if !ok {
					return nil, fmt.Errorf("%s expects a list as second argument", name)
				}
				var result []Value
				if collect {
					result = make([]Value, len(lst))
				}
				for i, v := range lst {
					res, err := callFunction(fn, []Value{v}, env)
					if err != nil {
						return nil, err
					}
					if collect {
						result[i] = res
					}
				}
				if collect {
					return result, nil
				}
				return nil, nil
			}
			lists := make([]Value, len(args)-1)
			for i, arg := range args[1:] {
				v, ok, err := listInput(arg)
				if err != nil {
					return nil, err
				}
				if !ok {
					return nil, fmt.Errorf("%s expects lists", name)
				}
				lists[i] = v
			}
			var result []Value
			cars := make([]Value, len(lists))
			for steps := 0; steps < maxListSteps; steps++ {
				for i, l := range lists {
					car, rest, ok := seqNext(l)
					if !ok {
						if collect {
							return result, nil
						}
						return nil, nil
					}
					cars[i] = car
					lists[i] = rest
				}
				res, err := callFunction(fn, cars, env)
				if err != nil {
					return nil, err
				}
				if collect {
					result = append(result, res)
				}
			}
			return nil, fmt.Errorf("%s: all lists are circular", name)
		}
	}
	env.SetBuiltin("map", "apply a function element-wise: (map fn list...); shortest list ends the walk", mapCore("map", true))
	env.SetBuiltin("for-each", "call a function on each element for its effects: (for-each fn list...)", mapCore("for-each", false))
}

func cxrDoc(ops string) string {
	doc := "the"
	for i := 0; i < len(ops); i++ {
		if ops[i] == 'a' {
			doc += " car of the"
		} else {
			doc += " cdr of the"
		}
	}
	return doc[:len(doc)-7] + " of a list"
}
