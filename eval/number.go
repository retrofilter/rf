package eval

import (
	"errors"
	"fmt"
	"math"
	"strconv"
)

// Integer is the exact integer type of the D1 numeric split (SCHEME.md):
// int64 exact alongside Number's float64 inexact.
type Integer int64

func (i Integer) String() string { return strconv.FormatInt(int64(i), 10) }

var smallInts [2049]Value

func init() {
	for i := range smallInts {
		smallInts[i] = Integer(i - 1024)
	}
}

func boxInt(i Integer) Value {
	if i >= -1024 && i <= 1024 {
		return smallInts[i+1024]
	}
	return i
}

func isNumber(v Value) bool {
	switch v.(type) {
	case Integer, Number:
		return true
	}
	return false
}

func numFloat(v Value) (float64, bool) {
	switch n := v.(type) {
	case Integer:
		return float64(n), true
	case Number:
		return float64(n), true
	}
	return 0, false
}

func numIndex(v Value) (int, bool) {
	switch n := v.(type) {
	case Integer:
		return int(n), true
	case Number:
		if f := float64(n); f == math.Trunc(f) && !math.IsInf(f, 0) {
			return int(f), true
		}
	}
	return 0, false
}

func numInt64(v Value) (i int64, exact bool, ok bool) {
	switch n := v.(type) {
	case Integer:
		return int64(n), true, true
	case Number:
		if f := float64(n); f == math.Trunc(f) && !math.IsInf(f, 0) {
			return int64(f), false, true
		}
	}
	return 0, false, false
}

func toInexact(v Value) (Value, bool) {
	switch n := v.(type) {
	case Integer:
		return Number(float64(n)), true
	case Number:
		return n, true
	}
	return nil, false
}

func toExact(v Value) (Value, error) {
	switch n := v.(type) {
	case Integer:
		return n, nil
	case Number:
		f := float64(n)
		if f == math.Trunc(f) && f >= math.MinInt64 && f <= math.MaxInt64 {
			return Integer(int64(f)), nil
		}
		return nil, fmt.Errorf("exact: no exact representation for %s (no rationals)", n.String())
	}
	return nil, errors.New("exact expects a number")
}

func numAdd2(a, b Value) (Value, bool) {
	if x, ok := a.(Integer); ok {
		if y, ok := b.(Integer); ok {
			s := x + y
			if (x >= 0) == (y >= 0) && (s >= 0) != (x >= 0) {
				return Number(float64(x) + float64(y)), true
			}
			return boxInt(s), true
		}
	}
	xf, ok1 := numFloat(a)
	yf, ok2 := numFloat(b)
	if !ok1 || !ok2 {
		return nil, false
	}
	return Number(xf + yf), true
}

func numSub2(a, b Value) (Value, bool) {
	if x, ok := a.(Integer); ok {
		if y, ok := b.(Integer); ok {
			s := x - y
			if (x >= 0) != (y >= 0) && (s >= 0) != (x >= 0) {
				return Number(float64(x) - float64(y)), true
			}
			return boxInt(s), true
		}
	}
	xf, ok1 := numFloat(a)
	yf, ok2 := numFloat(b)
	if !ok1 || !ok2 {
		return nil, false
	}
	return Number(xf - yf), true
}

func numMul2(a, b Value) (Value, bool) {
	if x, ok := a.(Integer); ok {
		if y, ok := b.(Integer); ok {
			if x == 0 || y == 0 {
				return Integer(0), true
			}
			if x == math.MinInt64 || y == math.MinInt64 {
				return Number(float64(x) * float64(y)), true
			}
			p := x * y
			if p/y != x {
				return Number(float64(x) * float64(y)), true
			}
			return boxInt(p), true
		}
	}
	xf, ok1 := numFloat(a)
	yf, ok2 := numFloat(b)
	if !ok1 || !ok2 {
		return nil, false
	}
	return Number(xf * yf), true
}

func numDiv2(a, b Value) (Value, error) {
	if x, ok := a.(Integer); ok {
		if y, ok := b.(Integer); ok {
			if y == 0 {
				return nil, errors.New("division by zero")
			}
			if x%y == 0 && !(x == math.MinInt64 && y == -1) {
				return boxInt(x / y), nil
			}
			return Number(float64(x) / float64(y)), nil
		}
	}
	xf, ok1 := numFloat(a)
	yf, ok2 := numFloat(b)
	if !ok1 || !ok2 {
		return nil, errors.New("expects numbers")
	}
	if yf == 0 {
		if _, exactDivisor := b.(Integer); exactDivisor {
			return nil, errors.New("division by zero")
		}
		return Number(xf / yf), nil // IEEE: inf or nan
	}
	return Number(xf / yf), nil
}

func numEqual2(a, b Value) (bool, bool) {
	if x, ok := a.(Integer); ok {
		switch y := b.(type) {
		case Integer:
			return x == y, true
		case Number:
			return intFloatEqual(int64(x), float64(y)), true
		}
		return false, false
	}
	if x, ok := a.(Number); ok {
		if y, ok := b.(Integer); ok {
			return intFloatEqual(int64(y), float64(x)), true
		}
	}
	xf, ok1 := numFloat(a)
	yf, ok2 := numFloat(b)
	if !ok1 || !ok2 {
		return false, false
	}
	return xf == yf, true
}

func numOrder2(a, b Value) (cmp int, unordered bool, ok bool) {
	if x, xok := a.(Integer); xok {
		switch y := b.(type) {
		case Integer:
			switch {
			case x < y:
				return -1, false, true
			case x > y:
				return 1, false, true
			}
			return 0, false, true
		case Number:
			cmp, unordered = intFloatOrder(int64(x), float64(y))
			return cmp, unordered, true
		}
		return 0, false, false
	}
	x, xok := a.(Number)
	if !xok {
		return 0, false, false
	}
	switch y := b.(type) {
	case Integer:
		cmp, unordered = intFloatOrder(int64(y), float64(x))
		return -cmp, unordered, true
	case Number:
		fx, fy := float64(x), float64(y)
		if math.IsNaN(fx) || math.IsNaN(fy) {
			return 0, true, true
		}
		switch {
		case fx < fy:
			return -1, false, true
		case fx > fy:
			return 1, false, true
		}
		return 0, false, true
	}
	return 0, false, false
}

func intFloatOrder(x int64, y float64) (cmp int, unordered bool) {
	if math.IsNaN(y) {
		return 0, true
	}
	if y >= 9223372036854775808.0 {
		return -1, false
	}
	if y < -9223372036854775808.0 {
		return 1, false
	}
	t := math.Trunc(y)
	ti := int64(t) // exact: t is integral and inside int64's range
	if x != ti {
		if x < ti {
			return -1, false
		}
		return 1, false
	}
	switch {
	case y > t:
		return -1, false
	case y < t:
		return 1, false
	}
	return 0, false
}

func intFloatEqual(x int64, y float64) bool {
	if math.IsNaN(y) || math.IsInf(y, 0) || y != math.Trunc(y) {
		return false
	}
	if y < -9223372036854775808.0 || y >= 9223372036854775808.0 {
		return false
	}
	return int64(y) == x
}
