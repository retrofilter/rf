package eval

import (
	"math"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestMathBuiltinsComprehensive(t *testing.T) {
	env := NewEnvironment(nil)
	addBuiltins(env) // Add basic arithmetic operations
	mathBuiltins(env)

	tests := []struct {
		desc      string
		expr      string
		expectErr bool
		check     func(Value) // custom check for the result
	}{
		// Constants
		{"pi constant", `pi`, false, func(result Value) {
			num, ok := numFloat(result)
			require.True(t, ok, "expected numeric result")
			require.InDelta(t, math.Pi, num, 1e-10, "pi should equal math.Pi")
		}},
		{"e constant", `e`, false, func(result Value) {
			num, ok := numFloat(result)
			require.True(t, ok, "expected numeric result")
			require.InDelta(t, math.E, num, 1e-10, "e should equal math.E")
		}},

		// cos function
		{"cos 0", `(cos 0)`, false, func(result Value) {
			num, ok := numFloat(result)
			require.True(t, ok, "expected numeric result")
			require.InDelta(t, 1.0, num, 1e-10, "cos(0) should be 1")
		}},
		{"cos pi/2", `(cos 1.5707963267948966)`, false, func(result Value) {
			num, ok := numFloat(result)
			require.True(t, ok, "expected numeric result")
			require.InDelta(t, 0.0, num, 1e-10, "cos(π/2) should be 0")
		}},
		{"cos pi", `(cos 3.141592653589793)`, false, func(result Value) {
			num, ok := numFloat(result)
			require.True(t, ok, "expected numeric result")
			require.InDelta(t, -1.0, num, 1e-10, "cos(π) should be -1")
		}},
		{"cos wrong arg count", `(cos)`, true, nil},
		{"cos too many args", `(cos 1 2)`, true, nil},
		{"cos wrong type", `(cos "hello")`, true, nil},

		// sin function
		{"sin 0", `(sin 0)`, false, func(result Value) {
			num, ok := numFloat(result)
			require.True(t, ok, "expected numeric result")
			require.InDelta(t, 0.0, num, 1e-10, "sin(0) should be 0")
		}},
		{"sin pi/2", `(sin 1.5707963267948966)`, false, func(result Value) {
			num, ok := numFloat(result)
			require.True(t, ok, "expected numeric result")
			require.InDelta(t, 1.0, num, 1e-10, "sin(π/2) should be 1")
		}},
		{"sin pi", `(sin 3.141592653589793)`, false, func(result Value) {
			num, ok := numFloat(result)
			require.True(t, ok, "expected numeric result")
			require.InDelta(t, 0.0, num, 1e-10, "sin(π) should be 0")
		}},
		{"sin wrong arg count", `(sin)`, true, nil},
		{"sin too many args", `(sin 1 2)`, true, nil},
		{"sin wrong type", `(sin "hello")`, true, nil},

		// exp function
		{"exp 0", `(exp 0)`, false, func(result Value) {
			num, ok := numFloat(result)
			require.True(t, ok, "expected numeric result")
			require.InDelta(t, 1.0, num, 1e-10, "exp(0) should be 1")
		}},
		{"exp 1", `(exp 1)`, false, func(result Value) {
			num, ok := numFloat(result)
			require.True(t, ok, "expected numeric result")
			require.InDelta(t, math.E, num, 1e-10, "exp(1) should be e")
		}},
		{"exp 2", `(exp 2)`, false, func(result Value) {
			num, ok := numFloat(result)
			require.True(t, ok, "expected numeric result")
			require.InDelta(t, math.E*math.E, num, 1e-10, "exp(2) should be e²")
		}},
		{"exp wrong arg count", `(exp)`, true, nil},
		{"exp too many args", `(exp 1 2)`, true, nil},
		{"exp wrong type", `(exp "hello")`, true, nil},

		// sqrt function
		{"sqrt 0", `(sqrt 0)`, false, func(result Value) {
			num, ok := numFloat(result)
			require.True(t, ok, "expected numeric result")
			require.InDelta(t, 0.0, num, 1e-10, "sqrt(0) should be 0")
		}},
		{"sqrt 1", `(sqrt 1)`, false, func(result Value) {
			num, ok := numFloat(result)
			require.True(t, ok, "expected numeric result")
			require.InDelta(t, 1.0, num, 1e-10, "sqrt(1) should be 1")
		}},
		{"sqrt 4", `(sqrt 4)`, false, func(result Value) {
			num, ok := numFloat(result)
			require.True(t, ok, "expected numeric result")
			require.InDelta(t, 2.0, num, 1e-10, "sqrt(4) should be 2")
		}},
		{"sqrt 9", `(sqrt 9)`, false, func(result Value) {
			num, ok := numFloat(result)
			require.True(t, ok, "expected numeric result")
			require.InDelta(t, 3.0, num, 1e-10, "sqrt(9) should be 3")
		}},
		{"sqrt wrong arg count", `(sqrt)`, true, nil},
		{"sqrt too many args", `(sqrt 4 9)`, true, nil},
		{"sqrt wrong type", `(sqrt "hello")`, true, nil},

		// sqr function
		{"sqr 0", `(sqr 0)`, false, func(result Value) {
			num, ok := numFloat(result)
			require.True(t, ok, "expected numeric result")
			require.InDelta(t, 0.0, num, 1e-10, "sqr(0) should be 0")
		}},
		{"sqr 1", `(sqr 1)`, false, func(result Value) {
			num, ok := numFloat(result)
			require.True(t, ok, "expected numeric result")
			require.InDelta(t, 1.0, num, 1e-10, "sqr(1) should be 1")
		}},
		{"sqr 3", `(sqr 3)`, false, func(result Value) {
			num, ok := numFloat(result)
			require.True(t, ok, "expected numeric result")
			require.InDelta(t, 9.0, num, 1e-10, "sqr(3) should be 9")
		}},
		{"sqr -2", `(sqr -2)`, false, func(result Value) {
			num, ok := numFloat(result)
			require.True(t, ok, "expected numeric result")
			require.InDelta(t, 4.0, num, 1e-10, "sqr(-2) should be 4")
		}},
		{"sqr wrong arg count", `(sqr)`, true, nil},
		{"sqr too many args", `(sqr 2 3)`, true, nil},
		{"sqr wrong type", `(sqr "hello")`, true, nil},

		// log function
		{"log 1", `(log 1)`, false, func(result Value) {
			num, ok := numFloat(result)
			require.True(t, ok, "expected numeric result")
			require.InDelta(t, 0.0, num, 1e-10, "log(1) should be 0")
		}},
		{"log e", `(log 2.718281828459045)`, false, func(result Value) {
			num, ok := numFloat(result)
			require.True(t, ok, "expected numeric result")
			require.InDelta(t, 1.0, num, 1e-10, "log(e) should be 1")
		}},
		{"log wrong arg count", `(log)`, true, nil},
		{"log with base", `(log 100 10)`, false, func(result Value) {
			num, ok := numFloat(result)
			require.True(t, ok, "expected numeric result")
			require.InDelta(t, 2.0, num, 1e-10, "log base 10 of 100 should be 2")
		}},
		{"log too many args", `(log 2 3 4)`, true, nil},
		{"log wrong type", `(log "hello")`, true, nil},

		// round function
		{"round 1.4", `(round 1.4)`, false, func(result Value) {
			num, ok := numFloat(result)
			require.True(t, ok, "expected numeric result")
			require.Equal(t, 1.0, num, "round(1.4) should be 1")
		}},
		{"round 1.5", `(round 1.5)`, false, func(result Value) {
			num, ok := numFloat(result)
			require.True(t, ok, "expected numeric result")
			require.Equal(t, 2.0, num, "round(1.5) should be 2")
		}},
		{"round -1.4", `(round -1.4)`, false, func(result Value) {
			num, ok := numFloat(result)
			require.True(t, ok, "expected numeric result")
			require.Equal(t, -1.0, num, "round(-1.4) should be -1")
		}},
		{"round wrong arg count", `(round)`, true, nil},
		{"round too many args", `(round 1.5 2.5)`, true, nil},
		{"round wrong type", `(round "hello")`, true, nil},

		// floor function
		{"floor 1.9", `(floor 1.9)`, false, func(result Value) {
			num, ok := numFloat(result)
			require.True(t, ok, "expected numeric result")
			require.Equal(t, 1.0, num, "floor(1.9) should be 1")
		}},
		{"floor -1.9", `(floor -1.9)`, false, func(result Value) {
			num, ok := numFloat(result)
			require.True(t, ok, "expected numeric result")
			require.Equal(t, -2.0, num, "floor(-1.9) should be -2")
		}},
		{"floor wrong arg count", `(floor)`, true, nil},
		{"floor too many args", `(floor 1.5 2.5)`, true, nil},
		{"floor wrong type", `(floor "hello")`, true, nil},

		// expt function (exponentiation)
		{"expt 2^0", `(expt 2 0)`, false, func(result Value) {
			num, ok := numFloat(result)
			require.True(t, ok, "expected numeric result")
			require.InDelta(t, 1.0, num, 1e-10, "2^0 should be 1")
		}},
		{"expt 2^1", `(expt 2 1)`, false, func(result Value) {
			num, ok := numFloat(result)
			require.True(t, ok, "expected numeric result")
			require.InDelta(t, 2.0, num, 1e-10, "2^1 should be 2")
		}},
		{"expt 3^2", `(expt 3 2)`, false, func(result Value) {
			num, ok := numFloat(result)
			require.True(t, ok, "expected numeric result")
			require.InDelta(t, 9.0, num, 1e-10, "3^2 should be 9")
		}},
		{"expt 2^0.5", `(expt 2 0.5)`, false, func(result Value) {
			num, ok := numFloat(result)
			require.True(t, ok, "expected numeric result")
			require.InDelta(t, math.Sqrt(2), num, 1e-10, "2^0.5 should be sqrt(2)")
		}},
		{"expt wrong arg count", `(expt 2)`, true, nil},
		{"expt too many args", `(expt 2 3 4)`, true, nil},
		{"expt wrong type first", `(expt "hello" 2)`, true, nil},
		{"expt wrong type second", `(expt 2 "hello")`, true, nil},
	}

	for _, tc := range tests {
		t.Run(tc.desc, func(t *testing.T) {
			ast, err := Parse(tc.expr)
			require.NoError(t, err, "failed to parse expression: %s", tc.expr)

			eval := NewEvaluator()
			got, err := eval.Eval(ast, env)

			if tc.expectErr {
				require.Error(t, err, "expected error for expression: %s", tc.expr)
				return
			}

			require.NoError(t, err, "unexpected error for expression: %s", tc.expr)
			if tc.check != nil {
				tc.check(got)
			}
		})
	}
}

func TestMathBuiltinsIntegration(t *testing.T) {
	env := NewEnvironment(nil)
	addBuiltins(env) // Add basic arithmetic operations
	mathBuiltins(env)
	eval := NewEvaluator()

	// Test complex expressions using multiple builtins
	tests := []struct {
		desc  string
		expr  string
		check func(Value)
	}{
		{"sin squared plus cos squared", `(+ (sqr (sin 0)) (sqr (cos 0)))`, func(result Value) {
			num, ok := numFloat(result)
			require.True(t, ok, "expected numeric result")
			require.InDelta(t, 1.0, num, 1e-10, "sin²(0) + cos²(0) should be 1")
		}},
		{"sin squared plus cos squared pi/4", `(+ (sqr (sin 0.7853981633974483)) (sqr (cos 0.7853981633974483)))`, func(result Value) {
			num, ok := numFloat(result)
			require.True(t, ok, "expected numeric result")
			require.InDelta(t, 1.0, num, 1e-10, "sin²(π/4) + cos²(π/4) should be 1")
		}},
		{"e to the power of log", `(expt e (log 2))`, func(result Value) {
			num, ok := numFloat(result)
			require.True(t, ok, "expected numeric result")
			require.InDelta(t, 2.0, num, 1e-10, "e^(log(2)) should be 2")
		}},
		{"sqrt of square", `(sqrt (sqr 5))`, func(result Value) {
			num, ok := numFloat(result)
			require.True(t, ok, "expected numeric result")
			require.InDelta(t, 5.0, num, 1e-10, "sqrt(5²) should be 5")
		}},
		{"round of sqrt", `(round (sqrt 2))`, func(result Value) {
			num, ok := numFloat(result)
			require.True(t, ok, "expected numeric result")
			require.Equal(t, 1.0, num, "round(sqrt(2)) should be 1")
		}},
		{"floor of exp", `(floor (exp 1))`, func(result Value) {
			num, ok := numFloat(result)
			require.True(t, ok, "expected numeric result")
			require.Equal(t, 2.0, num, "floor(e) should be 2")
		}},
	}

	for _, tc := range tests {
		t.Run(tc.desc, func(t *testing.T) {
			ast, err := Parse(tc.expr)
			require.NoError(t, err, "failed to parse expression: %s", tc.expr)

			got, err := eval.Eval(ast, env)
			require.NoError(t, err, "unexpected error for expression: %s", tc.expr)

			if tc.check != nil {
				tc.check(got)
			}
		})
	}
}

func TestCeilingTruncateRandom(t *testing.T) {
	ev := NewEvaluator()
	env := ev.globalEnv

	for expr, want := range map[string]Value{
		"(ceiling 1.2)":   Number(2),
		"(ceiling -1.2)":  Number(-1),
		"(ceiling 3)":     Integer(3),
		"(truncate 1.9)":  Number(1),
		"(truncate -1.9)": Number(-1),
	} {
		got, err := evalExpr(expr, ev, env)
		if err != nil || got != want {
			t.Errorf("%s = %v, %v; want %v", expr, got, err, want)
		}
	}
	for _, expr := range []string{
		"(ceiling \"x\")", "(truncate)", "(random 0)", "(random -1)",
		"(random 1.5)", "(random 1 2)", "(random \"x\")",
	} {
		if _, err := evalExpr(expr, ev, env); err == nil {
			t.Errorf("%s should error", expr)
		}
	}

	// (random) is a float in [0,1); (random n) an integer in [0,n)
	for i := 0; i < 50; i++ {
		got, err := evalExpr("(random)", ev, env)
		if err != nil {
			t.Fatalf("(random): %v", err)
		}
		if n := got.(Number); n < 0 || n >= 1 {
			t.Fatalf("(random) = %v, want [0,1)", n)
		}
		got, err = evalExpr("(random 10)", ev, env)
		if err != nil {
			t.Fatalf("(random 10): %v", err)
		}
		n, ok := got.(Integer)
		if !ok || n < 0 || n >= 10 {
			t.Fatalf("(random 10) = %v, want exact integer in [0,10)", got)
		}
	}
}

func TestPhase5Numbers(t *testing.T) {
	runStdlibTests(t, []struct {
		expr      string
		expect    Value
		shouldErr bool
	}{
		{"(square 12)", Integer(144), false},
		{"(sqr 12)", Integer(144), false},
		{"(floor/ -5 2)", &MultipleValues{Vals: []Value{Integer(-3), Integer(1)}}, false},
		{"(truncate/ -5 2)", &MultipleValues{Vals: []Value{Integer(-2), Integer(-1)}}, false},
		// Exactness contagion: one inexact operand, inexact results.
		{"(truncate/ -5.0 -2)", &MultipleValues{Vals: []Value{Number(2), Number(-1)}}, false},
		{"(floor-quotient -5 2)", Integer(-3), false},
		{"(floor-remainder -5 2)", Integer(1), false},
		{"(truncate-quotient -5 2)", Integer(-2), false},
		{"(truncate-remainder -5 2)", Integer(-1), false},
		{"(floor/ 5 0)", nil, true},
		{"(gcd 32 -36)", Integer(4), false},
		{"(gcd)", Integer(0), false},
		{"(lcm 32 -36)", Integer(288), false},
		{"(lcm 32.0 -36)", Number(288), false},
		{"(lcm)", Integer(1), false},
		{"(lcm 4 0)", Integer(0), false},
		{"(gcd 7 0.5)", nil, true},
		{"(exact-integer-sqrt 5)", &MultipleValues{Vals: []Value{Integer(2), Integer(1)}}, false},
		{"(exact-integer-sqrt 0)", &MultipleValues{Vals: []Value{Integer(0), Integer(0)}}, false},
		{"(call-with-values (lambda () (exact-integer-sqrt (expt 2 62))) (lambda (s r) (list s r)))",
			[]Value{Integer(2147483648), Integer(0)}, false},
		{"(exact-integer-sqrt 5.0)", nil, true},
		{"(exact-integer-sqrt -1)", nil, true},
		{"(numerator 7)", Integer(7), false},
		{"(denominator 7)", Integer(1), false},
		{"(numerator 5.5)", Number(11), false},
		{"(denominator 5.5)", Number(2), false},
		{"(denominator 5.0)", Number(1), false},
		{"(rationalize 3.1 1)", Number(3), false},
		{"(rationalize 5 1)", Integer(4), false},
		{"(rationalize 0.5 0.25)", Number(0.5), false},
		{"(atan 1.0 0.0)", Number(1.5707963267948966), false},
		{"(log 4096 2)", Number(12), false},
		{"(finite? 3)", true, false},
		{"(finite? +inf.0)", false, false},
		{"(infinite? -inf.0)", true, false},
		{"(nan? +nan.0)", true, false},
		{"(nan? 32)", false, false},
		{"(nan? 'x)", nil, true},
		// features: a list of symbols including r7rs.
		{"(if (memq 'r7rs (features)) #t #f)", true, false},
		{"(features 1)", nil, true},
	})
}
