package eval

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func TestCompiledGuard(t *testing.T) {
	runStdlibTests(t, []struct {
		expr      string
		expect    Value
		shouldErr bool
	}{
		// Clause shapes: (test), (test => proc), (test body...), else.
		{`(guard (e ((symbol? e))) (raise 'sym))`, true, false},
		{`(guard (e ((assq 'a e) => cdr) ((assq 'b e))) (raise '((a . 42))))`, Integer(42), false},
		{`(guard (e ((assq 'a e) => cdr) ((assq 'b e))) (raise '((b . 23))))`,
			&Pair{Car: Symbol("b"), Cdr: Integer(23)}, false},
		{`(guard (e ((string? e) 'string) (else (list 'other e))) (raise 7))`,
			listFromSlice([]Value{Symbol("other"), Integer(7)}), false},
		// No clause matched: the original condition re-raises to an outer guard.
		{`(guard (outer (#t (list 'outer outer)))
		    (guard (inner ((string? inner) 'no)) (raise 'x)))`,
			listFromSlice([]Value{Symbol("outer"), Symbol("x")}), false},
		{`(let ((y 1)) (guard (e (#t 'no)) (define x (+ y 1)) (* x 10)))`, Integer(20), false},
		{`(guard (e (#t (define doubled (* e 2)) doubled)) (raise 21))`, Integer(42), false},
		{`(let ((e 'outer)) (guard (e (#t e)) (list e (raise 'inner))))`, Symbol("inner"), false},
		// Malformed clauses are compile-time errors.
		{`(guard (e (else)) 1)`, nil, true},
		{`(guard (e (#t => car cdr)) 1)`, nil, true},
		{`(guard (e ()) 1)`, nil, true},
	})
}

func TestCompiledGuardTailClause(t *testing.T) {
	ev := NewEvaluator()
	got := evalAllString(t, ev, `
		(define (down n)
		  (guard (e (#t (if (= n 0) 'done (down (- n 1)))))
		    (raise 'again)))
		(down 200000)`)
	if !deepEqual(got, Symbol("done")) {
		t.Fatalf("guard tail clause: got %v, want done", got)
	}
}

func TestCompiledGuardHandlerStack(t *testing.T) {
	ev := NewEvaluator()
	got := evalAllString(t, ev, `
		(define log '())
		(call/cc (lambda (k)
		  (with-exception-handler
		    (lambda (c) (set! log (cons (list 'handler c) log)) (k 'escaped))
		    (lambda ()
		      (guard (e ((number? e) (set! log (cons 'caught log)) (raise 'rethrown)))
		        (raise 1))))))
		(reverse log)`)
	want := listFromSlice([]Value{Symbol("caught"), listFromSlice([]Value{Symbol("handler"), Symbol("rethrown")})})
	if !deepEqual(got, want) {
		t.Fatalf("handler stack after guard: got %v, want %v", PrintValue(got), PrintValue(want))
	}
}

func TestCompiledParameterize(t *testing.T) {
	runStdlibTests(t, []struct {
		expr      string
		expect    Value
		shouldErr bool
	}{
		{`(begin (define p (make-parameter 10 (lambda (x) (* x 2)))) (p))`, Integer(20), false},
		{`(parameterize ((p 5)) (p))`, Integer(10), false},
		// Restored after a normal exit and after an unwind.
		{`(p)`, Integer(20), false},
		{`(guard (e (#t (p))) (parameterize ((p 1)) (raise 'x)))`, Integer(20), false},
		// The value expressions see the outer binding (simultaneous).
		{`(parameterize ((p (+ (p) 1))) (p))`, Integer(42), false},
		// Five bindings spill the stack buffers.
		{`(let ((a (make-parameter 1)) (b (make-parameter 2)) (c (make-parameter 3))
		        (d (make-parameter 4)) (f (make-parameter 5)))
		    (parameterize ((a 10) (b 20) (c 30) (d 40) (f 50)) (+ (a) (b) (c) (d) (f))))`, Integer(150), false},
		{`(parameterize ((car 1)) 1)`, nil, true},
		{`(parameterize (p) 1)`, nil, true},
	})
}

func TestCompiledParameterizeError(t *testing.T) {
	ev := NewEvaluator()
	_, err := evalAll(t, ev, `(parameterize ((car 1)) 1)`)
	if err == nil || !strings.Contains(err.Error(), "car is not a parameter object") {
		t.Fatalf("parameterize non-parameter: got %v", err)
	}
}

func TestCompiledLetValuesScoping(t *testing.T) {
	runStdlibTests(t, []struct {
		expr      string
		expect    Value
		shouldErr bool
	}{
		{`(let ((x 10)) (let*-values (((y) (values x)) ((x) (values 5))) (list x y)))`,
			listFromSlice([]Value{Integer(5), Integer(10)}), false},
		// Rest formals at every position, and a symbol formal.
		{`(let-values (((a . r) (values 1 2 3)) (all (values 4 5)) (() (values)))
		    (list a r all))`,
			listFromSlice([]Value{Integer(1), listFromSlice([]Value{Integer(2), Integer(3)}),
				listFromSlice([]Value{Integer(4), Integer(5)})}), false},
		// Internal defines in the body sit beside the formals' slots.
		{`(let-values (((a b) (values 1 2))) (define c (+ a b)) (define (d) (* c 2)) (d))`, Integer(6), false},
		// A duplicate variable across clauses is an error (§4.2.2).
		{`(let-values (((x) (values 1)) ((x) (values 2))) x)`, nil, true},
		// A closure escaping the body keeps its frame.
		{`(begin
		    (define (mk) (let-values (((a b) (values 1 2))) (lambda () (+ a b))))
		    (define lv-f (mk))
		    ` + churn + `
		    (lv-f))`, Integer(3), false},
		// A tail call from the body: constant stack.
		{`(begin
		    (define (lv-loop n acc)
		      (let-values (((m a) (values (- n 1) (+ acc 1))))
		        (if (= m 0) a (lv-loop m a))))
		    (lv-loop 100000 0))`, Integer(100000), false},
		{`(let*-values (((a) (values 1)) ((b) (values (+ a 1))) ((c) (values (+ b 1)))) (list a b c))`,
			listFromSlice([]Value{Integer(1), Integer(2), Integer(3)}), false},
	})
}

func TestCompiledDefineValuesInBody(t *testing.T) {
	runStdlibTests(t, []struct {
		expr      string
		expect    Value
		shouldErr bool
	}{
		{`(begin
		    (define (f)
		      (define (sum) (+ a b))
		      (define-values (a b) (values 1 2))
		      (sum))
		    (f))`, Integer(3), false},
		{`(begin (define (g) (define-values (x . r) (values 1 2 3)) (list x r)) (g))`,
			listFromSlice([]Value{Integer(1), listFromSlice([]Value{Integer(2), Integer(3)})}), false},
		{`(begin (define (h) (define-values (a b) (values 1)) a) (h))`, nil, true},
	})
}

func TestCompiledDefineRecordType(t *testing.T) {
	runStdlibTests(t, []struct {
		expr      string
		expect    Value
		shouldErr bool
	}{
		// Each evaluation of the definition creates a disjoint type.
		{`(begin
		    (define (mk-type) (define-record-type <p> (mk x) p? (x p-x set-p-x!)) (list mk p? p-x))
		    (define t1 (mk-type))
		    (define t2 (mk-type))
		    (list ((cadr t1) ((car t1) 1)) ((cadr t2) ((car t1) 1))))`,
			listFromSlice([]Value{true, false}), false},
		// Internal definition with a forward reference from a sibling define.
		{`(begin
		    (define (f)
		      (define (build) (mk 7))
		      (define-record-type <pt> (mk x) pt? (x pt-x))
		      (pt-x (build)))
		    (f))`, Integer(7), false},
		// Malformed specs are compile-time errors with the old messages.
		{`(define-record-type <p> (mk y) p? (x p-x))`, nil, true},
		{`(define-record-type <p> mk p? (x p-x))`, nil, true},
	})
}

func TestCompiledDefineRecordTypeErrorText(t *testing.T) {
	ev := NewEvaluator()
	_, err := evalAll(t, ev, `(define-record-type <p> (mk y) p? (x p-x))`)
	if err == nil || !strings.Contains(err.Error(), "constructor field y is not a declared field") {
		t.Fatalf("record constructor field error: got %v", err)
	}
}

func TestCompiledCaseLambdaCapture(t *testing.T) {
	ev := NewEvaluator()
	evalAllString(t, ev, `
		(define (make-acc start)
		  (case-lambda
		    (() start)
		    ((n) (set! start (+ start n)) start)))
		(define a1 (make-acc 0))
		(define a2 (make-acc 100))
		(a1 5)`)
	evalAllString(t, ev, churn)
	if got := evalAllString(t, ev, `(list (a1) (a2 1))`); !deepEqual(got, listFromSlice([]Value{Integer(5), Integer(101)})) {
		t.Fatalf("case-lambda frames after churn: got %v", PrintValue(got))
	}
	if _, err := evalAll(t, ev, `(a1 1 2)`); err == nil || !strings.Contains(err.Error(), "no clause matches 2 arguments") {
		t.Fatalf("case-lambda arity error: got %v", err)
	}
	if _, err := evalAll(t, ev, `(case-lambda (x))`); err == nil {
		t.Fatal("case-lambda with a malformed clause compiled")
	}
}

func TestCompiledDelayCapture(t *testing.T) {
	ev := NewEvaluator()
	evalAllString(t, ev, `
		(define (mk n) (delay (* n n)))
		(define ps (map mk '(1 2 3)))
		(define (chain n) (delay-force (if (= n 0) (delay 'bottom) (chain (- n 1)))))
		(define deep (chain 100000))`)
	evalAllString(t, ev, churn)
	if got := evalAllString(t, ev, `(map force ps)`); !deepEqual(got, listFromSlice([]Value{Integer(1), Integer(4), Integer(9)})) {
		t.Fatalf("promises over recycled frames: got %v", PrintValue(got))
	}
	// delay-force chains force iteratively (SRFI-45), in constant stack.
	if got := evalAllString(t, ev, `(force deep)`); !deepEqual(got, Symbol("bottom")) {
		t.Fatalf("delay-force chain: got %v", PrintValue(got))
	}
	if _, err := evalAll(t, ev, `(delay 1 2)`); err == nil {
		t.Fatal("(delay 1 2) compiled")
	}
}

func TestCompiledLetSyntax(t *testing.T) {
	runStdlibTests(t, []struct {
		expr      string
		expect    Value
		shouldErr bool
	}{
		// A template referencing an enclosing local resolves to its slot.
		{`((lambda (x) (let-syntax ((get-x (syntax-rules () ((_) x)))) (get-x))) 42)`, Integer(42), false},
		{`(letrec-syntax ((my-or (syntax-rules () ((_) #f) ((_ e) e)
		                                          ((_ e r ...) (let ((t e)) (if t t (my-or r ...))))))
		                 (any-of (syntax-rules () ((_ x ...) (my-or x ...)))))
		    (list (any-of #f #f 3) (any-of) (my-or #f)))`,
			listFromSlice([]Value{Integer(3), false, false}), false},
		// The body is its own scope: defines don't leak (§4.3.1).
		{`(begin (define ls-x 1) (let-syntax () (define ls-x 2) ls-x) ls-x)`, Integer(1), false},
		// The macro is scoped to the body.
		{`(begin (let-syntax ((only-here (syntax-rules () ((_) 1)))) (only-here)) (only-here))`, nil, true},
		// A transformer bound to a variable, used at top level and inside a body.
		{`(define my-double (syntax-rules () ((_ x) (* 2 x))))`, Symbol("my-double"), false},
		{`(let-syntax ((dbl my-double)) (dbl 21))`, Integer(42), false},
		{`((lambda () (let-syntax ((dbl my-double)) (dbl 5))))`, Integer(10), false},
		{`(let-syntax ((m car)) 1)`, nil, true},
		{`(let-syntax (m) 1)`, nil, true},
		{`(let ((tmp 'user))
		    (let-syntax ((swap-tmp (syntax-rules () ((_ e) (let ((tmp 'macro)) e)))))
		      (swap-tmp tmp)))`, Symbol("user"), false},
	})
}

func TestCompiledDefiningFormsGrantGuard(t *testing.T) {
	ev := NewEvaluator()
	ev.SetCaller(CallerAssistant)
	defer ev.SetCaller(CallerUser)
	for _, src := range []string{
		`(define-values (agent-allow-commands) (values '("rm")))`,
		`(define-values (x . agent-allow-commands) (values 1 "rm"))`,
		`(define-record-type agent-allow-working-dir (mk x) p? (x p-x))`,
		`(define-record-type <p> (agent-allow-commands x) p? (x p-x))`,
	} {
		_, err := evalAll(t, ev, src)
		if err == nil || !strings.Contains(err.Error(), "can only be bound by the user") {
			t.Errorf("%s as the assistant: got %v, want the grant guard", src, err)
		}
	}
}

const runawayMacros = `
	(define-syntax rw-ev? (syntax-rules () ((_ n) (if (= n 0) #t (rw-od? (- n 1))))))
	(define-syntax rw-od? (syntax-rules () ((_ n) (if (= n 0) #f (rw-ev? (- n 1))))))`

func TestRunawayMacroExpansionInterrupt(t *testing.T) {
	ClearInterrupt()
	defer ClearInterrupt()
	ev := NewEvaluator()
	evalAllString(t, ev, runawayMacros)

	done := make(chan error, 1)
	go func() {
		_, err := evalAll(t, ev, `(rw-ev? 4)`)
		done <- err
	}()
	start := time.Now()
	time.AfterFunc(50*time.Millisecond, Interrupt)

	select {
	case err := <-done:
		if !errors.Is(err, ErrInterrupted) {
			t.Fatalf("expected ErrInterrupted, got %v", err)
		}
		if d := time.Since(start); d > time.Second {
			t.Fatalf("interrupt took %v to land", d)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("interrupt did not stop the runaway expansion")
	}
}

func TestRunawayMacroExpansionCap(t *testing.T) {
	ClearInterrupt()
	defer ClearInterrupt()
	ev := NewEvaluator()
	evalAllString(t, ev, runawayMacros)
	_, err := evalAll(t, ev, `(rw-od? 7)`)
	if err == nil || !strings.Contains(err.Error(), "macro expansion exceeded") {
		t.Fatalf("expected the expansion cap, got %v", err)
	}
}
