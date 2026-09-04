package eval

import (
	"errors"
	"fmt"

	"golang.org/x/text/cases"
)

// MutableString is the second string representation of SCHEME.md D3: a
// []rune-backed mutable string, allocated only by make-string, string, and
// string-copy.
type MutableString struct {
	runes []rune
}

func stringText(v Value) (string, bool) {
	switch s := v.(type) {
	case String:
		return string(s), true
	case *MutableString:
		return string(s.runes), true
	}
	return "", false
}

func isString(v Value) bool {
	switch v.(type) {
	case String, *MutableString:
		return true
	}
	return false
}

func mutableStringArg(name string, v Value) (*MutableString, error) {
	if m, ok := v.(*MutableString); ok {
		return m, nil
	}
	if _, ok := v.(String); ok {
		return nil, fmt.Errorf("%s expects a mutable string (from make-string, string, or string-copy; literals are immutable)", name)
	}
	return nil, fmt.Errorf("%s expects a string", name)
}

func stringCompare(name string, fold bool, cmp func(a, b string) bool) BuiltinFunc {
	return func(args []Value, env *Environment) (Value, error) {
		if len(args) < 2 {
			return nil, fmt.Errorf("%s expects at least 2 arguments", name)
		}
		prev, ok := stringText(args[0])
		if !ok {
			return nil, fmt.Errorf("%s expects strings, got %s", name, PrintValue(args[0]))
		}
		if fold {
			prev = foldString(prev)
		}
		result := true
		for _, arg := range args[1:] {
			s, ok := stringText(arg)
			if !ok {
				return nil, fmt.Errorf("%s expects strings, got %s", name, PrintValue(arg))
			}
			if fold {
				s = foldString(s)
			}
			if !cmp(prev, s) {
				result = false
			}
			prev = s
		}
		return result, nil
	}
}

func foldString(s string) string {
	return cases.Fold().String(s)
}

func mutstringBuiltins(env *Environment) {
	env.SetBuiltin("make-string", "a fresh mutable string: (make-string k [char])", BuiltinFunc(func(args []Value, env *Environment) (Value, error) {
		if len(args) < 1 || len(args) > 2 {
			return nil, errors.New("make-string expects 1 or 2 arguments: (make-string k [char])")
		}
		k, ok := numIndex(args[0])
		if !ok || k < 0 {
			return nil, errors.New("make-string expects a non-negative integer length")
		}
		fill := ' '
		if len(args) == 2 {
			c, ok := charArg(args[1])
			if !ok {
				return nil, errors.New("make-string expects a character fill")
			}
			fill = c
		}
		runes := make([]rune, k)
		for i := range runes {
			runes[i] = fill
		}
		return &MutableString{runes: runes}, nil
	}))
	env.SetBuiltin("string", "a fresh mutable string of its character arguments", BuiltinFunc(func(args []Value, env *Environment) (Value, error) {
		runes := make([]rune, len(args))
		for i, arg := range args {
			c, ok := charArg(arg)
			if !ok {
				return nil, fmt.Errorf("string expects characters, got %s", PrintValue(arg))
			}
			runes[i] = c
		}
		return &MutableString{runes: runes}, nil
	}))
	env.SetBuiltin("string-copy", "a fresh mutable copy of a string: (string-copy s [start [end]])", BuiltinFunc(func(args []Value, env *Environment) (Value, error) {
		if len(args) < 1 || len(args) > 3 {
			return nil, errors.New("string-copy expects 1 to 3 arguments")
		}
		s, ok := stringText(args[0])
		if !ok {
			return nil, errors.New("string-copy expects a string")
		}
		runes := []rune(s)
		start, end, err := startEnd("string-copy", len(runes), args[1:])
		if err != nil {
			return nil, err
		}
		out := make([]rune, end-start)
		copy(out, runes[start:end])
		return &MutableString{runes: out}, nil
	}))
	env.SetBuiltin("string-set!", "replace the character at an index in place", BuiltinFunc(func(args []Value, env *Environment) (Value, error) {
		if len(args) != 3 {
			return nil, errors.New("string-set! expects 3 arguments: (string-set! s k char)")
		}
		m, err := mutableStringArg("string-set!", args[0])
		if err != nil {
			return nil, err
		}
		k, ok := numIndex(args[1])
		if !ok {
			return nil, errors.New("string-set! expects an integer index")
		}
		if k < 0 || k >= len(m.runes) {
			return nil, fmt.Errorf("string-set!: index %d out of bounds for length %d", k, len(m.runes))
		}
		c, ok := charArg(args[2])
		if !ok {
			return nil, errors.New("string-set! expects a character")
		}
		m.runes[k] = c
		return nil, nil
	}))
	env.SetBuiltin("string-fill!", "fill a string range in place: (string-fill! s char [start [end]])", BuiltinFunc(func(args []Value, env *Environment) (Value, error) {
		if len(args) < 2 || len(args) > 4 {
			return nil, errors.New("string-fill! expects 2 to 4 arguments: (string-fill! s char [start [end]])")
		}
		m, err := mutableStringArg("string-fill!", args[0])
		if err != nil {
			return nil, err
		}
		c, ok := charArg(args[1])
		if !ok {
			return nil, errors.New("string-fill! expects a character")
		}
		start, end, err := startEnd("string-fill!", len(m.runes), args[2:])
		if err != nil {
			return nil, err
		}
		for i := start; i < end; i++ {
			m.runes[i] = c
		}
		return nil, nil
	}))
	env.SetBuiltin("string-copy!", "copy a range between strings in place: (string-copy! to at from [start [end]])", BuiltinFunc(func(args []Value, env *Environment) (Value, error) {
		if len(args) < 3 || len(args) > 5 {
			return nil, errors.New("string-copy! expects 3 to 5 arguments: (string-copy! to at from [start [end]])")
		}
		to, err := mutableStringArg("string-copy!", args[0])
		if err != nil {
			return nil, err
		}
		at, ok := numIndex(args[1])
		if !ok || at < 0 {
			return nil, errors.New("string-copy! expects a non-negative integer at")
		}
		from, ok := stringText(args[2])
		if !ok {
			return nil, errors.New("string-copy! expects a string source")
		}
		fromRunes := []rune(from) // a copy, so to and from may alias
		start, end, err := startEnd("string-copy!", len(fromRunes), args[3:])
		if err != nil {
			return nil, err
		}
		if at+(end-start) > len(to.runes) {
			return nil, errors.New("string-copy!: destination range out of bounds")
		}
		copy(to.runes[at:], fromRunes[start:end])
		return nil, nil
	}))
	env.SetBuiltin("string-ref", "the character at an index: (string-ref s k)", BuiltinFunc(func(args []Value, env *Environment) (Value, error) {
		if len(args) != 2 {
			return nil, errors.New("string-ref expects 2 arguments: (string-ref s k)")
		}
		s, ok := stringText(args[0])
		if !ok {
			return nil, errors.New("string-ref expects a string")
		}
		k, ok := numIndex(args[1])
		if !ok {
			return nil, errors.New("string-ref expects an integer index")
		}
		runes := []rune(s)
		if k < 0 || k >= len(runes) {
			return nil, fmt.Errorf("string-ref: index %d out of bounds for length %d", k, len(runes))
		}
		return Char(runes[k]), nil
	}))
	env.SetBuiltin("string->list", "a string's characters as a list: (string->list s [start [end]])", BuiltinFunc(func(args []Value, env *Environment) (Value, error) {
		if len(args) < 1 || len(args) > 3 {
			return nil, errors.New("string->list expects 1 to 3 arguments")
		}
		s, ok := stringText(args[0])
		if !ok {
			return nil, errors.New("string->list expects a string")
		}
		runes := []rune(s)
		start, end, err := startEnd("string->list", len(runes), args[1:])
		if err != nil {
			return nil, err
		}
		out := make([]Value, end-start)
		for i, r := range runes[start:end] {
			out[i] = Char(r)
		}
		return listFromSlice(out), nil
	}))
	env.SetBuiltin("list->string", "a list of characters as a string", BuiltinFunc(func(args []Value, env *Environment) (Value, error) {
		if len(args) != 1 {
			return nil, errors.New("list->string expects 1 argument")
		}
		lst, ok, err := AsList(args[0])
		if err != nil {
			return nil, err
		}
		if !ok {
			return nil, errors.New("list->string expects a list of characters")
		}
		runes := make([]rune, len(lst))
		for i, v := range lst {
			c, ok := charArg(v)
			if !ok {
				return nil, fmt.Errorf("list->string expects characters, got %s", PrintValue(v))
			}
			runes[i] = c
		}
		return String(runes), nil
	}))

	env.SetBuiltin("string<?", "lexicographic string less-than across arguments", stringCompare("string<?", false, func(a, b string) bool { return a < b }))
	env.SetBuiltin("string<=?", "lexicographic string less-or-equal across arguments", stringCompare("string<=?", false, func(a, b string) bool { return a <= b }))
	env.SetBuiltin("string>?", "lexicographic string greater-than across arguments", stringCompare("string>?", false, func(a, b string) bool { return a > b }))
	env.SetBuiltin("string>=?", "lexicographic string greater-or-equal across arguments", stringCompare("string>=?", false, func(a, b string) bool { return a >= b }))
	env.SetBuiltin("string-ci=?", "case-insensitive string equality", stringCompare("string-ci=?", true, func(a, b string) bool { return a == b }))
	env.SetBuiltin("string-ci<?", "case-insensitive string less-than", stringCompare("string-ci<?", true, func(a, b string) bool { return a < b }))
	env.SetBuiltin("string-ci<=?", "case-insensitive string less-or-equal", stringCompare("string-ci<=?", true, func(a, b string) bool { return a <= b }))
	env.SetBuiltin("string-ci>?", "case-insensitive string greater-than", stringCompare("string-ci>?", true, func(a, b string) bool { return a > b }))
	env.SetBuiltin("string-ci>=?", "case-insensitive string greater-or-equal", stringCompare("string-ci>=?", true, func(a, b string) bool { return a >= b }))
	Register("string-foldcase", "the case-folded form of a string", CommandMeta{})
	env.Set("string-foldcase", BuiltinFunc(func(args []Value, env *Environment) (Value, error) {
		if len(args) != 1 {
			return nil, errors.New("string-foldcase expects 1 argument")
		}
		s, ok, err := AsString(args[0])
		if err != nil {
			return nil, err
		}
		if !ok {
			return nil, errors.New("string-foldcase expects a string")
		}
		return String(foldString(s)), nil
	}))

	stringWalk := func(name string, collect bool) BuiltinFunc {
		return func(args []Value, env *Environment) (Value, error) {
			if len(args) < 2 {
				return nil, fmt.Errorf("%s expects at least 2 arguments: (%s proc string...)", name, name)
			}
			fn := args[0]
			runes := make([][]rune, len(args)-1)
			min := -1
			for i, arg := range args[1:] {
				s, ok := stringText(arg)
				if !ok {
					return nil, fmt.Errorf("%s expects strings", name)
				}
				runes[i] = []rune(s)
				if min < 0 || len(runes[i]) < min {
					min = len(runes[i])
				}
			}
			var out []rune
			if collect {
				out = make([]rune, 0, min)
			}
			chars := make([]Value, len(runes))
			for i := 0; i < min; i++ {
				for j, rs := range runes {
					chars[j] = Char(rs[i])
				}
				res, err := callFunction(fn, chars, env)
				if err != nil {
					return nil, err
				}
				if collect {
					c, ok := res.(Char)
					if !ok {
						return nil, fmt.Errorf("%s: proc must return a character, got %s", name, PrintValue(res))
					}
					out = append(out, rune(c))
				}
			}
			if collect {
				return String(out), nil
			}
			return nil, nil
		}
	}
	env.SetBuiltin("string-map", "map a procedure over strings' characters: (string-map proc string...)", stringWalk("string-map", true))
	env.SetBuiltin("string-for-each", "call a procedure on each character for its effects: (string-for-each proc string...)", stringWalk("string-for-each", false))
}
