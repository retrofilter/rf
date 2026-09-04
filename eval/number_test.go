package eval

import (
	"strings"
	"testing"
)

func TestOrderedComparePrecision(t *testing.T) {
	eval := NewEvaluator()
	env := eval.globalEnv
	tests := []struct {
		expr   string
		expect Value
	}{
		// 9007199254740992 = 2^53; both convert to the same float64.
		{"(< 9007199254740992 9007199254740993)", true},
		{"(> 9007199254740993 9007199254740992)", true},
		{"(<= 9007199254740993 9007199254740992)", false},
		{"(>= 9007199254740992 9007199254740993)", false},
		{"(< 9007199254740993 9007199254740993)", false},
		{"(<= 9007199254740993 9007199254740993)", true},
		{"(max 9007199254740992 9007199254740993)", Integer(9007199254740993)},
		{"(min 9007199254740993 9007199254740992)", Integer(9007199254740992)},
		{"(< 9007199254740993 9.007199254740992e15)", false},
		{"(> 9007199254740993 9.007199254740992e15)", true},
		{"(< 9007199254740992 9.007199254740992e15)", false},
		{"(<= 9007199254740992 9.007199254740992e15)", true},
		// Fractions break integer-part ties in either direction.
		{"(< 2 2.5)", true},
		{"(> -2 -2.5)", true},
		{"(< -3 -2.5)", true},
		// Infinities and NaN.
		{"(< 9007199254740993 +inf.0)", true},
		{"(> 9007199254740993 -inf.0)", true},
		{"(< 1 +nan.0)", false},
		{"(<= +nan.0 +nan.0)", false},
		{"(>= 1 +nan.0)", false},
		// min/max exactness contagion survives the rewrite (§6.2.6).
		{"(max 3 2.5)", Number(3.0)},
		{"(min 1 2.5)", Number(1.0)},
		{"(max 1 2)", Integer(2)},
		{"(max 1 +nan.0)", Number(1.0)},
		{"(assert 0)", true},
		{"(assert \"\")", true},
	}
	for _, tc := range tests {
		got, err := evalExpr(tc.expr, eval, env)
		if err != nil {
			t.Errorf("unexpected error for %q: %v", tc.expr, err)
			continue
		}
		if !deepEqual(got, tc.expect) {
			t.Errorf("for %q, got %v, want %v", tc.expr, PrintValue(got), PrintValue(tc.expect))
		}
	}
}

func TestEqualStepCapErrors(t *testing.T) {
	eval := NewEvaluator()
	env := eval.globalEnv
	setup := []string{
		"(define a (list 1 2 3))",
		"(set-cdr! (cddr a) a)",
		"(define b (list 1 2 3))",
		"(set-cdr! (cddr b) b)",
	}
	for _, expr := range setup {
		if _, err := evalExpr(expr, eval, env); err != nil {
			t.Fatalf("setup %q: %v", expr, err)
		}
	}
	_, err := evalExpr("(equal? a b)", eval, env)
	if err == nil {
		t.Fatal("(equal? a b) on circular lists: want error, got value")
	}
	if !strings.Contains(err.Error(), "circular") {
		t.Fatalf("(equal? a b): error %q should mention circularity", err)
	}
	// A circular list that differs within the cap still answers #f.
	got, err := evalExpr("(equal? a (list 9 9 9))", eval, env)
	if err != nil {
		t.Fatalf("(equal? a '(9 9 9)): %v", err)
	}
	if got != false {
		t.Fatalf("(equal? a '(9 9 9)): got %v, want #f", PrintValue(got))
	}
}
