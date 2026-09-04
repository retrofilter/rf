package eval

import (
	"errors"
	"testing"
)

func TestCallCC(t *testing.T) {
	runStdlibTests(t, []struct {
		expr      string
		expect    Value
		shouldErr bool
	}{
		// Normal return: the receiver's value is call/cc's value.
		{`(call/cc (lambda (k) 42))`, Integer(42), false},
		{`(call-with-current-continuation (lambda (k) 42))`, Integer(42), false},
		// Escape from within nested calls (the for-each pattern).
		{`(call/cc (lambda (exit)
		     (for-each (lambda (x) (if (< x 0) (exit x))) '(54 0 37 -3 245 19))
		     true))`, Integer(-3), false},
		// Escape from deep recursion.
		{`(call/cc (lambda (k)
		     (define (loop n) (if (= n 0) (k 'done) (loop (- n 1))))
		     (loop 10000)))`, Symbol("done"), false},
		{`(call/cc (lambda (k1)
		     (+ 100 (call/cc (lambda (k2) (k1 7))))))`, Integer(7), false},
		// A continuation is a procedure.
		{`(call/cc procedure?)`, true, false},
		// Escaping restores nothing it shouldn't: value flows out.
		{`(+ 1 (call/cc (lambda (k) (k 1) 99)))`, Integer(2), false},
		// Invoking after the extent has exited errors (D4 escape-only).
		{`(begin (define saved-k false)
		         (call/cc (lambda (k) (set! saved-k k)))
		         (saved-k 1))`, nil, true},
		{`(call/cc)`, nil, true},
		{`(call/cc 42)`, nil, true},
	})
}

func TestDynamicWind(t *testing.T) {
	runStdlibTests(t, []struct {
		expr      string
		expect    Value
		shouldErr bool
	}{
		// before → thunk → after, thunk's value returned.
		{`(begin (define dw-log '())
		         (define (add s) (set! dw-log (cons s dw-log)))
		         (dynamic-wind (lambda () (add 'before))
		                       (lambda () (add 'during) 'result)
		                       (lambda () (add 'after))))`, Symbol("result"), false},
		{`(reverse dw-log)`, listFromSlice([]Value{Symbol("before"), Symbol("during"), Symbol("after")}), false},
		// after runs when the thunk raises; the error still propagates.
		{`(begin (set! dw-log '())
		         (guard (e (true (reverse dw-log)))
		           (dynamic-wind (lambda () (add 'in))
		                         (lambda () (raise 'boom))
		                         (lambda () (add 'out)))))`,
			listFromSlice([]Value{Symbol("in"), Symbol("out")}), false},
		// after runs when a continuation escapes through the extent.
		{`(begin (set! dw-log '())
		         (call/cc (lambda (k)
		           (dynamic-wind (lambda () (add 'in))
		                         (lambda () (k 'escaped))
		                         (lambda () (add 'out)))))
		         (reverse dw-log))`,
			listFromSlice([]Value{Symbol("in"), Symbol("out")}), false},
		// A failing before-thunk skips both thunk and after.
		{`(begin (set! dw-log '())
		         (guard (e (true (reverse dw-log)))
		           (dynamic-wind (lambda () (raise 'pre))
		                         (lambda () (add 'during))
		                         (lambda () (add 'out)))))`,
			listFromSlice([]Value{}), false},
		{`(dynamic-wind (lambda () 1) (lambda () 2))`, nil, true},
	})
}

func TestDynamicWindInterrupt(t *testing.T) {
	ev := NewEvaluator()
	env := ev.globalEnv
	env.SetBuiltin("interrupt-now!", "test: set the interrupt flag", BuiltinFunc(func(args []Value, env *Environment) (Value, error) {
		Interrupt()
		return nil, nil
	}))
	defer ClearInterrupt()

	if _, err := evalExpr(`(define after-ran false)`, ev, env); err != nil {
		t.Fatal(err)
	}
	_, err := evalExpr(`(dynamic-wind (lambda () 1)
	                                  (lambda () (interrupt-now!) (+ 1 2))
	                                  (lambda () (set! after-ran true)))`, ev, env)
	if !errors.Is(err, ErrInterrupted) {
		t.Fatalf("expected ErrInterrupted, got %v", err)
	}
	if !Interrupted() {
		t.Fatal("interrupt flag should still be set after the unwind")
	}
	ClearInterrupt()
	v, err := evalExpr(`after-ran`, ev, env)
	if err != nil || v != true {
		t.Fatalf("after thunk should have run during the interrupt unwind: %v %v", v, err)
	}
}

func TestParameterize(t *testing.T) {
	runStdlibTests(t, []struct {
		expr      string
		expect    Value
		shouldErr bool
	}{
		{`(begin (define p (make-parameter 10)) (p))`, Integer(10), false},
		{`(parameterize ((p 20)) (p))`, Integer(20), false},
		{`(p)`, Integer(10), false},
		// Nested extents restore in order.
		{`(parameterize ((p 1)) (list (p) (parameterize ((p 2)) (p)) (p)))`,
			listFromSlice([]Value{Integer(1), Integer(2), Integer(1)}), false},
		// The converter runs on the initial value and on parameterize.
		{`(begin (define q (make-parameter 5 (lambda (x) (* x 2)))) (q))`, Integer(10), false},
		{`(parameterize ((q 7)) (q))`, Integer(14), false},
		// A converter rejecting the value aborts before anything is pushed.
		{`(begin (define r (make-parameter 1 (lambda (x) (if (< x 0) (error "bad") x))))
		         (parameterize ((r -1)) (r)))`, nil, true},
		{`(r)`, Integer(1), false},
		// The stack unwinds even when the body raises.
		{`(begin (guard (e (true 'ok)) (parameterize ((p 99)) (raise 'x))) (p))`, Integer(10), false},
		{`(parameterize ((p 3)) (car (map (lambda (x) (+ x (p))) '(1))))`, Integer(4), false},
		{`(parameterize ((+ 1)) 2)`, nil, true},
		{`(p 5)`, nil, true},
		{`(procedure? p)`, true, false},
	})
}

func TestCaseLambda(t *testing.T) {
	runStdlibTests(t, []struct {
		expr      string
		expect    Value
		shouldErr bool
	}{
		{`(begin (define arity (case-lambda
		           (() 'zero)
		           ((x) x)
		           ((x y) (list x y))
		           (args (cons 'many args))))
		         (arity))`, Symbol("zero"), false},
		{`(arity 1)`, Integer(1), false},
		{`(arity 1 2)`, listFromSlice([]Value{Integer(1), Integer(2)}), false},
		{`(arity 1 2 3)`, listFromSlice([]Value{Symbol("many"), Integer(1), Integer(2), Integer(3)}), false},
		// Clauses match in order: an early rest clause shadows later ones.
		{`(begin (define dead (case-lambda ((x . y) 'many) (() 'none) (foo 'unreachable)))
		         (list (dead) (dead 1) (dead 1 2)))`,
			listFromSlice([]Value{Symbol("none"), Symbol("many"), Symbol("many")}), false},
		// No matching clause errors.
		{`(begin (define one-only (case-lambda ((x) x))) (one-only 1 2))`, nil, true},
		// case-lambda procedures work with apply and procedure?.
		{`(apply arity '(7))`, Integer(7), false},
		{`(procedure? arity)`, true, false},
		// Tail calls in clause bodies keep constant stack.
		{`(begin (define count (case-lambda
		           ((n) (count n 0))
		           ((n acc) (if (= n 0) acc (count (- n 1) (+ acc 1))))))
		         (count 100000))`, Integer(100000), false},
		{`(case-lambda)`, nil, true},
		{`(case-lambda 42)`, nil, true},
	})
}

func TestLetValuesForms(t *testing.T) {
	runStdlibTests(t, []struct {
		expr      string
		expect    Value
		shouldErr bool
	}{
		{`(let-values (((a b) (values 1 2))) (+ a b))`, Integer(3), false},
		{`(let-values () 'ok)`, Symbol("ok"), false},
		// Simultaneous: inits see the outer bindings.
		{`(let ((x 'x) (y 'y))
		    (let-values (((x y) (values 1 2)) ((p q) (values x y)))
		      (list p q)))`, listFromSlice([]Value{Symbol("x"), Symbol("y")}), false},
		// Sequential: let*-values inits see previous bindings.
		{`(let ((x 'x) (y 'y))
		    (let*-values (((x y) (values 1 2)) ((p q) (values x y)))
		      (list p q)))`, listFromSlice([]Value{Integer(1), Integer(2)}), false},
		// Rest formals.
		{`(let-values (((a . rest) (values 1 2 3))) (list a rest))`,
			listFromSlice([]Value{Integer(1), listFromSlice([]Value{Integer(2), Integer(3)})}), false},
		{`(let-values ((all (values 1 2))) all)`, listFromSlice([]Value{Integer(1), Integer(2)}), false},
		// The body is its own scope: internal defines don't leak.
		{`(begin (define lv-x 1) (let*-values () (define lv-x 2) false) lv-x)`, Integer(1), false},
		// Arity mismatches error.
		{`(let-values (((a b) (values 1))) a)`, nil, true},
		{`(let-values (((a b) 1)) a)`, nil, true},
		// define-values, including rest and zero-values forms.
		{`(begin (define-values (dv-a dv-b) (values 1 2)) (+ dv-a dv-b))`, Integer(3), false},
		{`(begin (define-values (dv-c . dv-r) (values 1 2 3)) (list dv-c dv-r))`,
			listFromSlice([]Value{Integer(1), listFromSlice([]Value{Integer(2), Integer(3)})}), false},
		{`(begin (define-values () (values)) 'ok)`, Symbol("ok"), false},
		{`(define-values (a b) (values 1))`, nil, true},
		// letrec* (same expansion as letrec: sequential assignment).
		{`(letrec* ((p (lambda (x) (+ 1 (q (- x 1)))))
		            (q (lambda (y) (if (= y 0) 0 (+ 1 (p (- y 1))))))
		            (x (p 5)) (y x))
		   y)`, Integer(5), false},
	})
}

func TestCaseArrowAndHygiene(t *testing.T) {
	runStdlibTests(t, []struct {
		expr      string
		expect    Value
		shouldErr bool
	}{
		{`(case 2 ((1 2 3) => (lambda (x) (* x 10))) (else 'no))`, Integer(20), false},
		{`(case 9 ((1 2 3) 'small) (else => (lambda (x) (* x 2))))`, Integer(18), false},
		{`(case (* 2 3) ((2 3 5 7) 'prime) ((1 4 6 8 9) 'composite))`, Symbol("composite"), false},
		// The expansion's temporary can't collide with user bindings.
		{`(let ((_case_tmp 'user) (_cond_tmp 'user2))
		    (case 1 ((1) (list _case_tmp _cond_tmp)) (else 'no)))`,
			listFromSlice([]Value{Symbol("user"), Symbol("user2")}), false},
		{`(case 1 ((1) => ) (else 'no))`, nil, true},
	})
}
