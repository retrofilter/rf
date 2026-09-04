package eval

import (
	"errors"
	"fmt"
	"unicode/utf8"
)

func startEnd(name string, length int, args []Value) (int, int, error) {
	start, end := 0, length
	if len(args) >= 1 {
		s, ok := numIndex(args[0])
		if !ok {
			return 0, 0, fmt.Errorf("%s: start must be an integer", name)
		}
		start = s
	}
	if len(args) >= 2 {
		e, ok := numIndex(args[1])
		if !ok {
			return 0, 0, fmt.Errorf("%s: end must be an integer", name)
		}
		end = e
	}
	if start < 0 || end > length || start > end {
		return 0, 0, fmt.Errorf("%s: range [%d, %d) out of bounds for length %d", name, start, end, length)
	}
	return start, end, nil
}

func vectorArg(name string, v Value) (Vector, error) {
	vec, ok := v.(Vector)
	if !ok {
		return nil, fmt.Errorf("%s expects a vector, got %s", name, PrintValue(v))
	}
	return vec, nil
}

func vectorBuiltins(env *Environment) {
	env.SetBuiltin("vector?", "true when the value is a vector", BuiltinFunc(func(args []Value, env *Environment) (Value, error) {
		if len(args) != 1 {
			return nil, errors.New("vector? expects 1 argument")
		}
		_, ok := args[0].(Vector)
		return ok, nil
	}))
	env.SetBuiltin("make-vector", "a fresh vector: (make-vector k [fill])", BuiltinFunc(func(args []Value, env *Environment) (Value, error) {
		if len(args) < 1 || len(args) > 2 {
			return nil, errors.New("make-vector expects 1 or 2 arguments: (make-vector k [fill])")
		}
		k, ok := numIndex(args[0])
		if !ok || k < 0 {
			return nil, errors.New("make-vector expects a non-negative integer length")
		}
		var fill Value = false // unspecified per R7RS; #f is a printable choice
		if len(args) == 2 {
			fill = args[1]
		}
		vec := make(Vector, k)
		for i := range vec {
			vec[i] = fill
		}
		return vec, nil
	}))
	env.SetBuiltin("vector", "a fresh vector of its arguments", BuiltinFunc(func(args []Value, env *Environment) (Value, error) {
		vec := make(Vector, len(args))
		copy(vec, args)
		return vec, nil
	}))
	env.SetBuiltin("vector-length", "the number of elements in a vector", BuiltinFunc(func(args []Value, env *Environment) (Value, error) {
		if len(args) != 1 {
			return nil, errors.New("vector-length expects 1 argument")
		}
		vec, err := vectorArg("vector-length", args[0])
		if err != nil {
			return nil, err
		}
		return Integer(len(vec)), nil
	}))
	env.SetBuiltin("vector-ref", "the element at an index: (vector-ref vec k)", BuiltinFunc(func(args []Value, env *Environment) (Value, error) {
		if len(args) != 2 {
			return nil, errors.New("vector-ref expects 2 arguments: (vector-ref vec k)")
		}
		vec, err := vectorArg("vector-ref", args[0])
		if err != nil {
			return nil, err
		}
		k, ok := numIndex(args[1])
		if !ok {
			return nil, errors.New("vector-ref expects an integer index")
		}
		if k < 0 || k >= len(vec) {
			return nil, fmt.Errorf("vector-ref: index %d out of bounds for length %d", k, len(vec))
		}
		return vec[k], nil
	}))
	env.SetBuiltin("vector-set!", "replace the element at an index in place", BuiltinFunc(func(args []Value, env *Environment) (Value, error) {
		if len(args) != 3 {
			return nil, errors.New("vector-set! expects 3 arguments: (vector-set! vec k obj)")
		}
		vec, err := vectorArg("vector-set!", args[0])
		if err != nil {
			return nil, err
		}
		k, ok := numIndex(args[1])
		if !ok {
			return nil, errors.New("vector-set! expects an integer index")
		}
		if k < 0 || k >= len(vec) {
			return nil, fmt.Errorf("vector-set!: index %d out of bounds for length %d", k, len(vec))
		}
		vec[k] = args[2]
		return nil, nil
	}))
	Register("vector->list", "a vector's elements as a fresh list: (vector->list vec [start [end]])", CommandMeta{})
	env.Set("vector->list", BuiltinFunc(func(args []Value, env *Environment) (Value, error) {
		if len(args) < 1 || len(args) > 3 {
			return nil, errors.New("vector->list expects 1 to 3 arguments")
		}
		vec, err := vectorArg("vector->list", args[0])
		if err != nil {
			return nil, err
		}
		start, end, err := startEnd("vector->list", len(vec), args[1:])
		if err != nil {
			return nil, err
		}
		return listFromSlice(vec[start:end]), nil
	}))
	Register("list->vector", "a list's elements as a fresh vector", CommandMeta{})
	env.Set("list->vector", BuiltinFunc(func(args []Value, env *Environment) (Value, error) {
		if len(args) != 1 {
			return nil, errors.New("list->vector expects 1 argument")
		}
		lst, ok, err := AsList(args[0])
		if err != nil {
			return nil, err
		}
		if !ok {
			return nil, errors.New("list->vector expects a list")
		}
		vec := make(Vector, len(lst))
		copy(vec, lst)
		return vec, nil
	}))
	env.SetBuiltin("vector->string", "a vector of characters as a string: (vector->string vec [start [end]])", BuiltinFunc(func(args []Value, env *Environment) (Value, error) {
		if len(args) < 1 || len(args) > 3 {
			return nil, errors.New("vector->string expects 1 to 3 arguments")
		}
		vec, err := vectorArg("vector->string", args[0])
		if err != nil {
			return nil, err
		}
		start, end, err := startEnd("vector->string", len(vec), args[1:])
		if err != nil {
			return nil, err
		}
		runes := make([]rune, 0, end-start)
		for _, v := range vec[start:end] {
			c, ok := v.(Char)
			if !ok {
				return nil, fmt.Errorf("vector->string expects a vector of characters, got %s", PrintValue(v))
			}
			runes = append(runes, rune(c))
		}
		return String(runes), nil
	}))
	env.SetBuiltin("string->vector", "a string's characters as a vector: (string->vector s [start [end]])", BuiltinFunc(func(args []Value, env *Environment) (Value, error) {
		if len(args) < 1 || len(args) > 3 {
			return nil, errors.New("string->vector expects 1 to 3 arguments")
		}
		s, ok := stringText(args[0])
		if !ok {
			return nil, errors.New("string->vector expects a string")
		}
		runes := []rune(s)
		start, end, err := startEnd("string->vector", len(runes), args[1:])
		if err != nil {
			return nil, err
		}
		vec := make(Vector, end-start)
		for i, r := range runes[start:end] {
			vec[i] = Char(r)
		}
		return vec, nil
	}))
	env.SetBuiltin("vector-copy", "a fresh copy of a vector: (vector-copy vec [start [end]])", BuiltinFunc(func(args []Value, env *Environment) (Value, error) {
		if len(args) < 1 || len(args) > 3 {
			return nil, errors.New("vector-copy expects 1 to 3 arguments")
		}
		vec, err := vectorArg("vector-copy", args[0])
		if err != nil {
			return nil, err
		}
		start, end, err := startEnd("vector-copy", len(vec), args[1:])
		if err != nil {
			return nil, err
		}
		out := make(Vector, end-start)
		copy(out, vec[start:end])
		return out, nil
	}))
	env.SetBuiltin("vector-copy!", "copy a range between vectors in place: (vector-copy! to at from [start [end]])", BuiltinFunc(func(args []Value, env *Environment) (Value, error) {
		if len(args) < 3 || len(args) > 5 {
			return nil, errors.New("vector-copy! expects 3 to 5 arguments: (vector-copy! to at from [start [end]])")
		}
		to, err := vectorArg("vector-copy!", args[0])
		if err != nil {
			return nil, err
		}
		at, ok := numIndex(args[1])
		if !ok || at < 0 {
			return nil, errors.New("vector-copy! expects a non-negative integer at")
		}
		from, err := vectorArg("vector-copy!", args[2])
		if err != nil {
			return nil, err
		}
		start, end, err := startEnd("vector-copy!", len(from), args[3:])
		if err != nil {
			return nil, err
		}
		if at+(end-start) > len(to) {
			return nil, errors.New("vector-copy!: destination range out of bounds")
		}
		copy(to[at:], from[start:end]) // copy is overlap-safe
		return nil, nil
	}))
	env.SetBuiltin("vector-append", "concatenate vectors into a fresh vector", BuiltinFunc(func(args []Value, env *Environment) (Value, error) {
		total := 0
		for _, arg := range args {
			vec, err := vectorArg("vector-append", arg)
			if err != nil {
				return nil, err
			}
			total += len(vec)
		}
		out := make(Vector, 0, total)
		for _, arg := range args {
			out = append(out, arg.(Vector)...)
		}
		return out, nil
	}))
	env.SetBuiltin("vector-fill!", "fill a vector range in place: (vector-fill! vec fill [start [end]])", BuiltinFunc(func(args []Value, env *Environment) (Value, error) {
		if len(args) < 2 || len(args) > 4 {
			return nil, errors.New("vector-fill! expects 2 to 4 arguments: (vector-fill! vec fill [start [end]])")
		}
		vec, err := vectorArg("vector-fill!", args[0])
		if err != nil {
			return nil, err
		}
		start, end, err := startEnd("vector-fill!", len(vec), args[2:])
		if err != nil {
			return nil, err
		}
		for i := start; i < end; i++ {
			vec[i] = args[1]
		}
		return nil, nil
	}))
	env.SetBuiltin("vector-map", "apply a function elementwise over vectors: (vector-map fn vec...)", BuiltinFunc(func(args []Value, env *Environment) (Value, error) {
		if len(args) < 2 {
			return nil, errors.New("vector-map expects at least 2 arguments: (vector-map fn vec...)")
		}
		vecs := make([]Vector, len(args)-1)
		shortest := -1
		for i, arg := range args[1:] {
			vec, err := vectorArg("vector-map", arg)
			if err != nil {
				return nil, err
			}
			vecs[i] = vec
			if shortest < 0 || len(vec) < shortest {
				shortest = len(vec)
			}
		}
		out := make(Vector, shortest)
		callArgs := make([]Value, len(vecs))
		for i := 0; i < shortest; i++ {
			for j, vec := range vecs {
				callArgs[j] = vec[i]
			}
			res, err := callFunction(args[0], callArgs, env)
			if err != nil {
				return nil, err
			}
			out[i] = res
		}
		return out, nil
	}))
	env.SetBuiltin("vector-for-each", "call a function elementwise over vectors for effect: (vector-for-each fn vec...)", BuiltinFunc(func(args []Value, env *Environment) (Value, error) {
		if len(args) < 2 {
			return nil, errors.New("vector-for-each expects at least 2 arguments: (vector-for-each fn vec...)")
		}
		vecs := make([]Vector, len(args)-1)
		shortest := -1
		for i, arg := range args[1:] {
			vec, err := vectorArg("vector-for-each", arg)
			if err != nil {
				return nil, err
			}
			vecs[i] = vec
			if shortest < 0 || len(vec) < shortest {
				shortest = len(vec)
			}
		}
		callArgs := make([]Value, len(vecs))
		for i := 0; i < shortest; i++ {
			for j, vec := range vecs {
				callArgs[j] = vec[i]
			}
			if _, err := callFunction(args[0], callArgs, env); err != nil {
				return nil, err
			}
		}
		return nil, nil
	}))
}

func bytevectorArg(name string, v Value) (Bytevector, error) {
	bv, ok := v.(Bytevector)
	if !ok {
		return nil, fmt.Errorf("%s expects a bytevector, got %s", name, PrintValue(v))
	}
	return bv, nil
}

func byteArg(name string, v Value) (byte, error) {
	n, ok := numIndex(v)
	if !ok || n < 0 || n > 255 {
		return 0, fmt.Errorf("%s expects a byte (0-255), got %s", name, PrintValue(v))
	}
	return byte(n), nil
}

func bytevectorBuiltins(env *Environment) {
	env.SetBuiltin("bytevector?", "true when the value is a bytevector", BuiltinFunc(func(args []Value, env *Environment) (Value, error) {
		if len(args) != 1 {
			return nil, errors.New("bytevector? expects 1 argument")
		}
		_, ok := args[0].(Bytevector)
		return ok, nil
	}))
	env.SetBuiltin("make-bytevector", "a fresh bytevector: (make-bytevector k [fill])", BuiltinFunc(func(args []Value, env *Environment) (Value, error) {
		if len(args) < 1 || len(args) > 2 {
			return nil, errors.New("make-bytevector expects 1 or 2 arguments: (make-bytevector k [fill])")
		}
		k, ok := numIndex(args[0])
		if !ok || k < 0 {
			return nil, errors.New("make-bytevector expects a non-negative integer length")
		}
		var fill byte
		if len(args) == 2 {
			b, err := byteArg("make-bytevector", args[1])
			if err != nil {
				return nil, err
			}
			fill = b
		}
		bv := make(Bytevector, k)
		for i := range bv {
			bv[i] = fill
		}
		return bv, nil
	}))
	env.SetBuiltin("bytevector", "a fresh bytevector of its byte arguments", BuiltinFunc(func(args []Value, env *Environment) (Value, error) {
		bv := make(Bytevector, len(args))
		for i, arg := range args {
			b, err := byteArg("bytevector", arg)
			if err != nil {
				return nil, err
			}
			bv[i] = b
		}
		return bv, nil
	}))
	env.SetBuiltin("bytevector-length", "the number of bytes in a bytevector", BuiltinFunc(func(args []Value, env *Environment) (Value, error) {
		if len(args) != 1 {
			return nil, errors.New("bytevector-length expects 1 argument")
		}
		bv, err := bytevectorArg("bytevector-length", args[0])
		if err != nil {
			return nil, err
		}
		return Integer(len(bv)), nil
	}))
	env.SetBuiltin("bytevector-u8-ref", "the byte at an index: (bytevector-u8-ref bv k)", BuiltinFunc(func(args []Value, env *Environment) (Value, error) {
		if len(args) != 2 {
			return nil, errors.New("bytevector-u8-ref expects 2 arguments: (bytevector-u8-ref bv k)")
		}
		bv, err := bytevectorArg("bytevector-u8-ref", args[0])
		if err != nil {
			return nil, err
		}
		k, ok := numIndex(args[1])
		if !ok {
			return nil, errors.New("bytevector-u8-ref expects an integer index")
		}
		if k < 0 || k >= len(bv) {
			return nil, fmt.Errorf("bytevector-u8-ref: index %d out of bounds for length %d", k, len(bv))
		}
		return Integer(bv[k]), nil
	}))
	env.SetBuiltin("bytevector-u8-set!", "replace the byte at an index in place", BuiltinFunc(func(args []Value, env *Environment) (Value, error) {
		if len(args) != 3 {
			return nil, errors.New("bytevector-u8-set! expects 3 arguments: (bytevector-u8-set! bv k byte)")
		}
		bv, err := bytevectorArg("bytevector-u8-set!", args[0])
		if err != nil {
			return nil, err
		}
		k, ok := numIndex(args[1])
		if !ok {
			return nil, errors.New("bytevector-u8-set! expects an integer index")
		}
		if k < 0 || k >= len(bv) {
			return nil, fmt.Errorf("bytevector-u8-set!: index %d out of bounds for length %d", k, len(bv))
		}
		b, err := byteArg("bytevector-u8-set!", args[2])
		if err != nil {
			return nil, err
		}
		bv[k] = b
		return nil, nil
	}))
	env.SetBuiltin("bytevector-copy", "a fresh copy of a bytevector: (bytevector-copy bv [start [end]])", BuiltinFunc(func(args []Value, env *Environment) (Value, error) {
		if len(args) < 1 || len(args) > 3 {
			return nil, errors.New("bytevector-copy expects 1 to 3 arguments")
		}
		bv, err := bytevectorArg("bytevector-copy", args[0])
		if err != nil {
			return nil, err
		}
		start, end, err := startEnd("bytevector-copy", len(bv), args[1:])
		if err != nil {
			return nil, err
		}
		out := make(Bytevector, end-start)
		copy(out, bv[start:end])
		return out, nil
	}))
	env.SetBuiltin("bytevector-copy!", "copy a range between bytevectors in place: (bytevector-copy! to at from [start [end]])", BuiltinFunc(func(args []Value, env *Environment) (Value, error) {
		if len(args) < 3 || len(args) > 5 {
			return nil, errors.New("bytevector-copy! expects 3 to 5 arguments: (bytevector-copy! to at from [start [end]])")
		}
		to, err := bytevectorArg("bytevector-copy!", args[0])
		if err != nil {
			return nil, err
		}
		at, ok := numIndex(args[1])
		if !ok || at < 0 {
			return nil, errors.New("bytevector-copy! expects a non-negative integer at")
		}
		from, err := bytevectorArg("bytevector-copy!", args[2])
		if err != nil {
			return nil, err
		}
		start, end, err := startEnd("bytevector-copy!", len(from), args[3:])
		if err != nil {
			return nil, err
		}
		if at+(end-start) > len(to) {
			return nil, errors.New("bytevector-copy!: destination range out of bounds")
		}
		copy(to[at:], from[start:end]) // copy is overlap-safe
		return nil, nil
	}))
	env.SetBuiltin("bytevector-append", "concatenate bytevectors into a fresh bytevector", BuiltinFunc(func(args []Value, env *Environment) (Value, error) {
		total := 0
		for _, arg := range args {
			bv, err := bytevectorArg("bytevector-append", arg)
			if err != nil {
				return nil, err
			}
			total += len(bv)
		}
		out := make(Bytevector, 0, total)
		for _, arg := range args {
			out = append(out, arg.(Bytevector)...)
		}
		return out, nil
	}))
	env.SetBuiltin("utf8->string", "decode a bytevector's UTF-8 as a string: (utf8->string bv [start [end]])", BuiltinFunc(func(args []Value, env *Environment) (Value, error) {
		if len(args) < 1 || len(args) > 3 {
			return nil, errors.New("utf8->string expects 1 to 3 arguments")
		}
		bv, err := bytevectorArg("utf8->string", args[0])
		if err != nil {
			return nil, err
		}
		start, end, err := startEnd("utf8->string", len(bv), args[1:])
		if err != nil {
			return nil, err
		}
		b := bv[start:end]
		if !utf8.Valid(b) {
			return nil, errors.New("utf8->string: bytevector range is not valid UTF-8")
		}
		return String(b), nil
	}))
	env.SetBuiltin("string->utf8", "a string's UTF-8 bytes as a bytevector: (string->utf8 s [start [end]])", BuiltinFunc(func(args []Value, env *Environment) (Value, error) {
		if len(args) < 1 || len(args) > 3 {
			return nil, errors.New("string->utf8 expects 1 to 3 arguments")
		}
		s, ok := stringText(args[0])
		if !ok {
			return nil, errors.New("string->utf8 expects a string")
		}
		runes := []rune(s)
		start, end, err := startEnd("string->utf8", len(runes), args[1:])
		if err != nil {
			return nil, err
		}
		return Bytevector(string(runes[start:end])), nil
	}))
}
