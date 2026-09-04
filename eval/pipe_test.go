package eval

import (
	"reflect"
	"testing"
)

func TestPipe(t *testing.T) {
	eval := NewEvaluator()
	env := eval.globalEnv
	tests := []struct {
		expr      string
		expect    Value
		shouldErr bool
	}{
		// Single expression: pipe is a no-op wrapper
		{"(pipe (+ 1 2))", Integer(3), false},

		// Thread-last: the piped value becomes each stage's final argument
		{`(pipe (list 1 2 3) (map (lambda (x) (* x 10))))`,
			[]Value{Integer(10), Integer(20), Integer(30)}, false},
		{`(pipe (list "apple" "banana" "cherry") (grep "an"))`,
			[]Value{String("banana")}, false},
		{`(pipe (list "aa" "ab" "bb") (grep "a") (grep "b"))`,
			[]Value{String("ab")}, false},

		// Explicit _ placeholder overrides the append position
		{`(pipe (list 10 20 30) (get 1 _))`, Integer(20), false},
		{`(pipe 5 (- 100 _))`, Integer(95), false},
		// _ works at any depth
		{`(pipe 3 (+ 1 (* 2 _)))`, Integer(7), false},

		// Bare function name: called with just the value
		{`(pipe (list 3 1 2) sort)`, []Value{Integer(1), Integer(2), Integer(3)}, false},

		// Stages see definitions from the surrounding environment
		{`(begin (define (double x) (* x 2)) (pipe 21 (double)))`, Integer(42), false},
		{`(pipe 21 double)`, Integer(42), false},

		// Errors
		{"(pipe)", nil, true},
		{"(pipe 1 42)", nil, true}, // stage is not a call form or symbol
		{"(pipe 1 ())", nil, true}, // empty stage
		{"(pipe 1 (no-such-fn))", nil, true},
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
		if !reflect.DeepEqual(got, tc.expect) {
			t.Errorf("%q: expected %v, got %v", tc.expr, tc.expect, got)
		}
	}
}

func TestPipePlaceholderScope(t *testing.T) {
	eval := NewEvaluator()
	env := eval.globalEnv
	if _, err := evalExpr(`(pipe 1 (+ 1))`, eval, env); err != nil {
		t.Fatal(err)
	}
	if _, err := evalExpr("_", eval, env); err == nil {
		t.Error("_ leaked into the global environment after a pipe")
	}
	got, err := evalExpr(`(pipe 10 (+ (pipe 2 (* 3 _)) _))`, eval, env)
	if err != nil {
		t.Fatal(err)
	}
	// inner pipe: 2*3=6; outer stage: 6+10=16
	if got != Integer(16) {
		t.Errorf("nested pipes: expected 16, got %v", got)
	}
}

func TestTake(t *testing.T) {
	eval := NewEvaluator()
	env := eval.globalEnv
	tests := []struct {
		expr      string
		expect    Value
		shouldErr bool
	}{
		{`(take 2 (list 1 2 3))`, []Value{Integer(1), Integer(2)}, false},
		{`(take 0 (list 1 2))`, []Value{}, false},
		// n beyond the input is the whole input, not an error
		{`(take 9 (list 1 2))`, []Value{Integer(1), Integer(2)}, false},
		// command mode passes n as a numeric string
		{`(take "2" (list 1 2 3))`, []Value{Integer(1), Integer(2)}, false},
		// a string input means its lines, like grep
		{`(take 2 "a\nb\nc\n")`, []Value{String("a"), String("b")}, false},
		// head is the same builtin
		{`(head 1 (list 7 8))`, []Value{Integer(7)}, false},
		// pipeline shape: thread-last
		{`(pipe (list 5 6 7) (take 2) (get 1))`, Integer(6), false},
		// Errors
		{`(take -1 (list 1))`, nil, true},
		{`(take "x" (list 1))`, nil, true},
		{`(take 1 42)`, nil, true},
		{`(take 1)`, nil, true},
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
		if !reflect.DeepEqual(got, tc.expect) {
			t.Errorf("%q: expected %v, got %v", tc.expr, tc.expect, got)
		}
	}
}

func TestBuiltinsListing(t *testing.T) {
	eval := NewEvaluator()
	env := eval.globalEnv
	got, err := evalExpr(`(builtins)`, eval, env)
	if err != nil {
		t.Fatal(err)
	}
	rows, ok := got.([]Value)
	if !ok || len(rows) == 0 {
		t.Fatalf("expected a non-empty list of rows, got %v", got)
	}
	kinds := map[string]string{}
	prev := ""
	for _, row := range rows {
		dict, ok := row.(Dictionary)
		if !ok {
			t.Fatalf("expected dictionary rows, got %v", row)
		}
		name := string(dict["name"].(String))
		if name < prev {
			t.Errorf("rows not sorted: %q after %q", name, prev)
		}
		prev = name
		kinds[name] = string(dict["type"].(String))
	}
	for name, kind := range map[string]string{
		"ls":   "function",
		"take": "function",
		"pipe": "special form",
		"let":  "macro",
	} {
		if kinds[name] != kind {
			t.Errorf("%s: expected kind %q, got %q", name, kind, kinds[name])
		}
	}
	// value bindings and user definitions are not builtins
	if _, found := kinds["*1"]; found {
		t.Error("*1 should not be listed")
	}
	if _, err := evalExpr(`(define (mine x) x)`, eval, env); err != nil {
		t.Fatal(err)
	}
	got, err = evalExpr(`(pipe (builtins) (grep "^mine$") (length))`, eval, env)
	if err != nil {
		t.Fatal(err)
	}
	if got != Integer(0) {
		t.Errorf("user-defined lambda should not be listed, matched %v rows", got)
	}
	if _, err := evalExpr(`(builtins "arg")`, eval, env); err == nil {
		t.Error("expected an arity error for (builtins \"arg\")")
	}
}

func TestGetArgumentOrders(t *testing.T) {
	eval := NewEvaluator()
	env := eval.globalEnv
	if _, err := evalExpr(`(define d {:name "rf" :size 7})`, eval, env); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		expr      string
		expect    Value
		shouldErr bool
	}{
		// Key-first: (get key collection)
		{`(get "name" d)`, String("rf"), false},
		{`(get 2 (list 1 2 3))`, Integer(3), false},
		// Command mode passes indexes as strings
		{`(get "1" (list 1 2 3))`, Integer(2), false},
		// Missing dictionary key is false, not an error
		{`(get "nope" d)`, false, false},
		// Errors
		{`(get d "name")`, nil, true},       // old collection-first order
		{`(get (list 1 2 3) 0)`, nil, true}, // old collection-first order
		{`(get "x" (list 1 2))`, nil, true}, // non-numeric index
		{`(get 1 2)`, nil, true},            // no collection at all
		{`(get d)`, nil, true},              // wrong arity
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
		if !reflect.DeepEqual(got, tc.expect) {
			t.Errorf("%q: expected %v, got %v", tc.expr, tc.expect, got)
		}
	}
}
