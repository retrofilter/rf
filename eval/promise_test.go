package eval

import (
	"strings"
	"testing"
)

func TestPromises(t *testing.T) {
	runStdlibTests(t, []struct {
		expr      string
		expect    Value
		shouldErr bool
	}{
		{`(force (delay (+ 1 2)))`, Integer(3), false},
		// Memoized: the computation runs once.
		{`(let ((n 0)) (let ((p (delay (begin (set! n (+ n 1)) n)))) (force p) (force p)))`, Integer(1), false},
		{`(promise? (delay 1))`, true, false},
		{`(promise? 1)`, false, false},
		{`(force (make-promise 4))`, Integer(4), false},
		{`(force (make-promise (make-promise 4)))`, Integer(4), false},
		{`(force 5)`, Integer(5), false},
		// make-promise of a promise is that promise, not a wrapper.
		{`(let ((p (delay 1))) (eq? p (make-promise p)))`, true, false},
		// The body must not run at delay time.
		{`(let ((n 0)) (delay (set! n 99)) n)`, Integer(0), false},
	})

	if !strings.Contains(PrintValue(&Promise{}), "promise") {
		t.Errorf("promise print missing")
	}

	ev := NewEvaluator()
	env := ev.globalEnv
	forms, err := ParseAll(`
		(define (countdown n)
		  (delay-force (if (= n 0) (delay 'done) (countdown (- n 1)))))
		(force (countdown 100000))`)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	got, err := ev.EvalAll(forms, env)
	if err != nil {
		t.Fatalf("delay-force loop: %v", err)
	}
	if !deepEqual(got, Symbol("done")) {
		t.Errorf("delay-force loop: got %v", got)
	}
}
