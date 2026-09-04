package eval

import (
	"errors"
	"fmt"
	"math"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"golang.org/x/text/cases"
	"golang.org/x/text/language"
)

// Apply invokes any procedure value with the given arguments on this
// evaluator, so dynamic state riding on it (caller identity, approval gate)
// stays in force for the call.
func (e *Evaluator) Apply(fn Value, args []Value, env *Environment) (Value, error) {
	switch f := fn.(type) {
	case BuiltinFunc:
		v, err := f(args, env)
		if tc, ok := v.(*TailCall); ok {
			return e.runTail(tc)
		}
		return v, err
	case *FastBuiltin:
		return f.Fn(args, env)
	case *Lambda:
		return e.applyLambda(f, args)
	case *CaseLambda:
		l := f.match(len(args))
		if l == nil {
			return nil, fmt.Errorf("case-lambda: no clause matches %d arguments", len(args))
		}
		return e.applyLambda(l, args)
	case *Continuation:
		return nil, f.invoke(args)
	case *Parameter:
		if len(args) != 0 {
			return nil, errors.New("a parameter object takes no arguments")
		}
		return f.current(), nil
	default:
		return nil, errors.New("expected a procedure")
	}
}

func callFunction(fn Value, args []Value, env *Environment) (Value, error) {
	if ev := rootOf(env).ev; ev != nil {
		return ev.Apply(fn, args, env)
	}
	return (&Evaluator{globalEnv: env}).Apply(fn, args, env)
}

func stdlibBuiltins(env *Environment) {
	// Numeric predicates
	numPred := func(name string, pred func(f float64) bool) BuiltinFunc {
		return func(args []Value, env *Environment) (Value, error) {
			if len(args) != 1 {
				return nil, fmt.Errorf("%s expects 1 argument", name)
			}
			f, ok := numFloat(args[0])
			if !ok {
				return nil, fmt.Errorf("%s expects a number", name)
			}
			return pred(f), nil
		}
	}
	env.SetBuiltin("zero?", "whether a number is zero", numPred("zero?", func(f float64) bool { return f == 0 }))
	env.SetBuiltin("positive?", "whether a number is greater than zero", numPred("positive?", func(f float64) bool { return f > 0 }))
	env.SetBuiltin("negative?", "whether a number is less than zero", numPred("negative?", func(f float64) bool { return f < 0 }))
	parity := func(name string, want int64) BuiltinFunc {
		return func(args []Value, env *Environment) (Value, error) {
			if len(args) != 1 {
				return nil, fmt.Errorf("%s expects 1 argument", name)
			}
			n, _, ok := numInt64(args[0])
			if !ok {
				if !isNumber(args[0]) {
					return nil, fmt.Errorf("%s expects a number", name)
				}
				return nil, fmt.Errorf("%s expects an integer", name)
			}
			m := n % 2
			if m < 0 {
				m = -m
			}
			return m == want, nil
		}
	}
	env.SetBuiltin("even?", "whether an integer is even", parity("even?", 0))
	env.SetBuiltin("odd?", "whether an integer is odd", parity("odd?", 1))

	// Exactness (D1): exact integers are int64, inexact numbers float64.
	env.SetBuiltin("exact?", "whether a number is exact (an integer)", BuiltinFunc(func(args []Value, env *Environment) (Value, error) {
		if len(args) != 1 {
			return nil, errors.New("exact? expects 1 argument")
		}
		if !isNumber(args[0]) {
			return nil, errors.New("exact? expects a number")
		}
		_, ok := args[0].(Integer)
		return ok, nil
	}))
	env.SetBuiltin("inexact?", "whether a number is inexact (a float)", BuiltinFunc(func(args []Value, env *Environment) (Value, error) {
		if len(args) != 1 {
			return nil, errors.New("inexact? expects 1 argument")
		}
		if !isNumber(args[0]) {
			return nil, errors.New("inexact? expects a number")
		}
		_, ok := args[0].(Number)
		return ok, nil
	}))
	env.SetBuiltin("exact-integer?", "whether a value is an exact integer", BuiltinFunc(func(args []Value, env *Environment) (Value, error) {
		if len(args) != 1 {
			return nil, errors.New("exact-integer? expects 1 argument")
		}
		_, ok := args[0].(Integer)
		return ok, nil
	}))
	env.SetBuiltin("integer?", "whether a value is an integer (exact or integral inexact)", BuiltinFunc(func(args []Value, env *Environment) (Value, error) {
		if len(args) != 1 {
			return nil, errors.New("integer? expects 1 argument")
		}
		_, _, ok := numInt64(args[0])
		return ok, nil
	}))
	env.SetBuiltin("real?", "whether a value is a real number", BuiltinFunc(func(args []Value, env *Environment) (Value, error) {
		if len(args) != 1 {
			return nil, errors.New("real? expects 1 argument")
		}
		return isNumber(args[0]), nil
	}))
	env.SetBuiltin("complex?", "whether a value is a number (no complex tower)", BuiltinFunc(func(args []Value, env *Environment) (Value, error) {
		if len(args) != 1 {
			return nil, errors.New("complex? expects 1 argument")
		}
		return isNumber(args[0]), nil
	}))
	env.SetBuiltin("rational?", "whether a value is a rational number (finite real)", BuiltinFunc(func(args []Value, env *Environment) (Value, error) {
		if len(args) != 1 {
			return nil, errors.New("rational? expects 1 argument")
		}
		f, ok := numFloat(args[0])
		if !ok {
			return false, nil
		}
		return !math.IsInf(f, 0) && !math.IsNaN(f), nil
	}))
	exactFn := BuiltinFunc(func(args []Value, env *Environment) (Value, error) {
		if len(args) != 1 {
			return nil, errors.New("exact expects 1 argument")
		}
		return toExact(args[0])
	})
	inexactFn := BuiltinFunc(func(args []Value, env *Environment) (Value, error) {
		if len(args) != 1 {
			return nil, errors.New("inexact expects 1 argument")
		}
		v, ok := toInexact(args[0])
		if !ok {
			return nil, errors.New("inexact expects a number")
		}
		return v, nil
	})
	env.SetBuiltin("exact", "the exact form of a number (errors when none exists)", exactFn)
	env.SetBuiltin("inexact", "the inexact (float) form of a number", inexactFn)
	// (scheme r5rs) spellings (Phase 7).
	env.SetBuiltin("inexact->exact", "R5RS name for exact", exactFn)
	env.SetBuiltin("exact->inexact", "R5RS name for inexact", inexactFn)

	// String operations
	env.SetBuiltin("string-length", "the number of characters in a string", BuiltinFunc(func(args []Value, env *Environment) (Value, error) {
		if len(args) != 1 {
			return nil, errors.New("string-length expects 1 argument")
		}
		s, ok, err := AsString(args[0])
		if err != nil {
			return nil, err
		}
		if !ok {
			return nil, errors.New("string-length expects a string")
		}
		return Integer(len([]rune(s))), nil
	}))
	env.SetBuiltin("substring", "a slice of a string: (substring s start [end])", BuiltinFunc(func(args []Value, env *Environment) (Value, error) {
		if len(args) < 2 || len(args) > 3 {
			return nil, errors.New("substring expects 2 or 3 arguments: (substring string start [end])")
		}
		s, ok, err := AsString(args[0])
		if err != nil {
			return nil, err
		}
		if !ok {
			return nil, errors.New("substring expects a string as first argument")
		}
		start, ok := numIndex(args[1])
		if !ok {
			return nil, errors.New("substring expects a number as second argument")
		}
		runes := []rune(s)
		end := len(runes)
		if len(args) == 3 {
			end, ok = numIndex(args[2])
			if !ok {
				return nil, errors.New("substring expects a number as third argument")
			}
		}
		if start < 0 || end > len(runes) || start > end {
			return nil, errors.New("substring: index out of bounds")
		}
		return String(runes[start:end]), nil
	}))
	env.SetBuiltin("string-append", "concatenate strings", BuiltinFunc(func(args []Value, env *Environment) (Value, error) {
		var sb strings.Builder
		for _, arg := range args {
			s, ok := stringText(arg)
			if !ok {
				return nil, errors.New("string-append expects all arguments to be strings")
			}
			sb.WriteString(s)
		}
		return String(sb.String()), nil
	}))
	env.SetBuiltin("string-split", "split a string on a separator into a list of strings", BuiltinFunc(func(args []Value, env *Environment) (Value, error) {
		if len(args) != 2 {
			return nil, errors.New("string-split expects 2 arguments: (string-split string separator)")
		}
		s, ok1, err := AsString(args[0])
		if err != nil {
			return nil, err
		}
		sep, ok2 := args[1].(String)
		if !ok1 || !ok2 {
			return nil, errors.New("string-split expects strings")
		}
		parts := strings.Split(s, string(sep))
		result := make([]Value, len(parts))
		for i, p := range parts {
			result[i] = String(p)
		}
		return result, nil
	}))
	env.SetBuiltin("string-join", "join a list of strings with a separator", BuiltinFunc(func(args []Value, env *Environment) (Value, error) {
		if len(args) != 2 {
			return nil, errors.New("string-join expects 2 arguments: (string-join list separator)")
		}
		lst, ok, err := AsList(args[0])
		if err != nil {
			return nil, err
		}
		if !ok {
			return nil, errors.New("string-join expects a list as first argument")
		}
		sep, ok := args[1].(String)
		if !ok {
			return nil, errors.New("string-join expects a string as second argument")
		}
		parts := make([]string, len(lst))
		for i, v := range lst {
			s, ok := v.(String)
			if !ok {
				return nil, errors.New("string-join expects a list of strings")
			}
			parts[i] = string(s)
		}
		return String(strings.Join(parts, string(sep))), nil
	}))
	Register("string-upcase", "uppercase a string (full Unicode case mapping)", CommandMeta{Stage: true})
	env.Set("string-upcase", BuiltinFunc(func(args []Value, env *Environment) (Value, error) {
		if len(args) != 1 {
			return nil, errors.New("string-upcase expects 1 argument")
		}
		s, ok, err := AsString(args[0])
		if err != nil {
			return nil, err
		}
		if !ok {
			return nil, errors.New("string-upcase expects a string")
		}
		return String(cases.Upper(language.Und).String(s)), nil
	}))
	Register("string-downcase", "lowercase a string (full Unicode case mapping)", CommandMeta{Stage: true})
	env.Set("string-downcase", BuiltinFunc(func(args []Value, env *Environment) (Value, error) {
		if len(args) != 1 {
			return nil, errors.New("string-downcase expects 1 argument")
		}
		s, ok, err := AsString(args[0])
		if err != nil {
			return nil, err
		}
		if !ok {
			return nil, errors.New("string-downcase expects a string")
		}
		return String(cases.Lower(language.Und).String(s)), nil
	}))
	env.SetBuiltin("string-contains?", "whether a string contains a substring", BuiltinFunc(func(args []Value, env *Environment) (Value, error) {
		if len(args) != 2 {
			return nil, errors.New("string-contains? expects 2 arguments: (string-contains? string substring)")
		}
		s, ok1, err := AsString(args[0])
		if err != nil {
			return nil, err
		}
		sub, ok2 := args[1].(String)
		if !ok1 || !ok2 {
			return nil, errors.New("string-contains? expects strings")
		}
		return strings.Contains(s, string(sub)), nil
	}))
	env.SetBuiltin("string-trim", "strip whitespace (or a cutset's characters) from both ends", BuiltinFunc(func(args []Value, env *Environment) (Value, error) {
		if len(args) < 1 || len(args) > 2 {
			return nil, errors.New("string-trim expects 1 or 2 arguments: (string-trim string [cutset])")
		}
		s, ok, err := AsString(args[0])
		if err != nil {
			return nil, err
		}
		if !ok {
			return nil, errors.New("string-trim expects a string")
		}
		if len(args) == 2 {
			cutset, ok := args[1].(String)
			if !ok {
				return nil, errors.New("string-trim expects a string cutset")
			}
			return String(strings.Trim(s, string(cutset))), nil
		}
		return String(strings.TrimSpace(s)), nil
	}))
	env.SetBuiltin("string-replace", "replace every occurrence of a substring", BuiltinFunc(func(args []Value, env *Environment) (Value, error) {
		if len(args) != 3 {
			return nil, errors.New("string-replace expects 3 arguments: (string-replace string old new)")
		}
		s, ok1, err := AsString(args[0])
		if err != nil {
			return nil, err
		}
		old, ok2 := args[1].(String)
		repl, ok3 := args[2].(String)
		if !ok1 || !ok2 || !ok3 {
			return nil, errors.New("string-replace expects strings")
		}
		if old == "" {
			return nil, errors.New("string-replace: old substring cannot be empty")
		}
		return String(strings.ReplaceAll(s, string(old), string(repl))), nil
	}))
	env.SetBuiltin("string-prefix?", "whether a string starts with a prefix", BuiltinFunc(func(args []Value, env *Environment) (Value, error) {
		if len(args) != 2 {
			return nil, errors.New("string-prefix? expects 2 arguments: (string-prefix? string prefix)")
		}
		s, ok1, err := AsString(args[0])
		if err != nil {
			return nil, err
		}
		prefix, ok2 := args[1].(String)
		if !ok1 || !ok2 {
			return nil, errors.New("string-prefix? expects strings")
		}
		return strings.HasPrefix(s, string(prefix)), nil
	}))
	env.SetBuiltin("string-suffix?", "whether a string ends with a suffix", BuiltinFunc(func(args []Value, env *Environment) (Value, error) {
		if len(args) != 2 {
			return nil, errors.New("string-suffix? expects 2 arguments: (string-suffix? string suffix)")
		}
		s, ok1, err := AsString(args[0])
		if err != nil {
			return nil, err
		}
		suffix, ok2 := args[1].(String)
		if !ok1 || !ok2 {
			return nil, errors.New("string-suffix? expects strings")
		}
		return strings.HasSuffix(s, string(suffix)), nil
	}))
	env.SetBuiltin("match", "regex match: a list of the match and capture groups, else false", BuiltinFunc(func(args []Value, env *Environment) (Value, error) {
		if len(args) != 2 {
			return nil, errors.New("match expects 2 arguments: (match pattern string)")
		}
		pattern, ok1 := args[0].(String)
		s, ok2, err := AsString(args[1])
		if err != nil {
			return nil, err
		}
		if !ok1 || !ok2 {
			return nil, errors.New("match expects strings")
		}
		re, err := regexp.Compile(string(pattern))
		if err != nil {
			return nil, fmt.Errorf("match: %v", err)
		}
		groups := re.FindStringSubmatch(s)
		if groups == nil {
			return false, nil
		}
		result := make([]Value, len(groups))
		for i, g := range groups {
			result[i] = String(g)
		}
		return result, nil
	}))
	env.SetBuiltin("string->number", "parse a string as a number: (string->number s [radix])", BuiltinFunc(func(args []Value, env *Environment) (Value, error) {
		if len(args) < 1 || len(args) > 2 {
			return nil, errors.New("string->number expects 1 or 2 arguments: (string->number s [radix])")
		}
		s, ok := args[0].(String)
		if !ok {
			return nil, errors.New("string->number expects a string")
		}
		text := string(s)
		if len(args) == 2 {
			radix, ok := numIndex(args[1])
			if !ok {
				return nil, errors.New("string->number: radix must be a number")
			}
			prefix := ""
			switch radix {
			case 2:
				prefix = "#b"
			case 8:
				prefix = "#o"
			case 10:
				prefix = "#d"
			case 16:
				prefix = "#x"
			default:
				return nil, errors.New("string->number: radix must be 2, 8, 10, or 16")
			}
			hasRadix := false
			for t := text; len(t) >= 2 && t[0] == '#'; t = t[2:] {
				switch t[1] | 0x20 {
				case 'b', 'o', 'd', 'x':
					hasRadix = true
				}
			}
			if !hasRadix {
				text = prefix + text
			}
		}
		if n, ok := parseSchemeNumber(text); ok {
			return n, nil
		}
		return false, nil // Scheme convention: #f on failure
	}))
	env.SetBuiltin("number->string", "format a number as a string: (number->string n [radix])", BuiltinFunc(func(args []Value, env *Environment) (Value, error) {
		if len(args) < 1 || len(args) > 2 {
			return nil, errors.New("number->string expects 1 or 2 arguments: (number->string n [radix])")
		}
		radix := 10
		if len(args) == 2 {
			r, ok := numIndex(args[1])
			if !ok || (r != 2 && r != 8 && r != 10 && r != 16) {
				return nil, errors.New("number->string: radix must be 2, 8, 10, or 16")
			}
			radix = r
		}
		switch n := args[0].(type) {
		case Integer:
			return String(strconv.FormatInt(int64(n), radix)), nil
		case Number:
			if radix != 10 {
				return nil, errors.New("number->string: non-decimal radix requires an exact integer")
			}
			return String(n.String()), nil
		}
		return nil, errors.New("number->string expects a number")
	}))
	env.SetBuiltin("string->symbol", "intern a string as a symbol", BuiltinFunc(func(args []Value, env *Environment) (Value, error) {
		if len(args) != 1 {
			return nil, errors.New("string->symbol expects 1 argument")
		}
		s, ok := args[0].(String)
		if !ok {
			return nil, errors.New("string->symbol expects a string")
		}
		return Symbol(s), nil
	}))
	env.SetBuiltin("features", "the feature identifiers this implementation advertises", BuiltinFunc(func(args []Value, env *Environment) (Value, error) {
		if len(args) != 0 {
			return nil, errors.New("features expects no arguments")
		}
		names := featureNames()
		feats := make([]Value, len(names))
		for i, n := range names {
			feats[i] = Symbol(n)
		}
		return feats, nil
	}))
	env.SetBuiltin("symbol->string", "a symbol's name as a string", BuiltinFunc(func(args []Value, env *Environment) (Value, error) {
		if len(args) != 1 {
			return nil, errors.New("symbol->string expects 1 argument")
		}
		sym, ok := args[0].(Symbol)
		if !ok {
			return nil, errors.New("symbol->string expects a symbol")
		}
		return String(sym), nil
	}))
	env.SetBuiltin("string=?", "whether strings are equal", stringCompare("string=?", false, func(a, b string) bool { return a == b }))

	foldLeft := BuiltinFunc(func(args []Value, env *Environment) (Value, error) {
		if len(args) != 3 {
			return nil, errors.New("fold-left expects 3 arguments: (fold-left fn init list)")
		}
		lst, ok, err := AsList(args[2])
		if err != nil {
			return nil, err
		}
		if !ok {
			return nil, errors.New("fold-left expects a list as third argument")
		}
		acc := args[1]
		for _, v := range lst {
			acc, err = callFunction(args[0], []Value{acc, v}, env)
			if err != nil {
				return nil, err
			}
		}
		return acc, nil
	})
	env.SetBuiltin("fold-left", "fold a list from the left: (fold-left fn init lst)", foldLeft)
	env.SetBuiltin("reduce", "alias of fold-left: (reduce fn init lst)", foldLeft)
	env.SetBuiltin("fold-right", "fold a list from the right: (fold-right fn init lst)", BuiltinFunc(func(args []Value, env *Environment) (Value, error) {
		if len(args) != 3 {
			return nil, errors.New("fold-right expects 3 arguments: (fold-right fn init list)")
		}
		lst, ok, err := AsList(args[2])
		if err != nil {
			return nil, err
		}
		if !ok {
			return nil, errors.New("fold-right expects a list as third argument")
		}
		acc := args[1]
		for i := len(lst) - 1; i >= 0; i-- {
			acc, err = callFunction(args[0], []Value{lst[i], acc}, env)
			if err != nil {
				return nil, err
			}
		}
		return acc, nil
	}))
	env.SetBuiltin("sort", "a sorted copy of a list (stable; optional less? comparator)", BuiltinFunc(func(args []Value, env *Environment) (Value, error) {
		if len(args) < 1 || len(args) > 2 {
			return nil, errors.New("sort expects 1 or 2 arguments: (sort list [less?])")
		}
		lst, ok, err := AsList(args[0])
		if err != nil {
			return nil, err
		}
		if !ok {
			return nil, errors.New("sort expects a list as first argument")
		}
		result := make([]Value, len(lst))
		copy(result, lst)
		var sortErr error
		if len(args) == 2 {
			cmp := args[1]
			sort.SliceStable(result, func(i, j int) bool {
				if sortErr != nil {
					return false
				}
				res, err := callFunction(cmp, []Value{result[i], result[j]}, env)
				if err != nil {
					sortErr = err
					return false
				}
				b, ok := res.(bool)
				return ok && b
			})
		} else {
			sort.SliceStable(result, func(i, j int) bool {
				if sortErr != nil {
					return false
				}
				if af, aok := numFloat(result[i]); aok {
					bf, bok := numFloat(result[j])
					if !bok {
						sortErr = errors.New("sort expects a list of all numbers or all strings")
						return false
					}
					return af < bf
				}
				if a, ok := result[i].(String); ok {
					b, ok := result[j].(String)
					if !ok {
						sortErr = errors.New("sort expects a list of all numbers or all strings")
						return false
					}
					return a < b
				}
				sortErr = errors.New("sort expects a list of all numbers or all strings")
				return false
			})
		}
		if sortErr != nil {
			return nil, sortErr
		}
		return result, nil
	}))

	// let* macro
	Register("let*", "bind locals sequentially, each seeing the previous ones", CommandMeta{})
	env.SetMacro("let*", func(args []Value) (Value, error) {
		if len(args) < 2 {
			return nil, errors.New("let*: invalid syntax. Expected (let* (bindings) body...)")
		}
		bindings, ok := args[0].([]Value)
		if !ok {
			return nil, errors.New("let*: first argument must be a list of bindings")
		}
		body := args[1:]
		for _, binding := range bindings {
			pair, ok := binding.([]Value)
			if !ok || len(pair) != 2 {
				return nil, errors.New("let*: each binding must be a (symbol value) pair")
			}
		}
		if len(bindings) <= 1 {
			letForm := []Value{Symbol("let"), bindings}
			return append(letForm, body...), nil
		}
		inner := append([]Value{Symbol("let*"), bindings[1:]}, body...)
		return []Value{Symbol("let"), []Value{bindings[0]}, inner}, nil
	})

	// when macro
	Register("when", "evaluate body forms if a test is true", CommandMeta{})
	env.SetMacro("when", func(args []Value) (Value, error) {
		// (when test body...) expands to (if test (begin body...) nil)
		if len(args) < 2 {
			return nil, errors.New("when: invalid syntax. Expected (when test body...)")
		}
		body := append([]Value{Symbol("begin")}, args[1:]...)
		return []Value{Symbol("if"), args[0], body, nil}, nil
	})

	// unless macro
	Register("unless", "evaluate body forms if a test is false", CommandMeta{})
	env.SetMacro("unless", func(args []Value) (Value, error) {
		// (unless test body...) expands to (if test nil (begin body...))
		if len(args) < 2 {
			return nil, errors.New("unless: invalid syntax. Expected (unless test body...)")
		}
		body := append([]Value{Symbol("begin")}, args[1:]...)
		return []Value{Symbol("if"), args[0], nil, body}, nil
	})

	// case macro
	Register("case", "dispatch on an expression's value: (case expr ((datum...) body...) ...)", CommandMeta{})
	env.SetMacro("case", func(args []Value) (Value, error) {
		if len(args) < 2 {
			return nil, errors.New("case: invalid syntax. Expected (case expr clauses...)")
		}
		tmp := gensym("case")
		clauses := args[1:]
		clauseBody := func(cl []Value) ([]Value, error) {
			if len(cl) == 3 && symIsBase(cl[1], "=>") {
				return []Value{[]Value{cl[2], tmp}}, nil
			}
			for _, form := range cl[1:] {
				if symIsBase(form, "=>") {
					return nil, errors.New("case: => expects a single receiver: (datums => proc)")
				}
			}
			return cl[1:], nil
		}
		condClauses := make([]Value, 0, len(clauses))
		for i, clause := range clauses {
			cl, ok := clause.([]Value)
			if !ok {
				return nil, errors.New("case: clause must be a list")
			}
			if len(cl) < 2 {
				return nil, errors.New("case: clause requires at least one expression")
			}
			body, err := clauseBody(cl)
			if err != nil {
				return nil, err
			}
			if symIsBase(cl[0], "else") {
				if i != len(clauses)-1 {
					return nil, errors.New("case: else must be the last clause")
				}
				condClauses = append(condClauses, append([]Value{Symbol("else")}, body...))
				continue
			}
			datums, ok := cl[0].([]Value)
			if !ok {
				return nil, errors.New("case: clause datums must be a list")
			}
			// R7RS case matches datums with eqv? — memv, not member.
			test := []Value{Symbol("memv"), tmp, []Value{Symbol("quote"), datums}}
			condClauses = append(condClauses, append([]Value{Value(test)}, body...))
		}
		condForm := append([]Value{Symbol("cond")}, condClauses...)
		return []Value{Symbol("let"), []Value{[]Value{tmp, args[0]}}, condForm}, nil
	})

	Register("quasiquote", "quote a template with ,unquote and ,@splicing holes", CommandMeta{})
	Register("unquote", "evaluate a hole inside a quasiquote template", CommandMeta{})
	env.SetSpecialForm("unquote", func(args []Value, env *Environment, e *Evaluator) (Value, error) {
		return nil, errors.New("unquote: not inside quasiquote")
	})
	Register("unquote-splicing", "splice a list into a quasiquote template", CommandMeta{})
	env.SetSpecialForm("unquote-splicing", func(args []Value, env *Environment, e *Evaluator) (Value, error) {
		return nil, errors.New("unquote-splicing: not inside quasiquote")
	})
}
