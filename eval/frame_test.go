package eval

import (
	"testing"
)

const churn = `(define (churn-1 a b c) (+ a (+ b c)))
(define (churn-n n) (if (= n 0) 0 (+ (churn-1 n n n) (churn-n (- n 1)))))
(churn-n 50)`

func evalAllString(t *testing.T, ev *Evaluator, src string) Value {
	t.Helper()
	exprs, err := ParseAll(src)
	if err != nil {
		t.Fatalf("parse %q: %v", src, err)
	}
	v, err := ev.EvalAll(exprs, ev.globalEnv)
	if err != nil {
		t.Fatalf("eval %q: %v", src, err)
	}
	v, err = Materialize(v)
	if err != nil {
		t.Fatalf("materialize %q: %v", src, err)
	}
	return v
}

func TestFramePoolClosureCapture(t *testing.T) {
	ev := NewEvaluator()
	evalAllString(t, ev, `
		(define (make-counter start)
		  (lambda () (set! start (+ start 1)) start))
		(define c1 (make-counter 0))
		(define c2 (make-counter 100))
		(c1) (c1)`)
	evalAllString(t, ev, churn)
	if got := evalAllString(t, ev, `(c1)`); !deepEqual(got, Integer(3)) {
		t.Fatalf("c1 after churn: got %v, want 3", got)
	}
	if got := evalAllString(t, ev, `(c2)`); !deepEqual(got, Integer(101)) {
		t.Fatalf("c2 after churn: got %v, want 101", got)
	}
}

func TestFramePoolLetEscape(t *testing.T) {
	ev := NewEvaluator()
	evalAllString(t, ev, `
		(define (make-getter x)
		  (let ((doubled (* x 2)))
		    (lambda () (list x doubled))))
		(define g (make-getter 21))`)
	evalAllString(t, ev, churn)
	if got := evalAllString(t, ev, `(equal? (g) '(21 42))`); got != true {
		t.Fatalf("closure over let frame corrupted after churn: (g) != (21 42)")
	}
}

func TestFramePoolInternalDefine(t *testing.T) {
	ev := NewEvaluator()
	got := evalAllString(t, ev, `
		(define (f n)
		  (define t (* n 10))
		  (if (= n 0) 0 (+ t (f (- n 1)))))
		(f 4)`)
	if !deepEqual(got, Integer(100)) {
		t.Fatalf("recursive internal define: got %v, want 100", got)
	}
}

func TestFramePoolDelay(t *testing.T) {
	ev := NewEvaluator()
	evalAllString(t, ev, `
		(define (defer x) (delay (* x 3)))
		(define p (defer 14))`)
	evalAllString(t, ev, churn)
	if got := evalAllString(t, ev, `(force p)`); !deepEqual(got, Integer(42)) {
		t.Fatalf("promise over recycled frame: got %v, want 42", got)
	}
}

func TestFramePoolWhereStream(t *testing.T) {
	ev := NewEvaluator()
	evalAllString(t, ev, `
		(define (big-lines threshold)
		  (where (lambda (line) (> (string-length line) threshold))
		         (lines "a\nlonger line\nb\nanother long line")))
		(define s (big-lines 5))`)
	evalAllString(t, ev, churn)
	got := evalAllString(t, ev, `(length s)`)
	if !deepEqual(got, Integer(2)) {
		t.Fatalf("where stream over recycled frame: got %v, want 2", got)
	}
}

func TestFramePoolNamedLetLoop(t *testing.T) {
	ev := NewEvaluator()
	got := evalAllString(t, ev, `
		(let loop ((i 0) (acc 0))
		  (if (= i 1000) acc (loop (+ i 1) (+ acc i))))`)
	if !deepEqual(got, Integer(499500)) {
		t.Fatalf("named let: got %v, want 499500", got)
	}
}

func TestFramePoolVariadic(t *testing.T) {
	ev := NewEvaluator()
	got := evalAllString(t, ev, `
		(define (v a . rest) (cons a rest))
		(equal? (v 1 2 3) '(1 2 3))`)
	if got != true {
		t.Fatalf("variadic through pooled frames failed")
	}
	// Arity error message unchanged by the callLambda fast path.
	_, err := evalExpr(`((lambda (x) x) 1 2)`, ev, ev.globalEnv)
	if err == nil || err.Error() != "lambda: expected 1 arguments, got 2" {
		t.Fatalf("arity error: got %v", err)
	}
}

func TestSmallIntTable(t *testing.T) {
	runStdlibTests(t, []struct {
		expr      string
		expect    Value
		shouldErr bool
	}{
		// Around the pre-boxed table's edges, both signs.
		{`(- 0 1024)`, Integer(-1024), false},
		{`(- 0 1025)`, Integer(-1025), false},
		{`(+ 1000 24)`, Integer(1024), false},
		{`(+ 1000 25)`, Integer(1025), false},
		{`(* 2 512)`, Integer(1024), false},
		{`(quotient 2048 2)`, Integer(1024), false},
		{`(= (- 5 5) 0)`, true, false},
		{`(equal? (+ 500 12) 512)`, true, false},
	})
}
