package eval

import "testing"

func TestRecords(t *testing.T) {
	ev := NewEvaluator()
	env := ev.globalEnv
	forms, err := ParseAll(`
		(define-record-type <pare> (kons x y) pare? (x kar set-kar!) (y kdr))
		(define-record-type <point> (make-point x) point? (x point-x))`)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if _, err := ev.EvalAll(forms, env); err != nil {
		t.Fatalf("define-record-type: %v", err)
	}
	tests := []struct {
		expr      string
		expect    Value
		shouldErr bool
	}{
		{`(pare? (kons 1 2))`, true, false},
		{`(pare? (cons 1 2))`, false, false},
		{`(pare? 7)`, false, false},
		// Disjoint per record type, not one blanket record type.
		{`(point? (kons 1 2))`, false, false},
		{`(kar (kons 1 2))`, Integer(1), false},
		{`(kdr (kons 1 2))`, Integer(2), false},
		{`(let ((k (kons 1 2))) (set-kar! k 3) (kar k))`, Integer(3), false},
		{`(kar (make-point 5))`, nil, true},
		{`(kons 1)`, nil, true},
		// Records are not pairs/lists/vectors.
		{`(pair? (kons 1 2))`, false, false},
		{`(list? (kons 1 2))`, false, false},
		// equal? is identity for records.
		{`(equal? (kons 1 2) (kons 1 2))`, false, false},
		{`(let ((k (kons 1 2))) (equal? k k))`, true, false},
	}
	for _, tc := range tests {
		got, err := evalExpr(tc.expr, ev, env)
		if tc.shouldErr {
			if err == nil {
				t.Errorf("expected error for %q, got %v", tc.expr, got)
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
	// A record prints with its field values, dropping the <>.
	got, err := evalExpr(`(kons 1 2)`, ev, env)
	if err != nil {
		t.Fatalf("kons: %v", err)
	}
	if s := PrintValue(got); s != "#<pare x: 1 y: 2>" {
		t.Errorf("record printed as %q", s)
	}
}
