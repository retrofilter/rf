package eval

import (
	"strings"
	"testing"
)

func evalAll(t *testing.T, ev *Evaluator, src string) (Value, error) {
	t.Helper()
	exprs, err := ParseAll(src)
	if err != nil {
		t.Fatalf("parse %q: %v", src, err)
	}
	var last Value
	for _, e := range exprs {
		last, err = ev.Eval(e, ev.globalEnv)
		if err != nil {
			return nil, err
		}
	}
	return last, nil
}

const r7rsDerivedForms = `
(define-syntax my-when
  (syntax-rules ()
    ((my-when test result1 result2 ...)
     (if test (begin result1 result2 ...)))))

(define-syntax my-unless
  (syntax-rules ()
    ((my-unless test result1 result2 ...)
     (if (not test) (begin result1 result2 ...)))))

(define-syntax my-case
  (syntax-rules (else =>)
    ((my-case (key ...) clauses ...)
     (let ((atom-key (key ...))) (my-case atom-key clauses ...)))
    ((my-case key (else => result)) (result key))
    ((my-case key (else result1 result2 ...)) (begin result1 result2 ...))
    ((my-case key ((atoms ...) => result))
     (if (member key (quote (atoms ...))) (result key)))
    ((my-case key ((atoms ...) result1 result2 ...))
     (if (member key (quote (atoms ...))) (begin result1 result2 ...)))
    ((my-case key ((atoms ...) => result) clause clauses ...)
     (if (member key (quote (atoms ...)))
         (result key)
         (my-case key clause clauses ...)))
    ((my-case key ((atoms ...) result1 result2 ...) clause clauses ...)
     (if (member key (quote (atoms ...)))
         (begin result1 result2 ...)
         (my-case key clause clauses ...)))))

(define-syntax my-do
  (syntax-rules ()
    ((my-do ((var init step ...) ...) (test expr ...) command ...)
     (letrec ((loop (lambda (var ...)
                      (if test
                          (begin false expr ...)
                          (begin command ... (loop (my-do "step" var step ...) ...))))))
       (loop init ...)))
    ((my-do "step" x) x)
    ((my-do "step" x y) y)))
`

func TestSyntaxRulesValidation(t *testing.T) {
	ev := NewEvaluator()
	if _, err := evalAll(t, ev, r7rsDerivedForms); err != nil {
		t.Fatalf("defining derived forms: %v", err)
	}
	pairs := [][2]string{
		{`(when (> 3 2) 'a 'b)`, `(my-when (> 3 2) 'a 'b)`},
		{`(when (< 3 2) 'a)`, `(my-when (< 3 2) 'a)`},
		{`(unless (> 3 2) 'a 'b)`, `(my-unless (> 3 2) 'a 'b)`},
		{`(unless (< 3 2) 'a 'b)`, `(my-unless (< 3 2) 'a 'b)`},
		{`(case (* 2 3) ((2 3 5 7) 'prime) ((1 4 6 8 9) 'composite))`,
			`(my-case (* 2 3) ((2 3 5 7) 'prime) ((1 4 6 8 9) 'composite))`},
		{`(case (+ 3 4) ((2 3) 'low) (else 'high))`,
			`(my-case (+ 3 4) ((2 3) 'low) (else 'high))`},
		{`(case (+ 1 1) ((1 2 3) => (lambda (x) (* x 10))) (else 'no))`,
			`(my-case (+ 1 1) ((1 2 3) => (lambda (x) (* x 10))) (else 'no))`},
		{`(case (+ 4 5) ((1) 'one) (else => (lambda (x) (* x 2))))`,
			`(my-case (+ 4 5) ((1) 'one) (else => (lambda (x) (* x 2))))`},
		{`(do ((vec (make-vector 5)) (i 0 (+ i 1))) ((= i 5) vec) (vector-set! vec i i))`,
			`(my-do ((vec (make-vector 5)) (i 0 (+ i 1))) ((= i 5) vec) (vector-set! vec i i))`},
		{`(do ((x '(1 3 5 7 9) (cdr x)) (sum 0 (+ sum (car x)))) ((null? x) sum))`,
			`(my-do ((x '(1 3 5 7 9) (cdr x)) (sum 0 (+ sum (car x)))) ((null? x) sum))`},
	}
	for _, p := range pairs {
		goVal, err := evalAll(t, ev, p[0])
		if err != nil {
			t.Errorf("go macro %q: %v", p[0], err)
			continue
		}
		srVal, err := evalAll(t, ev, p[1])
		if err != nil {
			t.Errorf("syntax-rules %q: %v", p[1], err)
			continue
		}
		if !deepEqual(goVal, srVal) {
			t.Errorf("disagreement: %q → %s, but %q → %s",
				p[0], PrintValue(goVal), p[1], PrintValue(srVal))
		}
	}
}

func TestSyntaxRulesHygiene(t *testing.T) {
	runStdlibTests(t, []struct {
		expr      string
		expect    Value
		shouldErr bool
	}{
		{`(begin (define-syntax swap!
		           (syntax-rules () ((_ a b) (let ((tmp a)) (set! a b) (set! b tmp)))))
		         (let ((tmp 1) (other 2)) (swap! tmp other) (list tmp other)))`,
			listFromSlice([]Value{Integer(2), Integer(1)}), false},
		{`(let ((x 'outer))
		    (let-syntax ((m (syntax-rules () ((m) x))))
		      (let ((x 'inner)) (m))))`, Symbol("outer"), false},
		// Recursive expansion with shadowed keywords and temp names.
		{`(begin (define-syntax my-or2
		           (syntax-rules ()
		             ((_) false)
		             ((_ e) e)
		             ((_ e1 e2 ...) (let ((temp e1)) (if temp temp (my-or2 e2 ...))))))
		         (let ((x false) (y 7) (temp 8) (let odd?) (if even?))
		           (my-or2 x (let temp) (if y) y)))`, Integer(7), false},
		{`(begin (define-syntax jabber
		           (syntax-rules ()
		             ((_ hatter)
		              (begin (define march-hare 42)
		                     (define-syntax hatter
		                       (syntax-rules () ((_) march-hare)))))))
		         (jabber mad-hatter)
		         (mad-hatter))`, Integer(42), false},
		{`(begin (define-syntax q (syntax-rules () ((_) '(a b)))) (q))`,
			listFromSlice([]Value{Symbol("a"), Symbol("b")}), false},
		{`(eq? 'a (car (q)))`, true, false},
		// The (... ...) escape produces a literal ellipsis.
		{`(begin (define-syntax esc (syntax-rules () ((_ x) '(... (x ...))))) (esc 100))`,
			listFromSlice([]Value{Integer(100), Symbol("...")}), false},
		// Custom ellipsis identifier.
		{`(begin (define-syntax rev (syntax-rules dots () ((_ x dots) (list x dots)))) (rev 1 2 3))`,
			listFromSlice([]Value{Integer(1), Integer(2), Integer(3)}), false},
		{`(begin (define-syntax mid (syntax-rules () ((_ a (m n) ... x) (list a (list m ...) (list n ...) x))))
		         (mid 1 (2 3) (4 5) 9))`,
			listFromSlice([]Value{Integer(1),
				listFromSlice([]Value{Integer(2), Integer(4)}),
				listFromSlice([]Value{Integer(3), Integer(5)}),
				Integer(9)}), false},
		// _ is a wildcard in patterns, an ordinary symbol as a literal.
		{`(begin (define-syntax wild (syntax-rules () ((_ _ _) 'two) ((_ . _) 'other))) (wild a b))`,
			Symbol("two"), false},
		{`(let ((when (lambda (a b) (list a b)))) (when 1 2))`,
			listFromSlice([]Value{Integer(1), Integer(2)}), false},
		{`(begin (define my-global-when-shadow 1) (when true 'still-a-macro))`,
			Symbol("still-a-macro"), false},
		// Error surfaces: no matching rule, non-transformer.
		{`(begin (define-syntax one-arg (syntax-rules () ((_ x) x))) (one-arg 1 2))`, nil, true},
		{`(define-syntax bad 42)`, nil, true},
		{`(begin (define-syntax bad-tpl (syntax-rules () ((_ x) (list ...)))) (bad-tpl 1))`, nil, true},
	})
}

func TestMacroTailCalls(t *testing.T) {
	ev := NewEvaluator()
	if _, err := evalAll(t, ev, r7rsDerivedForms); err != nil {
		t.Fatal(err)
	}
	for _, src := range []string{
		// Go macro in tail position.
		`(begin (define (cd1 n) (when (> n 0) (cd1 (- n 1)))) (cd1 300000) 'ok)`,
		// syntax-rules macro in tail position.
		`(begin (define (cd2 n) (my-when (> n 0) (cd2 (- n 1)))) (cd2 300000) 'ok)`,
		// cond in tail position.
		`(begin (define (cd3 n) (cond ((> n 0) (cd3 (- n 1))) (else 'ok))) (cd3 300000))`,
		`(begin (define (nest n)
		          (if (= n 0) 'ok
		              (let ((a n)) (let ((b (- a 1))) (nest b)))))
		        (nest 50000))`,
		// The direct immediate-application shape of the same hazard.
		`(begin (define (imm n)
		          (if (= n 0) 'ok
		              ((lambda (x) ((lambda (y) (imm y)) (- x 1))) n)))
		        (imm 50000))`,
	} {
		v, err := evalAll(t, ev, src)
		if err != nil {
			t.Errorf("%q: %v", src, err)
		} else if v != Symbol("ok") {
			t.Errorf("%q: got %s", src, PrintValue(v))
		}
	}
}

func TestRenameMarks(t *testing.T) {
	name := markedName("tmp", 0, 7)
	orig, envID, ok := unmark(name)
	if !ok || orig != "tmp" || envID != 0 {
		t.Fatalf("unmark(%q) = %q %d %v", name, orig, envID, ok)
	}
	double := markedName(name, 3, 8)
	if symBase(double) != "tmp" {
		t.Errorf("symBase(%q) = %q", double, symBase(double))
	}
	if got := PrintValue(Symbol(double)); got != "tmp" {
		t.Errorf("marked symbol printed as %q", got)
	}
	if unmarked, _, ok := unmark("plain"); ok {
		t.Errorf("unmark(plain) = %q, want miss", unmarked)
	}
	// Marked defines stay out of command dispatch and completion.
	ev := NewEvaluator()
	if _, err := evalAll(t, ev, `(begin
	    (define-syntax defhidden
	      (syntax-rules () ((_ pub) (begin (define (hidden) 42) (define (pub) (hidden))))))
	    (defhidden visible)
	    (visible))`); err != nil {
		t.Fatal(err)
	}
	for _, n := range ev.globalEnv.UserFunctionNames() {
		if strings.ContainsRune(n, markByte) {
			t.Errorf("marked name %q leaked into UserFunctionNames", symBase(n))
		}
	}
}
