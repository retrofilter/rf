package eval

import "testing"

func TestMacroRedefinition(t *testing.T) {
	ev := NewEvaluator()
	env := ev.globalEnv
	eval := func(prog string) Value {
		t.Helper()
		exprs, err := ParseAll(prog)
		if err != nil {
			t.Fatalf("parse: %v", err)
		}
		got, err := ev.EvalAll(exprs, env)
		if err != nil {
			t.Fatalf("eval: %v", err)
		}
		return got
	}
	if got := eval(`
		(define-syntax m (syntax-rules () ((_) 1)))
		(define (f) (m))
		(f)`); PrintValue(got) != "1" {
		t.Fatalf("first definition: got %s, want 1", PrintValue(got))
	}
	if got := eval(`
		(define-syntax m (syntax-rules () ((_) 2)))
		(f)`); PrintValue(got) != "1" {
		t.Fatalf("compiled site after redefinition: got %s, want 1 (expand-once)", PrintValue(got))
	}
	if got := eval(`(m)`); PrintValue(got) != "2" {
		t.Fatalf("new site after redefinition: got %s, want 2", PrintValue(got))
	}
	if got := eval(`(define (f) (m)) (f)`); PrintValue(got) != "2" {
		t.Fatalf("recompiled definition: got %s, want 2", PrintValue(got))
	}
}

func TestMacroCacheRepeatedSite(t *testing.T) {
	ev := NewEvaluator()
	env := ev.globalEnv
	prog := `
		(define-syntax swap! (syntax-rules ()
		  ((_ a b) (let ((t a)) (set! a b) (set! b t)))))
		(define (spin n)
		  (let loop ((i 0) (x 1) (y 2))
		    (if (= i n) (list x y)
		        (loop (+ i 1) y x))))
		(define x 1) (define y 2)
		(swap! x y) (swap! x y) (swap! x y)
		(list x y (spin 101))`
	exprs, err := ParseAll(prog)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	got, err := ev.EvalAll(exprs, env)
	if err != nil {
		t.Fatalf("eval: %v", err)
	}
	if PrintValue(got) != "(2 1 (2 1))" {
		t.Fatalf("got %s, want (2 1 (2 1))", PrintValue(got))
	}
}
