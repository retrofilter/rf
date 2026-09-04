package eval

import (
	"math"
	"os"
	"testing"
)

func evalExpr(expr string, eval *Evaluator, env *Environment) (Value, error) {
	ast, err := Parse(expr)
	if err != nil {
		return nil, err
	}
	v, err := eval.Eval(ast, env)
	if err != nil {
		return nil, err
	}
	return Materialize(v)
}

func TestSchemeBuiltins(t *testing.T) {
	eval := NewEvaluator()
	env := eval.globalEnv
	tests := []struct {
		expr      string
		expect    Value
		shouldErr bool
	}{
		{"(+ 1 2 3)", Integer(6), false},
		{"(- 10 3 2)", Integer(5), false},
		{"(* 2 3 4)", Integer(24), false},
		{"(/ 8 2)", Integer(4), false},
		{"(define x 42)", Symbol("x"), false},
		{"x", Integer(42), false},
		{"(if #t 1 2)", Integer(1), false},
		{"(if #f 1 2)", Integer(2), false},
		{"(list 1 2 3)", []Value{Integer(1), Integer(2), Integer(3)}, false},
		{"(car (list 1 2 3))", Integer(1), false},
		{"(cdr (list 1 2 3))", []Value{Integer(2), Integer(3)}, false},
		{"(cons 0 (list 1 2))", []Value{Integer(0), Integer(1), Integer(2)}, false},
		{"(= 1 1 1)", true, false},
		{"(= 1 2)", false, false},
		{"(= 1 \"1\")", nil, true},
		{"(= \"a\" \"a\")", nil, true},
		{"(equal? \"a\" \"a\")", true, false},
		{"(equal? 1 \"1\")", false, false},
		{"(< 1 \"a\")", nil, true},
		{"(> 3 2 \"a\")", nil, true},
		{"(<= 1 \"2\")", nil, true},
		{"(>= \"3\" 2)", nil, true},
		{"(< 3 1)", false, false},
		{"(> 1 3)", false, false},
		{"(< 1 2 3)", true, false},
		{"(<= 1 1 1)", true, false},
		{"(<= 1 1 3)", true, false},
		{"(>= 1 1 1)", true, false},
		{"(>= 3 1 1)", true, false},
		{"(> 3 2 1)", true, false},
		{"(not #f)", true, false},
		{"(not #t)", false, false},
		{"(assert #t)", true, false},
		{"(assert #f)", nil, true},
		{"(assert (= 1 2) \"fail msg\")", nil, true},
	}
	for _, tc := range tests {
		got, err := evalExpr(tc.expr, eval, env)
		if tc.shouldErr {
			if err == nil {
				t.Errorf("expected error for %q, got value %v", tc.expr, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("unexpected error for %q: %v", tc.expr, err)
			continue
		}
		// deepEqual treats pair chains and slice lists as the same list.
		if !deepEqual(got, tc.expect) {
			t.Errorf("for %q, got %v, want %v", tc.expr, PrintValue(got), PrintValue(tc.expect))
		}
	}
}

func TestNewSchemeBuiltins(t *testing.T) {
	eval := NewEvaluator()
	env := eval.globalEnv
	tests := []struct {
		expr      string
		expect    Value
		shouldErr bool
	}{
		// Type predicates
		{"(null? (list))", true, false},
		{"(null? (list 1))", false, false},
		{"(list? (list 1 2))", true, false},
		{"(list? 42)", false, false},
		{"(number? 42)", true, false},
		{"(symbol? 42)", false, false},
		{"(string? \"abc\")", true, false},
		{"(string? 42)", false, false},
		{"(boolean? #t)", true, false},
		{"(boolean? 42)", false, false},

		// Equality
		{"(eq? 1 1)", true, false},
		{"(eq? 1 2)", false, false},
		{"(equal? (list 1 2) (list 1 2))", true, false},
		{"(equal? (list 1 2) (list 2 1))", false, false},

		// List utilities
		{"(length (list 1 2 3))", Integer(3), false},
		{"(length (list))", Integer(0), false},
		{"(append (list 1 2) (list 3 4))", []Value{Integer(1), Integer(2), Integer(3), Integer(4)}, false},
		{"(append (list) (list 1))", []Value{Integer(1)}, false},
		// map: use not on booleans
		{"(map not (list #t #f))", []Value{false, true}, false},
		{"(list-ref (list 10 20 30) 1)", Integer(20), false},
		// filter: keep only true
		{"(filter not (list #f #t))", []Value{false}, false},
		// member returns the matching sublist (R7RS), else false
		{"(member 2 (list 1 2 3))", []Value{Integer(2), Integer(3)}, false},
		{"(member 4 (list 1 2 3))", false, false},

		// begin
		{"(begin 1 2 3)", Integer(3), false},
		{"(begin)", nil, false},
	}
	for _, tc := range tests {
		got, err := evalExpr(tc.expr, eval, env)
		if tc.shouldErr {
			if err == nil {
				t.Errorf("expected error for %q, got value %v", tc.expr, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("unexpected error for %q: %v", tc.expr, err)
			continue
		}
		// deepEqual treats pair chains and slice lists as the same list.
		if !deepEqual(got, tc.expect) {
			t.Errorf("for %q, got %v, want %v", tc.expr, PrintValue(got), PrintValue(tc.expect))
		}
	}
}

func TestPrint(t *testing.T) {
	eval := NewEvaluator()
	env := eval.globalEnv
	old := os.Stdout
	// Open /dev/null for writing
	devNull, _ := os.Open(os.DevNull)
	os.Stdout = devNull

	// Run your test code
	if _, err := evalExpr("(print 1 2 3)", eval, env); err != nil {
		t.Errorf("print errored: %v", err)
	}

	// Restore original stdout
	os.Stdout = old
}

func TestDefine(t *testing.T) {
	eval := NewEvaluator()
	env := eval.globalEnv
	tests := []struct {
		expr      string
		expect    Value
		shouldErr bool
	}{
		// Define a variable
		{"(define x 10)", Symbol("x"), false},
		{"x", Integer(10), false},

		// Define a function shorthand
		{"(define (square y) (* y y))", Symbol("square"), false},
		{"(square 5)", Integer(25), false},

		// Define a function with multiple args
		{"(define (sum a b) (+ a b))", Symbol("sum"), false},
		{"(sum 3 4)", Integer(7), false},

		// Redefine a variable - should be allowed
		{"(define x 20)", Symbol("x"), false},
		{"x", Integer(20), false},

		// Invalid definitions
		{"(define)", nil, true},
		{"(define x)", nil, true},
		{"(define (f))", nil, true}, // empty body
	}

	for _, tc := range tests {
		got, err := evalExpr(tc.expr, eval, env)
		if tc.shouldErr {
			if err == nil {
				t.Errorf("expected error for %q, got value %v", tc.expr, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("unexpected error for %q: %v", tc.expr, err)
			continue
		}
		if !deepEqual(got, tc.expect) {
			t.Errorf("for %q, got %v, want %v", tc.expr, got, tc.expect)
		}
	}
}

func TestClosures(t *testing.T) {
	tests := []struct {
		name      string
		expr      string
		expect    Value
		shouldErr bool
	}{
		{
			name:   "simple closure",
			expr:   "(begin (define make-adder (lambda (n) (lambda (x) (+ x n)))) (define add5 (make-adder 5)) (add5 10))",
			expect: Integer(15),
		},
		{
			name:   "closure with mutation",
			expr:   "(begin (define x 10) (define set-x! (lambda (v) (set! x v))) (set-x! 20) x)",
			expect: Integer(20),
		},
		{
			name:   "counter using closure",
			expr:   "(begin (define make-counter (lambda () (begin (define n 0) (lambda () (set! n (+ n 1)) n)))) (define c1 (make-counter)) (c1) (c1) (c1))",
			expect: Integer(3),
		},
		{
			name:   "independent counters",
			expr:   "(begin (define make-counter (lambda () (begin (define n 0) (lambda () (set! n (+ n 1)) n)))) (define c1 (make-counter)) (define c2 (make-counter)) (c1) (c2) (c1))",
			expect: Integer(2),
		},
		{
			name: "nested closures with shadowing",
			expr: `(begin
						(define x 1)
						(define (outer y)
							(define x 10)
							(lambda (z) (+ x y z)))
						(define inner (outer 5))
						(inner 2))`,
			expect: Integer(17), // 10 + 5 + 2
		},
		{
			name: "closure over function argument",
			expr: `(begin
						(define (foo x) (lambda (y) (+ x y)))
						(define add2 (foo 2))
						(add2 3))`,
			expect: Integer(5),
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			eval := NewEvaluator() // Re-create for each test to ensure isolation
			env := eval.globalEnv
			got, err := evalExpr(tc.expr, eval, env)
			if tc.shouldErr {
				if err == nil {
					t.Errorf("expected error for %q, got value %v", tc.expr, got)
				}
				return
			}
			if err != nil {
				t.Errorf("unexpected error for %q: %v", tc.expr, err)
				return
			}
			if !deepEqual(got, tc.expect) {
				t.Errorf("got %v, want %v", got, tc.expect)
			}
		})
	}
}

func TestQuoteAndLambda(t *testing.T) {
	eval := NewEvaluator()
	env := eval.globalEnv
	tests := []struct {
		expr      string
		expect    Value
		shouldErr bool
	}{
		// Quoting
		{"(quote foo)", Symbol("foo"), false},
		{"'(1 2 3)", []Value{Integer(1), Integer(2), Integer(3)}, false},
		{"(quote (1 2 3))", []Value{Integer(1), Integer(2), Integer(3)}, false},
		// Lambda: identity
		{"((lambda (x) x) 42)", Integer(42), false},
		// Lambda: add
		{"((lambda (a b) (+ a b)) 10 32)", Integer(42), false},
		// Lambda: closure
		{"(begin (define make-adder (lambda (n) (lambda (x) (+ x n)))) ((make-adder 5) 10))", Integer(15), false},
		// Lambda: multiple body expressions
		{"((lambda (x) 1 2 3 x) 99)", Integer(99), false},
	}
	for _, tc := range tests {
		got, err := evalExpr(tc.expr, eval, env)
		if tc.shouldErr {
			if err == nil {
				t.Errorf("expected error for %q, got value %v", tc.expr, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("unexpected error for %q: %v", tc.expr, err)
			continue
		}
		// deepEqual treats pair chains and slice lists as the same list.
		if !deepEqual(got, tc.expect) {
			t.Errorf("for %q, got %v, want %v", tc.expr, PrintValue(got), PrintValue(tc.expect))
		}
	}
}

func TestMoreSchemeBuiltins(t *testing.T) {
	eval := NewEvaluator()
	env := eval.globalEnv
	tests := []struct {
		expr      string
		expect    Value
		shouldErr bool
	}{
		// modulo
		{"(modulo 13 4)", Integer(1), false},
		{"(modulo -13 4)", Integer(3), false},
		{"(modulo 13 -4)", Integer(-3), false},
		// remainder
		{"(remainder 13 4)", Integer(1), false},
		{"(remainder -13 4)", Integer(-1), false},
		{"(remainder 13 -4)", Integer(1), false},
		// quotient
		{"(quotient 13 4)", Integer(3), false},
		{"(quotient -13 4)", Integer(-3), false},
		// abs
		{"(abs -42)", Integer(42), false},
		{"(abs 42)", Integer(42), false},
		// max/min
		{"(max 1 5 3 2)", Integer(5), false},
		{"(min -1 5 3 2)", Integer(-1), false},
		// pair?
		{"(pair? (list 1 2))", true, false},
		{"(pair? (list))", false, false},
		// procedure?
		{"(procedure? +)", true, false},
		{"(procedure? (lambda (x) x))", true, false},
		{"(procedure? 42)", false, false},
		// reverse
		{"(reverse (list 1 2 3))", []Value{Integer(3), Integer(2), Integer(1)}, false},
		{"(reverse (list))", []Value{}, false},
		// apply
		{"(apply + (list 1 2 3))", Integer(6), false},
		{"(apply + 1 2 (list 3 4))", Integer(10), false},
		{"(apply (lambda (a b) (* a b)) (list 6 7))", Integer(42), false},
	}
	for _, tc := range tests {
		got, err := evalExpr(tc.expr, eval, env)
		if tc.shouldErr {
			if err == nil {
				t.Errorf("expected error for %q, got value %v", tc.expr, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("unexpected error for %q: %v", tc.expr, err)
			continue
		}
		// deepEqual treats pair chains and slice lists as the same list.
		if !deepEqual(got, tc.expect) {
			t.Errorf("for %q, got %v, want %v", tc.expr, PrintValue(got), PrintValue(tc.expect))
		}
	}
}

func TestMathBuiltins(t *testing.T) {
	eval := NewEvaluator()
	env := eval.globalEnv
	tests := []struct {
		expr      string
		expect    Value
		shouldErr bool
	}{
		{"(cos 0)", Number(1), false},
		{"(sin 0)", Number(0), false},
		{"(exp 1)", Number(math.Exp(1)), false},
		{"(sqrt 4)", Integer(2), false},
		{"(log 1)", Number(0), false},
		{"(round 2.7)", Number(3), false},
		{"(floor 2.7)", Number(2), false},
		{"(expt 2 3)", Integer(8), false},
		// Error cases
		{"(cos)", nil, true},
		{"(cos #t)", nil, true},
		{"(expt 2)", nil, true},
		{"(expt 2 #t)", nil, true},
	}
	for _, tc := range tests {
		got, err := evalExpr(tc.expr, eval, env)
		if tc.shouldErr {
			if err == nil {
				t.Errorf("expected error for %q, got value %v", tc.expr, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("unexpected error for %q: %v", tc.expr, err)
			continue
		}
		// For floating point results, allow a small epsilon
		if gNum, ok := got.(Number); ok {
			eNum, ok := tc.expect.(Number)
			if !ok || math.Abs(float64(gNum)-float64(eNum)) > 1e-9 {
				t.Errorf("for %q, got %v, want %v", tc.expr, got, tc.expect)
			}
		} else if got != tc.expect {
			t.Errorf("for %q, got %v, want %v", tc.expr, got, tc.expect)
		}
	}
}

func BenchmarkFibEval(b *testing.B) {
	eval := NewEvaluator()
	env := eval.globalEnv
	// Define a recursive Fibonacci function in Scheme
	_, err := evalExpr("(define fib (lambda (n) (if (< n 2) n (+ (fib (- n 1)) (fib (- n 2))))))", eval, env)
	if err != nil {
		b.Fatalf("define fib failed: %v", err)
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		res, err := evalExpr("(fib 30)", eval, env)
		if err != nil {
			b.Fatalf("evalExpr failed: %v", err)
		}
		num, ok := res.(Integer)
		if !ok || num != 832040 {
			b.Fatalf("fib(30) result incorrect: got %v", res)
		}
	}
}

func TestDictionary(t *testing.T) {
	eval := NewEvaluator()
	env := eval.globalEnv
	tests := []struct {
		expr   string
		expect Dictionary
	}{
		{"{:age (+ 1 2)}", Dictionary{"age": Integer(3)}},
		{"{:a 1 :b 2}", Dictionary{"a": Integer(1), "b": Integer(2)}},
		{"{:x (* 2 3) :y (- 10 4)}", Dictionary{"x": Integer(6), "y": Integer(6)}},
	}
	for _, tc := range tests {
		got, err := evalExpr(tc.expr, eval, env)
		if err != nil {
			t.Errorf("evalExpr(%q) error: %v", tc.expr, err)
			continue
		}
		dict, ok := got.(Dictionary)
		if !ok {
			t.Errorf("evalExpr(%q) = %T, want Dictionary", tc.expr, got)
			continue
		}
		if len(dict) != len(tc.expect) {
			t.Errorf("evalExpr(%q) = %v, want %v (length mismatch)", tc.expr, dict, tc.expect)
			continue
		}
		for k, v := range tc.expect {
			gv, ok := dict[k]
			if !ok || gv != v {
				t.Errorf("evalExpr(%q) key %v: got %v, want %v", tc.expr, k, gv, v)
			}
		}
	}
}

func TestLet(t *testing.T) {
	tests := []struct {
		name      string
		setup     string
		expr      string
		expect    Value
		shouldErr bool
	}{
		{
			name:   "simple let",
			expr:   "(let ((x 2) (y 3)) (+ x y))",
			expect: Integer(5),
		},
		{
			name:   "nested let",
			expr:   "(let ((x 2)) (let ((y 3)) (+ x y)))",
			expect: Integer(5),
		},
		{
			name:      "let bindings are not sequential",
			expr:      "(let ((x 1) (y x)) y)",
			shouldErr: true,
		},
		{
			name:   "let with multiple body expressions",
			expr:   "(let ((x 1)) (+ x 1) (+ x 2))",
			expect: Integer(3),
		},
		{
			name:   "let does not modify outer scope",
			setup:  "(define x 10)",
			expr:   "(begin (let ((x 20)) (* x 2)) x)",
			expect: Integer(10),
		},
		{
			name:   "named let loops",
			expr:   "(let loop ((i 0) (acc 0)) (if (= i 5) acc (loop (+ i 1) (+ acc i))))",
			expect: Integer(10),
		},
		{
			name: "named let as inner helper",
			// The shape LLMs write for primality testing.
			setup: "(define (prime? n) (if (< n 2) false (let loop ((i 2)) (cond ((> (* i i) n) true) ((= (remainder n i) 0) false) (else (loop (+ i 1)))))))",
			expr:  "(map prime? (list 1 2 3 4 5 23 232))",
			expect: []Value{
				false, true, true, false, true, true, false,
			},
		},
		{
			name:      "named let name is scoped to the body",
			expr:      "(begin (let loop ((i 0)) i) loop)",
			shouldErr: true,
		},
		{
			name:      "named let without bindings errors",
			expr:      "(let loop)",
			shouldErr: true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			eval := NewEvaluator()
			env := eval.globalEnv
			if tc.setup != "" {
				_, err := evalExpr(tc.setup, eval, env)
				if err != nil {
					t.Fatalf("setup for test failed: %v", err)
				}
			}
			got, err := evalExpr(tc.expr, eval, env)
			if tc.shouldErr {
				if err == nil {
					t.Errorf("expected error for %q, got value %v", tc.expr, got)
				}
				return
			}
			if err != nil {
				t.Errorf("unexpected error for %q: %v", tc.expr, err)
				return
			}
			if !deepEqual(got, tc.expect) {
				t.Errorf("for %q, got %v, want %v", tc.expr, got, tc.expect)
			}
		})
	}
}

func TestCond(t *testing.T) {
	tests := []struct {
		expr      string
		expect    Value
		shouldErr bool
		err       string
	}{
		{expr: `(cond ((> 2 1) "greater") (else "less"))`, expect: String("greater")},
		{expr: `(cond ((< 2 1) "greater") (else "less"))`, expect: String("less")},
		{expr: `(cond ((< 1 2) "first") ((< 2 3) "second"))`, expect: String("first")},
		{expr: `(cond ((> 1 2) "first") ((< 2 3) "second"))`, expect: String("second")},
		{expr: `(cond ((> 1 2) "first") ((> 2 3) "second") (else "last"))`, expect: String("last")},
		{expr: `(cond (#f "no") (#t "yes"))`, expect: String("yes")},
		{expr: `(cond ((= 1 1) (begin (define x 10) (+ x 5))))`, expect: Integer(15)},
		{expr: `(cond ((> 5 4) "ok"))`, expect: String("ok")},
		{expr: `(cond ((< 5 4) "not ok"))`, expect: nil},
		{expr: `(cond)`, expect: nil},
		{expr: `(cond (else "default"))`, expect: String("default")},
		{expr: `(cond (else (begin (define y 2) y)))`, expect: Integer(2)},
		{expr: `(cond (else))`, shouldErr: true, err: "cond: else clause must have a body"},
		{expr: `(cond (1))`, shouldErr: true, err: "cond: clause requires at least one expression"},
		{expr: `(cond (else 1) (#t 2))`, shouldErr: true, err: "cond: else must be the last clause"},
		{expr: `(cond "string")`, shouldErr: true, err: "cond: clause must be a list"},
	}

	evaluator := NewEvaluator()
	env := evaluator.GlobalEnv()

	for _, tc := range tests {
		t.Run(tc.expr, func(t *testing.T) {
			got, err := evalExpr(tc.expr, evaluator, env)

			if tc.shouldErr {
				if err == nil {
					t.Errorf("expected error for %q, but got none", tc.expr)
				} else if err.Error() != tc.err {
					t.Errorf("for %q, expected error %q, got %q", tc.expr, tc.err, err.Error())
				}
				return
			}

			if err != nil {
				t.Errorf("unexpected error for %q: %v", tc.expr, err)
				return
			}

			if !deepEqual(got, tc.expect) {
				t.Errorf("for %q, got %v, want %v", tc.expr, got, tc.expect)
			}
		})
	}
}

func TestTCO(t *testing.T) {
	tests := []struct {
		name      string
		setup     string
		expr      string
		expect    Value
		shouldErr bool
	}{
		{
			name:   "self-recursive tail call",
			setup:  `(define (countdown n) (if (= n 0) "done" (countdown (- n 1))))`,
			expr:   `(countdown 200000)`,
			expect: String("done"),
		},
		{
			name:   "mutual recursion tail call",
			setup:  `(begin (define (is-even? n) (if (= n 0) #t (is-odd? (- n 1)))) (define (is-odd? n) (if (= n 0) #f (is-even? (- n 1)))))`,
			expr:   `(is-even? 200000)`,
			expect: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			eval := NewEvaluator()
			env := eval.globalEnv
			if tc.setup != "" {
				_, err := evalExpr(tc.setup, eval, env)
				if err != nil {
					t.Fatalf("setup for test failed: %v", err)
				}
			}
			got, err := evalExpr(tc.expr, eval, env)
			if tc.shouldErr {
				if err == nil {
					t.Errorf("expected error for %q, got value %v", tc.expr, got)
				}
				return
			}
			if err != nil {
				t.Errorf("unexpected error for %q: %v", tc.expr, err)
				return
			}
			if !deepEqual(got, tc.expect) {
				t.Errorf("for %q, got %v, want %v", tc.expr, got, tc.expect)
			}
		})
	}
}

func TestEvalAll_Basic(t *testing.T) {
	eval := NewEvaluator()
	env := eval.globalEnv
	exprs, err := ParseAll("(+ 1 2) (* 2 3)")
	if err != nil {
		t.Fatalf("ParseAll error: %v", err)
	}
	result, err := eval.EvalAll(exprs, env)
	if err != nil {
		t.Fatalf("EvalAll error: %v", err)
	}
	if num, ok := result.(Integer); !ok || num != 6 {
		t.Errorf("EvalAll last result = %v, want 6", result)
	}
}

func TestEvalAll_SkipsNil(t *testing.T) {
	eval := NewEvaluator()
	env := eval.globalEnv
	// Insert a nil between two valid expressions
	exprs, err := ParseAll("(+ 1 2) (* 2 3)")
	if err != nil {
		t.Fatalf("ParseAll error: %v", err)
	}
	exprs = append([]Value{nil}, exprs...)
	result, err := eval.EvalAll(exprs, env)
	if err != nil {
		t.Fatalf("EvalAll error: %v", err)
	}
	if num, ok := result.(Integer); !ok || num != 6 {
		t.Errorf("EvalAll last result = %v, want 6", result)
	}
}

func TestRecordResult(t *testing.T) {
	ev := NewEvaluator()
	env := ev.globalEnv

	if v, err := env.Lookup("*1"); err != nil || v != nil {
		t.Fatalf("*1 should start bound to nil, got %v, %v", v, err)
	}

	ev.RecordResult(Integer(1))
	ev.RecordResult(nil) // define/for-each results must not shift
	ev.RecordResult(Integer(2))
	ev.RecordResult(Integer(3))

	exprs, err := ParseAll("(list *1 *2 *3)")
	if err != nil {
		t.Fatalf("ParseAll error: %v", err)
	}
	result, err := ev.EvalAll(exprs, env)
	if err != nil {
		t.Fatalf("EvalAll error: %v", err)
	}
	if got := PrintValue(result); got != "(3 2 1)" {
		t.Errorf("(list *1 *2 *3) = %s, want (3 2 1)", got)
	}
}

func TestR4RS(t *testing.T) {
	tests := []struct {
		name      string
		expr      string
		expect    Value
		shouldErr bool
		err       string
	}{
		{name: "and", expr: `(and #t #f)`, expect: false},
		{name: "and short-circuits", expr: `(and #f unbound-would-error)`, expect: false},
		{name: "and returns last value", expr: `(and 1 2)`, expect: Integer(2)},
		{name: "and empty", expr: `(and)`, expect: true},
		{name: "or short-circuits", expr: `(or 7 unbound-would-error)`, expect: Integer(7)},
		{name: "or empty", expr: `(or)`, expect: false},
		{name: "if without else", expr: `(if #f 1)`, expect: nil},
		{name: "eq? on lists does not panic", expr: `(eq? '(1) '(1))`, expect: false},
		{name: "eq? same builtin", expr: `(eq? car car)`, expect: true},
		{name: "equal? dictionaries", expr: `(equal? {:a 1 :b '(2)} {:b '(2) :a 1})`, expect: true},
		{name: "begin", expr: `(begin 1 2 3)`, expect: Integer(3)},
		{name: "case", expr: `(case 1 ((1) 'one) (else 'other))`, expect: Symbol("one")},
		{name: "cond", expr: `(cond ((= 1 1) 'yes) (else 'no))`, expect: Symbol("yes")},
		{name: "define", expr: `(begin (define x 42) x)`, expect: Integer(42)},
		{name: "do", expr: `(do ((i 0 (+ i 1))) ((= i 30) i))`, expect: Integer(30), shouldErr: false}, // If do is not implemented, set shouldErr: true
		{name: "else", expr: `(cond (#f 'no) (else 'yes))`, expect: Symbol("yes")},
		{name: "if", expr: `(if #t 1 2)`, expect: Integer(1)},
		{name: "lambda", expr: `((lambda (x) x) 5)`, expect: Integer(5)},
		{name: "let", expr: `(let ((x 1)) x)`, expect: Integer(1)},
		{name: "let*", expr: `(let* ((x 1) (y (+ x 1))) y)`, expect: Integer(2), shouldErr: false},
		{name: "letrec", expr: `(letrec ((even? (lambda (n) (if (= n 0) #t (odd? (- n 1))))) (odd? (lambda (n) (if (= n 0) #f (even? (- n 1)))))) (even? 10))`, expect: true, shouldErr: false}, // If letrec is not implemented, set shouldErr: true
		{name: "or", expr: `(or #f #t)`, expect: true},
		{name: "quasiquote", expr: "`(1 ,(+ 1 1))", expect: []Value{Integer(1), Integer(2)}},
		{name: "quote", expr: `'(1 2 3)`, expect: []Value{Integer(1), Integer(2), Integer(3)}},
		{name: "set!", expr: `(begin (define x 1) (set! x 2) x)`, expect: Integer(2)},
		{name: "unquote", expr: "`(a ,(+ 1 2))", expect: []Value{Symbol("a"), Integer(3)}},
		{name: "unquote-splicing", expr: "`(a ,@(list 1 2))", expect: []Value{Symbol("a"), Integer(1), Integer(2)}},
	}

	eval := NewEvaluatorWithEnvironment(nil, nil)
	env := eval.globalEnv

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := evalExpr(tc.expr, eval, env)
			if tc.shouldErr {
				if err == nil {
					t.Errorf("expected error for %q, but got none", tc.expr)
				} else if tc.err != "" && err.Error() != tc.err {
					t.Errorf("for %q, expected error %q, got %q", tc.expr, tc.err, err.Error())
				}
				return
			}
			if err != nil {
				t.Errorf("unexpected error for %q: %v", tc.expr, err)
				return
			}
			if !deepEqual(got, tc.expect) {
				t.Errorf("for %q, got %v, want %v", tc.expr, got, tc.expect)
			}
		})
	}
}
