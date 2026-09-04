package eval

import (
	"errors"
	"fmt"
	"unicode"
)

func charArg(v Value) (rune, bool) {
	c, ok := v.(Char)
	return rune(c), ok
}

func charFold(r rune) rune {
	return unicode.ToLower(unicode.ToUpper(r))
}

func charCompare(name string, fold bool, cmp func(a, b rune) bool) BuiltinFunc {
	return func(args []Value, env *Environment) (Value, error) {
		if len(args) < 2 {
			return nil, fmt.Errorf("%s expects at least 2 arguments", name)
		}
		prev, ok := charArg(args[0])
		if !ok {
			return nil, fmt.Errorf("%s expects characters, got %s", name, PrintValue(args[0]))
		}
		if fold {
			prev = charFold(prev)
		}
		result := true
		for _, arg := range args[1:] {
			r, ok := charArg(arg)
			if !ok {
				return nil, fmt.Errorf("%s expects characters, got %s", name, PrintValue(arg))
			}
			if fold {
				r = charFold(r)
			}
			if !cmp(prev, r) {
				result = false
			}
			prev = r
		}
		return result, nil
	}
}

func charClass(name string, pred func(r rune) bool) BuiltinFunc {
	return func(args []Value, env *Environment) (Value, error) {
		if len(args) != 1 {
			return nil, fmt.Errorf("%s expects 1 argument", name)
		}
		r, ok := charArg(args[0])
		if !ok {
			return nil, fmt.Errorf("%s expects a character", name)
		}
		return pred(r), nil
	}
}

func charConvert(name string, conv func(r rune) rune) BuiltinFunc {
	return func(args []Value, env *Environment) (Value, error) {
		if len(args) != 1 {
			return nil, fmt.Errorf("%s expects 1 argument", name)
		}
		r, ok := charArg(args[0])
		if !ok {
			return nil, fmt.Errorf("%s expects a character", name)
		}
		return Char(conv(r)), nil
	}
}

func digitValue(r rune) (int, bool) {
	if !unicode.Is(unicode.Nd, r) {
		return 0, false
	}
	for _, rng := range unicode.Nd.R16 {
		if r >= rune(rng.Lo) && r <= rune(rng.Hi) {
			return int(r-rune(rng.Lo)) % 10, true
		}
	}
	for _, rng := range unicode.Nd.R32 {
		if r >= rune(rng.Lo) && r <= rune(rng.Hi) {
			return int(r-rune(rng.Lo)) % 10, true
		}
	}
	return 0, false
}

func charBuiltins(env *Environment) {
	env.SetBuiltin("char?", "true when the value is a character", BuiltinFunc(func(args []Value, env *Environment) (Value, error) {
		if len(args) != 1 {
			return nil, errors.New("char? expects 1 argument")
		}
		_, ok := args[0].(Char)
		return ok, nil
	}))
	env.SetBuiltin("char->integer", "a character's Unicode code point", BuiltinFunc(func(args []Value, env *Environment) (Value, error) {
		if len(args) != 1 {
			return nil, errors.New("char->integer expects 1 argument")
		}
		r, ok := charArg(args[0])
		if !ok {
			return nil, errors.New("char->integer expects a character")
		}
		return Integer(r), nil
	}))
	env.SetBuiltin("integer->char", "the character at a Unicode code point", BuiltinFunc(func(args []Value, env *Environment) (Value, error) {
		if len(args) != 1 {
			return nil, errors.New("integer->char expects 1 argument")
		}
		n, ok := numIndex(args[0])
		if !ok {
			return nil, errors.New("integer->char expects an integer")
		}
		if n < 0 || n > int(unicode.MaxRune) || (n >= 0xD800 && n <= 0xDFFF) {
			return nil, fmt.Errorf("integer->char: %d is not a valid code point", n)
		}
		return Char(rune(n)), nil
	}))

	env.SetBuiltin("char=?", "character equality across arguments", charCompare("char=?", false, func(a, b rune) bool { return a == b }))
	env.SetBuiltin("char<?", "character code-point less-than across arguments", charCompare("char<?", false, func(a, b rune) bool { return a < b }))
	env.SetBuiltin("char<=?", "character code-point less-or-equal across arguments", charCompare("char<=?", false, func(a, b rune) bool { return a <= b }))
	env.SetBuiltin("char>?", "character code-point greater-than across arguments", charCompare("char>?", false, func(a, b rune) bool { return a > b }))
	env.SetBuiltin("char>=?", "character code-point greater-or-equal across arguments", charCompare("char>=?", false, func(a, b rune) bool { return a >= b }))

	env.SetBuiltin("char-ci=?", "case-insensitive character equality", charCompare("char-ci=?", true, func(a, b rune) bool { return a == b }))
	env.SetBuiltin("char-ci<?", "case-insensitive character less-than", charCompare("char-ci<?", true, func(a, b rune) bool { return a < b }))
	env.SetBuiltin("char-ci<=?", "case-insensitive character less-or-equal", charCompare("char-ci<=?", true, func(a, b rune) bool { return a <= b }))
	env.SetBuiltin("char-ci>?", "case-insensitive character greater-than", charCompare("char-ci>?", true, func(a, b rune) bool { return a > b }))
	env.SetBuiltin("char-ci>=?", "case-insensitive character greater-or-equal", charCompare("char-ci>=?", true, func(a, b rune) bool { return a >= b }))

	env.SetBuiltin("char-alphabetic?", "whether a character is a letter", charClass("char-alphabetic?", unicode.IsLetter))
	env.SetBuiltin("char-numeric?", "whether a character is a decimal digit", charClass("char-numeric?", unicode.IsDigit))
	env.SetBuiltin("char-whitespace?", "whether a character is whitespace", charClass("char-whitespace?", unicode.IsSpace))
	env.SetBuiltin("char-upper-case?", "whether a character is upper case", charClass("char-upper-case?", unicode.IsUpper))
	env.SetBuiltin("char-lower-case?", "whether a character is lower case", charClass("char-lower-case?", unicode.IsLower))

	env.SetBuiltin("char-upcase", "the upper-case form of a character", charConvert("char-upcase", unicode.ToUpper))
	env.SetBuiltin("char-downcase", "the lower-case form of a character", charConvert("char-downcase", unicode.ToLower))
	env.SetBuiltin("char-foldcase", "the case-folded form of a character", charConvert("char-foldcase", charFold))

	env.SetBuiltin("digit-value", "a decimal digit's numeric value in any script, else false", BuiltinFunc(func(args []Value, env *Environment) (Value, error) {
		if len(args) != 1 {
			return nil, errors.New("digit-value expects 1 argument")
		}
		r, ok := charArg(args[0])
		if !ok {
			return nil, errors.New("digit-value expects a character")
		}
		if v, ok := digitValue(r); ok {
			return Integer(v), nil
		}
		return false, nil
	}))
}
