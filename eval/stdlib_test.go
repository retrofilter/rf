package eval

import (
	"testing"
)

func runStdlibTests(t *testing.T, tests []struct {
	expr      string
	expect    Value
	shouldErr bool
}) {
	t.Helper()
	eval := NewEvaluator()
	env := eval.globalEnv
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

func TestLetStar(t *testing.T) {
	runStdlibTests(t, []struct {
		expr      string
		expect    Value
		shouldErr bool
	}{
		// Sequential bindings: later bindings see earlier ones
		{"(let* ((x 1) (y (+ x 1))) y)", Integer(2), false},
		{"(let* ((x 1) (y (+ x 1)) (z (* y 3))) z)", Integer(6), false},
		// Single binding behaves like let
		{"(let* ((x 5)) (* x x))", Integer(25), false},
		// Empty bindings
		{"(let* () 42)", Integer(42), false},
		// Shadowing
		{"(let* ((x 1) (x (+ x 10))) x)", Integer(11), false},
		// Multiple body expressions
		{"(let* ((x 1)) (+ x 1) (+ x 2))", Integer(3), false},
		// Does not leak into outer scope
		{"(begin (define lsx 10) (let* ((lsx 20)) lsx) lsx)", Integer(10), false},
		// Errors
		{"(let* ((x 1)))", nil, true},
		{"(let* x 1)", nil, true},
		{"(let* ((x)) x)", nil, true},
	})
}

func TestWhenUnless(t *testing.T) {
	runStdlibTests(t, []struct {
		expr      string
		expect    Value
		shouldErr bool
	}{
		{"(when #t 1)", Integer(1), false},
		{"(when #f 1)", nil, false},
		{"(when (> 2 1) 1 2 3)", Integer(3), false},
		{"(unless #f 1)", Integer(1), false},
		{"(unless #t 1)", nil, false},
		{"(unless (> 1 2) 1 2 3)", Integer(3), false},
		// Side effects run in order
		{"(begin (define wx 0) (when #t (set! wx 1) (set! wx (+ wx 1))) wx)", Integer(2), false},
		{"(begin (define ux 0) (unless #t (set! ux 99)) ux)", Integer(0), false},
		// Errors: missing body
		{"(when #t)", nil, true},
		{"(unless #f)", nil, true},
	})
}

func TestCase(t *testing.T) {
	runStdlibTests(t, []struct {
		expr      string
		expect    Value
		shouldErr bool
	}{
		{"(case 1 ((1) \"one\") (else \"other\"))", String("one"), false},
		{"(case 2 ((1) \"one\") ((2 3) \"two-or-three\") (else \"other\"))", String("two-or-three"), false},
		{"(case 9 ((1) \"one\") (else \"other\"))", String("other"), false},
		// No match, no else -> nil
		{"(case 9 ((1) \"one\"))", nil, false},
		// Key expression is evaluated
		{"(case (+ 1 1) ((2) \"yes\") (else \"no\"))", String("yes"), false},
		// Symbol datums via quote comparison
		{"(case (quote b) ((a) 1) ((b) 2) (else 3))", Integer(2), false},
		// Multiple body expressions
		{"(case 1 ((1) 1 2 3) (else 0))", Integer(3), false},
		// Errors
		{"(case 1)", nil, true},
		{"(case 1 \"clause\")", nil, true},
		{"(case 1 ((1)))", nil, true},
		{"(case 1 (else 1) ((2) 2))", nil, true},
		{"(case 1 (1 \"one\"))", nil, true},
	})
}

func TestStringBuiltins(t *testing.T) {
	runStdlibTests(t, []struct {
		expr      string
		expect    Value
		shouldErr bool
	}{
		// string-length
		{"(string-length \"hello\")", Integer(5), false},
		{"(string-length \"\")", Integer(0), false},
		{"(string-length 42)", nil, true},
		{"(string-length)", nil, true},

		// substring
		{"(substring \"hello\" 1 3)", String("el"), false},
		{"(substring \"hello\" 2)", String("llo"), false},
		{"(substring \"hello\" 0 0)", String(""), false},
		{"(substring \"hello\" 3 99)", nil, true},
		{"(substring \"hello\" 3 1)", nil, true},
		{"(substring 42 0 1)", nil, true},

		// string-append
		{"(string-append \"foo\" \"bar\" \"baz\")", String("foobarbaz"), false},
		{"(string-append)", String(""), false},
		{"(string-append \"foo\" 42)", nil, true},

		// string-split
		{"(string-split \"a,b,c\" \",\")", []Value{String("a"), String("b"), String("c")}, false},
		{"(string-split \"abc\" \",\")", []Value{String("abc")}, false},
		{"(string-split 42 \",\")", nil, true},

		// string-join
		{"(string-join (list \"a\" \"b\" \"c\") \"-\")", String("a-b-c"), false},
		{"(string-join (list) \"-\")", String(""), false},
		{"(string-join (list \"a\" 42) \"-\")", nil, true},
		{"(string-join \"abc\" \"-\")", nil, true},

		// string-upcase / string-downcase
		{"(string-upcase \"Hello\")", String("HELLO"), false},
		{"(string-downcase \"Hello\")", String("hello"), false},
		{"(string-upcase 42)", nil, true},
		{"(string-downcase 42)", nil, true},

		// string-contains?
		{"(string-contains? \"hello world\" \"world\")", true, false},
		{"(string-contains? \"hello\" \"xyz\")", false, false},
		{"(string-contains? \"hello\" 42)", nil, true},

		// string-trim
		{"(string-trim \"  hi \\n\")", String("hi"), false},
		{"(string-trim \"hi\")", String("hi"), false},
		{"(string-trim \"--hi--\" \"-\")", String("hi"), false},
		{"(string-trim 42)", nil, true},
		{"(string-trim \"x\" 42)", nil, true},
		{"(string-trim)", nil, true},

		// string-replace (all occurrences)
		{"(string-replace \"a-b-c\" \"-\" \"+\")", String("a+b+c"), false},
		{"(string-replace \"abc\" \"x\" \"y\")", String("abc"), false},
		{"(string-replace \"abc\" \"b\" \"\")", String("ac"), false},
		{"(string-replace \"abc\" \"\" \"y\")", nil, true},
		{"(string-replace \"abc\" \"b\")", nil, true},
		{"(string-replace 42 \"a\" \"b\")", nil, true},

		// string-prefix? / string-suffix?
		{"(string-prefix? \"notes.md\" \"notes\")", true, false},
		{"(string-prefix? \"notes.md\" \"md\")", false, false},
		{"(string-suffix? \"notes.md\" \".md\")", true, false},
		{"(string-suffix? \"notes.md\" \"notes\")", false, false},
		{"(string-prefix? \"x\" 42)", nil, true},
		{"(string-suffix? 42 \"x\")", nil, true},

		// match: (full-match group1 ...) or #f, pattern first like grep
		{"(match \"v([0-9.]+)\" \"rf v1.2.3 ready\")", []Value{String("v1.2.3"), String("1.2.3")}, false},
		{"(match \"[0-9]+\" \"abc 42\")", []Value{String("42")}, false},
		{"(match \"(a)(b)?\" \"a\")", []Value{String("a"), String("a"), String("")}, false},
		{"(match \"xyz\" \"abc\")", false, false},
		{"(match \"(\" \"abc\")", nil, true},
		{"(match \"a\" 42)", nil, true},

		// string->number
		{"(string->number \"42\")", Integer(42), false},
		{"(string->number \"3.14\")", Number(3.14), false},
		{"(string->number \"abc\")", false, false},
		{"(string->number 42)", nil, true},

		// number->string
		{"(number->string 42)", String("42"), false},
		{"(number->string 3.14)", String("3.14"), false},
		{"(number->string \"abc\")", nil, true},

		// string->symbol / symbol->string
		{"(string->symbol \"foo\")", Symbol("foo"), false},
		{"(symbol->string (quote foo))", String("foo"), false},
		{"(string->symbol 42)", nil, true},
		{"(symbol->string \"foo\")", nil, true},

		// string=?
		{"(string=? \"abc\" \"abc\")", true, false},
		{"(string=? \"abc\" \"abc\" \"abc\")", true, false},
		{"(string=? \"abc\" \"abd\")", false, false},
		{"(string=? \"abc\")", nil, true},
		{"(string=? \"abc\" 42)", nil, true},
	})
}

func TestNumericPredicates(t *testing.T) {
	runStdlibTests(t, []struct {
		expr      string
		expect    Value
		shouldErr bool
	}{
		{"(zero? 0)", true, false},
		{"(zero? 1)", false, false},
		{"(zero? \"a\")", nil, true},
		{"(positive? 3)", true, false},
		{"(positive? -3)", false, false},
		{"(positive? 0)", false, false},
		{"(positive? \"a\")", nil, true},
		{"(negative? -3)", true, false},
		{"(negative? 3)", false, false},
		{"(negative? \"a\")", nil, true},
		{"(even? 4)", true, false},
		{"(even? 3)", false, false},
		{"(even? 0)", true, false},
		{"(even? -2)", true, false},
		{"(even? 2.5)", nil, true},
		{"(even? \"a\")", nil, true},
		{"(odd? 3)", true, false},
		{"(odd? 4)", false, false},
		{"(odd? -3)", true, false},
		{"(odd? 2.5)", nil, true},
		{"(odd? \"a\")", nil, true},
	})
}

func TestHigherOrderBuiltins(t *testing.T) {
	runStdlibTests(t, []struct {
		expr      string
		expect    Value
		shouldErr bool
	}{
		// for-each: returns nil, runs side effects in order
		{"(begin (define fe 0) (for-each (lambda (x) (set! fe (+ fe x))) (list 1 2 3)) fe)", Integer(6), false},
		{"(for-each not (list #t #f))", nil, false},
		{"(for-each not 42)", nil, true},
		{"(for-each not)", nil, true},

		// fold-left / reduce
		{"(fold-left + 0 (list 1 2 3 4))", Integer(10), false},
		{"(fold-left - 0 (list 1 2 3))", Integer(-6), false},
		{"(fold-left (lambda (acc x) (+ acc x)) 100 (list 1 2 3))", Integer(106), false},
		{"(reduce + 0 (list 1 2 3 4))", Integer(10), false},
		{"(fold-left + 0 42)", nil, true},
		{"(fold-left + 0)", nil, true},

		// fold-right
		{"(fold-right - 0 (list 1 2 3))", Integer(2), false},
		{"(fold-right cons (list) (list 1 2 3))", []Value{Integer(1), Integer(2), Integer(3)}, false},
		{"(fold-right + 0 42)", nil, true},

		// assoc
		{"(assoc 2 (list (list 1 \"one\") (list 2 \"two\")))", []Value{Integer(2), String("two")}, false},
		{"(assoc 9 (list (list 1 \"one\") (list 2 \"two\")))", false, false},
		{"(assoc \"b\" (list (list \"a\" 1) (list \"b\" 2)))", []Value{String("b"), Integer(2)}, false},
		{"(assoc 1 42)", nil, true},
		{"(assoc 1 (list 1 2))", nil, true},

		// sort
		{"(sort (list 3 1 2))", []Value{Integer(1), Integer(2), Integer(3)}, false},
		{"(sort (list \"banana\" \"apple\" \"cherry\"))", []Value{String("apple"), String("banana"), String("cherry")}, false},
		{"(sort (list))", []Value{}, false},
		{"(sort (list 3 1 2) (lambda (a b) (> a b)))", []Value{Integer(3), Integer(2), Integer(1)}, false},
		{"(sort (list 3 1 2) >)", []Value{Integer(3), Integer(2), Integer(1)}, false},
		{"(sort (list 1 \"a\"))", nil, true},
		{"(sort 42)", nil, true},
		{"(sort (list 1 2) (lambda (a b) (car a)))", nil, true},
	})
}

func TestSortDoesNotMutateInput(t *testing.T) {
	eval := NewEvaluator()
	env := eval.globalEnv
	if _, err := evalExpr("(define sortlst (list 3 1 2))", eval, env); err != nil {
		t.Fatalf("setup failed: %v", err)
	}
	if _, err := evalExpr("(sort sortlst)", eval, env); err != nil {
		t.Fatalf("sort failed: %v", err)
	}
	got, err := evalExpr("sortlst", eval, env)
	if err != nil {
		t.Fatalf("lookup failed: %v", err)
	}
	if !deepEqual(got, []Value{Integer(3), Integer(1), Integer(2)}) {
		t.Errorf("sort mutated its input: got %v", got)
	}
}

func TestStdlibIntegration(t *testing.T) {
	runStdlibTests(t, []struct {
		expr      string
		expect    Value
		shouldErr bool
	}{
		// map/filter accept lambdas (fixed alongside stdlib via callFunction)
		{"(map (lambda (x) (* x x)) (list 2 3))", []Value{Integer(4), Integer(9)}, false},
		{"(filter (lambda (x) (even? x)) (list 1 2 3 4))", []Value{Integer(2), Integer(4)}, false},
		{"(map (lambda (x) x) 42)", nil, true},
		// case with string datums (member/deepEqual on strings)
		{"(case \"b\" ((\"a\") 1) ((\"b\") 2) (else 3))", Integer(2), false},
		// case body can reference outer bindings
		{"(begin (define cv 5) (case 1 ((1) (+ cv 1)) (else 0)))", Integer(6), false},
		// quasiquote with lambda inside unquote-splicing
		{"`(0 ,@(map (lambda (x) (* x x)) (list 2 3)) 9)", []Value{Integer(0), Integer(4), Integer(9), Integer(9)}, false},
		// quasiquote builds code as data
		{"(begin (define qq `(+ 1 ,(+ 1 1))) (car qq))", Symbol("+"), false},
		// let* bindings close over earlier bindings via lambda
		{"(let* ((a 1) (f (lambda (x) (+ x a))) (b (f 10))) b)", Integer(11), false},
		// when returns quasiquoted value
		{"(when #t `(x ,(+ 1 1)))", []Value{Symbol("x"), Integer(2)}, false},
		// fold-left with string-append across a split
		{"(fold-left string-append \"\" (string-split \"a-b-c\" \"-\"))", String("abc"), false},
		// sort strings with custom comparator
		{"(sort (list \"b\" \"a\") (lambda (x y) (string=? x \"a\")))", []Value{String("a"), String("b")}, false},
	})
}

func TestLetStarTCO(t *testing.T) {
	eval := NewEvaluator()
	env := eval.globalEnv
	setup := "(define (count n acc) (let* ((next (- n 1))) (if (= n 0) acc (count next (+ acc 1)))))"
	if _, err := evalExpr(setup, eval, env); err != nil {
		t.Fatalf("setup: %v", err)
	}
	got, err := evalExpr("(count 100000 0)", eval, env)
	if err != nil {
		t.Fatalf("deep recursion with let* failed: %v", err)
	}
	if !deepEqual(got, Integer(100000)) {
		t.Errorf("got %v", got)
	}
}

func TestNamedLetTCO(t *testing.T) {
	eval := NewEvaluator()
	env := eval.globalEnv
	got, err := evalExpr("(let loop ((i 0) (acc 0)) (if (= i 100000) acc (loop (+ i 1) (+ acc 1))))", eval, env)
	if err != nil {
		t.Fatalf("deep named let failed: %v", err)
	}
	if !deepEqual(got, Integer(100000)) {
		t.Errorf("got %v", got)
	}
}

func TestQuasiquote(t *testing.T) {
	runStdlibTests(t, []struct {
		expr      string
		expect    Value
		shouldErr bool
	}{
		// Plain quasiquote acts like quote
		{"`(1 2 3)", []Value{Integer(1), Integer(2), Integer(3)}, false},
		{"`foo", Symbol("foo"), false},
		// Unquote
		{"`(1 ,(+ 1 1))", []Value{Integer(1), Integer(2)}, false},
		{"`(a ,(+ 1 2))", []Value{Symbol("a"), Integer(3)}, false},
		{"`,(+ 1 2)", Integer(3), false},
		// Unquote-splicing
		{"`(a ,@(list 1 2) b)", []Value{Symbol("a"), Integer(1), Integer(2), Symbol("b")}, false},
		{"`(,@(list 1 2) ,@(list 3))", []Value{Integer(1), Integer(2), Integer(3)}, false},
		// Nested lists
		{"`(1 (2 ,(+ 1 2)))", []Value{Integer(1), []Value{Integer(2), Integer(3)}}, false},
		// Nested quasiquote: inner unquote is not evaluated at depth 2
		{"`(1 `(2 ,(+ 1 2)))", []Value{Integer(1), []Value{Symbol("quasiquote"), []Value{Integer(2), []Value{Symbol("unquote"), []Value{Symbol("+"), Integer(1), Integer(2)}}}}}, false},
		// Explicit forms (no reader syntax)
		{"(quasiquote (1 (unquote (+ 1 1))))", []Value{Integer(1), Integer(2)}, false},
		// Vector templates (§4.2.8): unquote and splicing inside #(...)
		{"`#(1 ,(+ 1 1) 3)", Vector{Integer(1), Integer(2), Integer(3)}, false},
		{"`#(10 5 ,(* 2 2) ,@(map (lambda (x) (* x x)) '(4 3)) 8)",
			Vector{Integer(10), Integer(5), Integer(4), Integer(16), Integer(9), Integer(8)}, false},
		{"`#(a ,@'() b)", Vector{Symbol("a"), Symbol("b")}, false},
		{"`#(1 #(2 ,(+ 1 2)))", Vector{Integer(1), Vector{Integer(2), Integer(3)}}, false},
		// Depth guard: inside a nested quasiquote a vector's unquote stays
		{"`(x `#(,(+ 1 2)))", []Value{Symbol("x"), []Value{Symbol("quasiquote"),
			Vector{[]Value{Symbol("unquote"), []Value{Symbol("+"), Integer(1), Integer(2)}}}}}, false},
		{"`#(a ,@2)", nil, true},
		// Errors
		{"(unquote 1)", nil, true},
		{"(unquote-splicing (list 1))", nil, true},
		{"`,@(list 1 2)", nil, true},
		{"`(a ,@2)", nil, true},
		{"(quasiquote)", nil, true},
	})
}

func TestNumericEqualityAcrossSplit(t *testing.T) {
	runStdlibTests(t, []struct {
		expr      string
		expect    Value
		shouldErr bool
	}{
		// The easy cases keep holding.
		{"(= 1 1.0)", true, false},
		{"(= 1.0 1)", true, false},
		{"(= 2 2.5)", false, false},
		{"(= 1 1.0 1)", true, false},
		{"(= 9007199254740993 9.007199254740992e15)", false, false},
		{"(= 9.007199254740992e15 9007199254740993)", false, false},
		{"(= 9007199254740992 9.007199254740992e15)", true, false},
		{"(= -9223372036854775808 -9.223372036854776e18)", true, false},
		{"(= 9223372036854775807 9.223372036854776e18)", false, false},
		// Non-integral, infinite, and NaN floats never equal an exact int.
		{"(= 1 +inf.0)", false, false},
		{"(= 1 +nan.0)", false, false},
		{"(= 0 -0.0)", true, false},
	})
}
