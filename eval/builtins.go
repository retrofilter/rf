package eval

import (
	"errors"
	"fmt"
	"math"
	"math/big"
	"math/rand/v2"
)

func float1(name string, f func(float64) float64) BuiltinFunc {
	return func(args []Value, env *Environment) (Value, error) {
		if len(args) != 1 {
			return nil, errors.New(name + " expects 1 argument")
		}
		val, ok := numFloat(args[0])
		if !ok {
			return nil, errors.New(name + " expects a number")
		}
		return Number(f(val)), nil
	}
}

func intRound(name string, f func(float64) float64) BuiltinFunc {
	return func(args []Value, env *Environment) (Value, error) {
		if len(args) != 1 {
			return nil, errors.New(name + " expects 1 argument")
		}
		switch n := args[0].(type) {
		case Integer:
			return n, nil
		case Number:
			return Number(f(float64(n))), nil
		}
		return nil, errors.New(name + " expects a number")
	}
}

func mathBuiltins(env *Environment) {
	env.Set("pi", Number(math.Pi))
	env.Set("e", Number(math.E))
	env.SetBuiltin("cos", "cosine of an angle in radians", float1("cos", math.Cos))
	env.SetBuiltin("sin", "sine of an angle in radians", float1("sin", math.Sin))
	env.SetBuiltin("exp", "e raised to a power", float1("exp", math.Exp))
	env.SetBuiltin("sqrt", "the square root of a number; exact for exact perfect squares", BuiltinFunc(func(args []Value, env *Environment) (Value, error) {
		if len(args) != 1 {
			return nil, errors.New("sqrt expects 1 argument")
		}
		if n, ok := args[0].(Integer); ok && n >= 0 {
			r := Integer(math.Sqrt(float64(n)))
			for _, c := range []Integer{r - 1, r, r + 1} {
				if c >= 0 && c*c == n {
					return c, nil
				}
			}
		}
		val, ok := numFloat(args[0])
		if !ok {
			return nil, errors.New("sqrt expects a number")
		}
		return Number(math.Sqrt(val)), nil
	}))
	// square is the R7RS name; sqr stays as the historical rf alias.
	square := BuiltinFunc(func(args []Value, env *Environment) (Value, error) {
		if len(args) != 1 {
			return nil, errors.New("square expects 1 argument")
		}
		res, ok := numMul2(args[0], args[0])
		if !ok {
			return nil, errors.New("square expects a number")
		}
		return res, nil
	})
	env.SetBuiltin("square", "a number squared", square)
	env.SetBuiltin("sqr", "a number squared (alias of square)", square)
	env.SetBuiltin("tan", "tangent of an angle in radians", float1("tan", math.Tan))
	env.SetBuiltin("asin", "arcsine, in radians", float1("asin", math.Asin))
	env.SetBuiltin("acos", "arccosine, in radians", float1("acos", math.Acos))
	env.SetBuiltin("atan", "arctangent: (atan y) or (atan y x)", BuiltinFunc(func(args []Value, env *Environment) (Value, error) {
		if len(args) < 1 || len(args) > 2 {
			return nil, errors.New("atan expects 1 or 2 arguments: (atan y [x])")
		}
		y, ok := numFloat(args[0])
		if !ok {
			return nil, errors.New("atan expects numbers")
		}
		if len(args) == 1 {
			return Number(math.Atan(y)), nil
		}
		x, ok := numFloat(args[1])
		if !ok {
			return nil, errors.New("atan expects numbers")
		}
		return Number(math.Atan2(y, x)), nil
	}))
	env.SetBuiltin("log", "natural logarithm; (log x base) uses the given base", BuiltinFunc(func(args []Value, env *Environment) (Value, error) {
		if len(args) < 1 || len(args) > 2 {
			return nil, errors.New("log expects 1 or 2 arguments: (log x [base])")
		}
		x, ok := numFloat(args[0])
		if !ok {
			return nil, errors.New("log expects a number")
		}
		if len(args) == 1 {
			return Number(math.Log(x)), nil
		}
		base, ok := numFloat(args[1])
		if !ok {
			return nil, errors.New("log expects a number as base")
		}
		return Number(math.Log(x) / math.Log(base)), nil
	}))
	// finite?/infinite?/nan? (§6.2.6): exact integers are always finite.
	env.SetBuiltin("finite?", "whether a number is neither infinite nor NaN", BuiltinFunc(func(args []Value, env *Environment) (Value, error) {
		if len(args) != 1 {
			return nil, errors.New("finite? expects 1 argument")
		}
		f, ok := numFloat(args[0])
		if !ok {
			return nil, errors.New("finite? expects a number")
		}
		return !math.IsInf(f, 0) && !math.IsNaN(f), nil
	}))
	env.SetBuiltin("infinite?", "whether a number is +inf.0 or -inf.0", BuiltinFunc(func(args []Value, env *Environment) (Value, error) {
		if len(args) != 1 {
			return nil, errors.New("infinite? expects 1 argument")
		}
		f, ok := numFloat(args[0])
		if !ok {
			return nil, errors.New("infinite? expects a number")
		}
		return math.IsInf(f, 0), nil
	}))
	env.SetBuiltin("nan?", "whether a number is +nan.0", BuiltinFunc(func(args []Value, env *Environment) (Value, error) {
		if len(args) != 1 {
			return nil, errors.New("nan? expects 1 argument")
		}
		f, ok := numFloat(args[0])
		if !ok {
			return nil, errors.New("nan? expects a number")
		}
		return math.IsNaN(f), nil
	}))
	env.SetBuiltin("round", "round a number to the nearest integer (ties to even)", intRound("round", math.RoundToEven))
	env.SetBuiltin("floor", "round a number down to an integer", intRound("floor", math.Floor))
	env.SetBuiltin("ceiling", "round a number up to an integer", intRound("ceiling", math.Ceil))
	env.SetBuiltin("truncate", "drop a number's fractional part, toward zero", intRound("truncate", math.Trunc))
	env.SetBuiltin("random", "(random) a float in [0,1); (random n) an integer in [0,n)", BuiltinFunc(func(args []Value, env *Environment) (Value, error) {
		if len(args) > 1 {
			return nil, errors.New("random expects 0 or 1 arguments: (random [n])")
		}
		if len(args) == 0 {
			return Number(rand.Float64()), nil
		}
		n, ok := numIndex(args[0])
		if !ok || n < 1 {
			return nil, errors.New("random expects a positive integer")
		}
		return Integer(rand.Int64N(int64(n))), nil
	}))
	env.SetBuiltin("expt", "raise a number to a power: (expt base exponent)", BuiltinFunc(func(args []Value, env *Environment) (Value, error) {
		if len(args) != 2 {
			return nil, errors.New("expt expects 2 arguments")
		}
		if base, ok := args[0].(Integer); ok {
			if exp, ok := args[1].(Integer); ok && exp >= 0 {
				var acc Value = Integer(1)
				var sq Value = base
				e := int64(exp)
				exactOK := true
				for e > 0 && exactOK {
					if e&1 == 1 {
						next, _ := numMul2(acc, sq)
						acc = next
					}
					e >>= 1
					if e > 0 {
						next, _ := numMul2(sq, sq)
						sq = next
					}
					_, exactOK = acc.(Integer)
					if _, sqExact := sq.(Integer); e > 0 && !sqExact {
						exactOK = false
					}
				}
				if exactOK {
					return acc, nil
				}
			}
		}
		val, ok := numFloat(args[0])
		if !ok {
			return nil, errors.New("expt expects 2 numbers")
		}
		exponent, ok := numFloat(args[1])
		if !ok {
			return nil, errors.New("expt expects 2 numbers")
		}
		return Number(math.Pow(val, exponent)), nil
	}))

	intDiv2 := func(name string, both func(a, b int64) (int64, int64)) func(args []Value) (int64, int64, bool, error) {
		return func(args []Value) (int64, int64, bool, error) {
			if len(args) != 2 {
				return 0, 0, false, fmt.Errorf("%s expects 2 arguments", name)
			}
			a, aExact, ok1 := numInt64(args[0])
			b, bExact, ok2 := numInt64(args[1])
			if !ok1 || !ok2 {
				return 0, 0, false, fmt.Errorf("%s expects integers", name)
			}
			if b == 0 {
				return 0, 0, false, fmt.Errorf("%s: division by zero", name)
			}
			q, r := both(a, b)
			return q, r, aExact && bExact, nil
		}
	}
	exactness := func(v int64, exact bool) Value {
		if exact {
			return boxInt(Integer(v))
		}
		return Number(float64(v))
	}
	floorDiv := func(a, b int64) (int64, int64) {
		q := a / b
		if a%b != 0 && (a < 0) != (b < 0) {
			q--
		}
		return q, a - q*b
	}
	truncDiv := func(a, b int64) (int64, int64) { return a / b, a % b }
	divValues := func(name string, both func(a, b int64) (int64, int64)) BuiltinFunc {
		div := intDiv2(name, both)
		return func(args []Value, env *Environment) (Value, error) {
			q, r, exact, err := div(args)
			if err != nil {
				return nil, err
			}
			return &MultipleValues{Vals: []Value{exactness(q, exact), exactness(r, exact)}}, nil
		}
	}
	divPart := func(name string, both func(a, b int64) (int64, int64), wantRemainder bool) BuiltinFunc {
		div := intDiv2(name, both)
		return func(args []Value, env *Environment) (Value, error) {
			q, r, exact, err := div(args)
			if err != nil {
				return nil, err
			}
			if wantRemainder {
				return exactness(r, exact), nil
			}
			return exactness(q, exact), nil
		}
	}
	env.SetBuiltin("floor/", "floored quotient and remainder as two values", divValues("floor/", floorDiv))
	env.SetBuiltin("truncate/", "truncated quotient and remainder as two values", divValues("truncate/", truncDiv))
	env.SetBuiltin("floor-quotient", "floored integer division", divPart("floor-quotient", floorDiv, false))
	env.SetBuiltin("floor-remainder", "floored remainder (modulo)", divPart("floor-remainder", floorDiv, true))
	env.SetBuiltin("truncate-quotient", "truncated integer division (quotient)", divPart("truncate-quotient", truncDiv, false))
	env.SetBuiltin("truncate-remainder", "truncated remainder (remainder)", divPart("truncate-remainder", truncDiv, true))

	gcd2 := func(a, b int64) int64 {
		for b != 0 {
			a, b = b, a%b
		}
		if a < 0 {
			return -a
		}
		return a
	}
	env.SetBuiltin("gcd", "greatest common divisor of its arguments; (gcd) is 0", BuiltinFunc(func(args []Value, env *Environment) (Value, error) {
		var g int64
		exact := true
		for _, arg := range args {
			n, nExact, ok := numInt64(arg)
			if !ok {
				return nil, errors.New("gcd expects integers")
			}
			exact = exact && nExact
			g = gcd2(g, n)
		}
		return exactness(g, exact), nil
	}))
	env.SetBuiltin("lcm", "least common multiple of its arguments; (lcm) is 1", BuiltinFunc(func(args []Value, env *Environment) (Value, error) {
		var l int64 = 1
		lf := 1.0 // float shadow, used after an overflow promotion
		promoted := false
		exact := true
		for _, arg := range args {
			n, nExact, ok := numInt64(arg)
			if !ok {
				return nil, errors.New("lcm expects integers")
			}
			exact = exact && nExact
			if n < 0 {
				n = -n
			}
			if n == 0 {
				l, lf = 0, 0
				continue
			}
			if promoted {
				a, b := lf, float64(n)
				for b != 0 {
					a, b = b, math.Mod(a, b)
				}
				lf = lf / a * float64(n)
				continue
			}
			step := l / gcd2(l, n)
			if next := step * n; step != 0 && next/step != n {
				promoted = true
				lf = float64(step) * float64(n)
			} else {
				l, lf = next, float64(next)
			}
		}
		if promoted || !exact {
			return Number(lf), nil
		}
		return exactness(l, true), nil
	}))

	// maxExactSqrt is the largest s with s*s representable in int64.
	const maxExactSqrt = 3037000499
	env.SetBuiltin("exact-integer-sqrt", "two values s and r with s² + r = n and s² ≤ n < (s+1)²", BuiltinFunc(func(args []Value, env *Environment) (Value, error) {
		if len(args) != 1 {
			return nil, errors.New("exact-integer-sqrt expects 1 argument")
		}
		n, ok := args[0].(Integer)
		if !ok || n < 0 {
			return nil, errors.New("exact-integer-sqrt expects a non-negative exact integer")
		}
		s := int64(math.Sqrt(float64(n)))
		if s > maxExactSqrt {
			s = maxExactSqrt
		}
		for s > 0 && s*s > int64(n) {
			s--
		}
		for s < maxExactSqrt && (s+1)*(s+1) <= int64(n) {
			s++
		}
		return &MultipleValues{Vals: []Value{boxInt(Integer(s)), boxInt(Integer(int64(n) - s*s))}}, nil
	}))

	ratPart := func(name string, wantDenom bool) BuiltinFunc {
		return func(args []Value, env *Environment) (Value, error) {
			if len(args) != 1 {
				return nil, fmt.Errorf("%s expects 1 argument", name)
			}
			switch n := args[0].(type) {
			case Integer:
				if wantDenom {
					return Integer(1), nil
				}
				return n, nil
			case Number:
				f := float64(n)
				if math.IsInf(f, 0) || math.IsNaN(f) {
					return nil, fmt.Errorf("%s is undefined for %s", name, n.String())
				}
				r := new(big.Rat).SetFloat64(f)
				part := r.Num()
				if wantDenom {
					part = r.Denom()
				}
				out, _ := new(big.Float).SetInt(part).Float64()
				return Number(out), nil
			}
			return nil, fmt.Errorf("%s expects a number", name)
		}
	}
	env.SetBuiltin("numerator", "the numerator of a number", ratPart("numerator", false))
	env.SetBuiltin("denominator", "the denominator of a number (1 for integers)", ratPart("denominator", true))

	env.SetBuiltin("rationalize", "the simplest rational within tolerance: (rationalize x y)", BuiltinFunc(func(args []Value, env *Environment) (Value, error) {
		if len(args) != 2 {
			return nil, errors.New("rationalize expects 2 arguments: (rationalize x y)")
		}
		x, ok1 := numFloat(args[0])
		y, ok2 := numFloat(args[1])
		if !ok1 || !ok2 {
			return nil, errors.New("rationalize expects numbers")
		}
		if math.IsNaN(x) || math.IsNaN(y) {
			return Number(math.NaN()), nil
		}
		res := simplestRational(x-math.Abs(y), x+math.Abs(y))
		_, xExact := args[0].(Integer)
		_, yExact := args[1].(Integer)
		if xExact && yExact {
			// Exact in, exact out — representable only when integral (D1).
			if res == math.Trunc(res) && !math.IsInf(res, 0) {
				return boxInt(Integer(int64(res))), nil
			}
			return nil, fmt.Errorf("rationalize: no exact representation for %g (no rationals)", res)
		}
		return Number(res), nil
	}))
}

func simplestRational(x, y float64) float64 {
	switch {
	case y < x:
		return simplestRational(y, x)
	case x == y:
		return x
	case x > 0:
		return simplestPositive(x, y)
	case y < 0:
		return -simplestPositive(-y, -x)
	default:
		return 0
	}
}

func simplestPositive(x, y float64) float64 {
	fx := math.Floor(x)
	fy := math.Floor(y)
	switch {
	case fx == x:
		return fx
	case fx == fy:
		return fx + 1/simplestPositive(1/(y-fy), 1/(x-fx))
	default:
		return fx + 1
	}
}
