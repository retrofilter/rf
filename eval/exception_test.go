package eval

import (
	"errors"
	"strings"
	"testing"
)

func TestGuard(t *testing.T) {
	runStdlibTests(t, []struct {
		expr      string
		expect    Value
		shouldErr bool
	}{
		{`(guard (e (true 'caught)) (raise 'boom))`, Symbol("caught"), false},
		{`(guard (e (true e)) (raise 42))`, Integer(42), false},
		// No raise: the body's value.
		{`(guard (e (true 'caught)) 'fine)`, Symbol("fine"), false},
		{`(guard (outer (true (list 'outer outer)))
		    (guard (e ((symbol? e) 'sym)) (raise 7)))`,
			listFromSlice([]Value{Symbol("outer"), Integer(7)}), false},
		{`(guard (e ((symbol? e) 'sym) ((number? e) 'num)) (raise 7))`, Symbol("num"), false},
		{`(guard (e ((+ e 1) => (lambda (x) (* x 2))) (else 'no)) (raise 20))`, Integer(42), false},
		{`(guard (e ((+ e 1))) (raise 20))`, Integer(21), false},
		// else with a body.
		{`(guard (e ((symbol? e) 'sym) (else 'other)) (raise 9))`, Symbol("other"), false},
		// A builtin failure is catchable and arrives wrapped.
		{`(guard (e (true (error-object? e))) (car '()))`, true, false},
		{`(guard (e (true 'caught)) (unbound-name-xyz))`, Symbol("caught"), false},
		// The condition variable scopes to the clauses only.
		{`(begin (guard (gv (true 'ok)) (raise 1))
		         (guard (e (true 'unbound-ok)) gv))`, Symbol("unbound-ok"), false},
		// error objects: message and irritants.
		{`(guard (e (true (error-object-message e))) (error "BOOM!" 1 2 3))`, String("BOOM!"), false},
		{`(guard (e (true (error-object-irritants e))) (error "BOOM!" 1 2 3))`,
			listFromSlice([]Value{Integer(1), Integer(2), Integer(3)}), false},
		{`(error-object? 'error)`, false, false},
		{`(guard (e (true (file-error? e))) (file "/nonexistent-rf-test-xyz"))`, true, false},
		{`(guard (e (true (file-error? e))) (error "BOOM!"))`, false, false},
		{`(guard (e (true (read-error? e))) (error "BOOM!"))`, false, false},
		// A continuation escaping through a guard is not a condition.
		{`(call/cc (lambda (k) (guard (e (true 'caught)) (k 'escaped))))`, Symbol("escaped"), false},
		{`(guard)`, nil, true},
		{`(guard (e) (raise 1))`, nil, true},
	})
}

func TestWithExceptionHandler(t *testing.T) {
	runStdlibTests(t, []struct {
		expr      string
		expect    Value
		shouldErr bool
	}{
		{`(with-exception-handler
		    (lambda (con) 42)
		    (lambda () (+ (raise-continuable "should be a number") 23)))`, Integer(65), false},
		// The handler escaping via a continuation (SRFI-34 pattern).
		{`(begin
		    (define (handled v)
		      (call/cc (lambda (k)
		        (with-exception-handler
		          (lambda (x) (k (list 'condition x)))
		          (lambda () (+ 1 (if (> v 0) (+ v 100) (raise 'an-error))))))))
		    (handled 5))`, Integer(106), false},
		{`(handled -1)`, listFromSlice([]Value{Symbol("condition"), Symbol("an-error")}), false},
		{`(begin (define handler-ran false)
		         (guard (e (true 'outer-caught))
		           (with-exception-handler
		             (lambda (x) (set! handler-ran true) 'ignored)
		             (lambda () (raise 'an-error)))))`, Symbol("outer-caught"), false},
		{`handler-ran`, true, false},
		{`(begin (define reached '())
		    (call/cc (lambda (k)
		      (with-exception-handler
		        (lambda (x) (set! reached (cons x reached)) (k 'handler))
		        (lambda ()
		          (guard (c ((> c 0) 'positive)) (raise 1)))))))`, Symbol("positive"), false},
		{`reached`, listFromSlice([]Value{}), false},
		{`(call/cc (lambda (k)
		    (with-exception-handler
		      (lambda (x) (k (list 'reraised x)))
		      (lambda ()
		        (guard (c ((> c 0) 'positive)) (raise 0))))))`,
			listFromSlice([]Value{Symbol("reraised"), Integer(0)}), false},
		// Handlers run for wrapped builtin failures too.
		{`(call/cc (lambda (k)
		    (with-exception-handler
		      (lambda (x) (k (error-object? x)))
		      (lambda () (car '())))))`, true, false},
		// raise-continuable with no handler is an ordinary error.
		{`(raise-continuable 'nobody-home)`, nil, true},
		{`(with-exception-handler (lambda (e) e))`, nil, true},
		{`(with-exception-handler 1 (lambda () 2))`, nil, true},
	})
}

func TestUncaughtConditions(t *testing.T) {
	ev := NewEvaluator()
	env := ev.globalEnv

	_, err := evalExpr(`(error "boom" 1 "two")`, ev, env)
	if err == nil || !strings.Contains(err.Error(), `boom 1 "two"`) {
		t.Errorf("uncaught error should carry message and irritants, got %v", err)
	}
	_, err = evalExpr(`(raise 'some-symbol)`, ev, env)
	if err == nil || !strings.Contains(err.Error(), "some-symbol") {
		t.Errorf("uncaught raise should name its payload, got %v", err)
	}
	_, err = evalExpr(`(syntax-error "bad form" (x y))`, ev, env)
	if err == nil || !strings.Contains(err.Error(), "bad form") {
		t.Errorf("syntax-error should error with its message, got %v", err)
	}

	// guard must not catch a pending interrupt.
	env.SetBuiltin("interrupt-now!", "test: set the interrupt flag", BuiltinFunc(func(args []Value, env *Environment) (Value, error) {
		Interrupt()
		return nil, nil
	}))
	defer ClearInterrupt()
	_, err = evalExpr(`(guard (e (true 'caught)) (interrupt-now!) (+ 1 2))`, ev, env)
	if !errors.Is(err, ErrInterrupted) {
		t.Errorf("guard must not catch interrupts, got %v", err)
	}
	ClearInterrupt()

	if len(ev.handlers) != 0 {
		t.Errorf("handler stack should be empty after unwinds, has %d entries", len(ev.handlers))
	}
}

func TestExceptionsAcrossBuiltinCalls(t *testing.T) {
	runStdlibTests(t, []struct {
		expr      string
		expect    Value
		shouldErr bool
	}{
		{`(map (lambda (x) (guard (e (true 'caught)) (if (= x 2) (raise 'two) x))) '(1 2 3))`,
			listFromSlice([]Value{Integer(1), Symbol("caught"), Integer(3)}), false},
		{`(car (map (lambda (x)
		       (with-exception-handler
		         (lambda (c) (* c 10))
		         (lambda () (+ 1 (raise-continuable x)))))
		     '(5)))`, Integer(51), false},
	})
}
