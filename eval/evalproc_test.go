package eval

import (
	"os"
	"path/filepath"
	"testing"
)

func TestEval(t *testing.T) {
	runStdlibTests(t, []struct {
		expr      string
		expect    Value
		shouldErr bool
	}{
		{`(eval '(* 7 3) (environment '(scheme base)))`, Integer(21), false},
		{`(eval '(+ 1 2))`, Integer(3), false},
		{`(eval 5)`, Integer(5), false},
		{`(eval ''a)`, Symbol("a"), false},
		{`(let ((f (eval '(lambda (f x) (f x x)) (null-environment 5))))
		    (f + 10))`, Integer(20), false},
		// Dotted formals survive the data→AST conversion.
		{`((eval '(lambda (a . rest) (cons a rest))) 1 2 3)`,
			&Pair{Car: Integer(1), Cdr: listFromSlice([]Value{Integer(2), Integer(3)})}, false},
		// Symbols evaluate as variable references.
		{`(procedure? (eval 'car (interaction-environment)))`, true, false},
		// define through eval lands in the shared global env.
		{`(begin (eval '(define eval-defined 99)) eval-defined)`, Integer(99), false},
		{`(eval '(+ 1 2) 5)`, nil, true},
		{`(eval)`, nil, true},
	})
}

func TestEnvironmentSpecifiers(t *testing.T) {
	runStdlibTests(t, []struct {
		expr      string
		expect    Value
		shouldErr bool
	}{
		// All specifiers are views of the one global env (D5).
		{`(eq? (interaction-environment) (environment '(scheme base)))`, true, false},
		{`(eq? (interaction-environment) (scheme-report-environment 5))`, true, false},
		{`(eq? (interaction-environment) (null-environment 5))`, true, false},
		{`(eq? (interaction-environment) (environment '(scheme base) '(scheme inexact)))`, true, false},
		{`(environment '(no such library))`, nil, true},
		{`(environment 42)`, nil, true},
		{`(scheme-report-environment 3)`, nil, true},
		{`(interaction-environment 1)`, nil, true},
	})
}

func TestLoad(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "loaded.scm")
	if err := os.WriteFile(file, []byte("(define loaded-x 7)\n(define (loaded-f y) (* loaded-x y))\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	ev := NewEvaluator()
	mustEvalSeq(t, ev, `(load "`+file+`")`)
	if v := mustEvalSeq(t, ev, `(loaded-f 6)`); !deepEqual(v, Integer(42)) {
		t.Errorf("load: got %v", PrintValue(v))
	}
	mustEvalSeq(t, ev, `(load "`+file+`" (interaction-environment))`)
	if _, err := evalExpr(`(load "`+filepath.Join(dir, "missing.scm")+`")`, ev, ev.globalEnv); err == nil {
		t.Error("load of a missing file should error")
	}
	if _, err := evalExpr(`(load)`, ev, ev.globalEnv); err == nil {
		t.Error("load with no arguments should error")
	}
}

func TestProcessContext(t *testing.T) {
	runStdlibTests(t, []struct {
		expr      string
		expect    Value
		shouldErr bool
	}{
		{`(list? (command-line))`, true, false},
		{`(string? (car (command-line)))`, true, false},
		{`(string? (get-environment-variable "PATH"))`, true, false},
		{`(get-environment-variable "RF_TEST_SURELY_UNSET_VARIABLE")`, false, false},
		{`(pair? (car (get-environment-variables)))`, true, false},
		{`(string? (car (car (get-environment-variables))))`, true, false},
		{`(string? (cdr (car (get-environment-variables))))`, true, false},
		{`(real? (current-second))`, true, false},
		{`(inexact? (current-second))`, true, false},
		{`(exact? (current-jiffy))`, true, false},
		{`(exact? (jiffies-per-second))`, true, false},
		{`(< 0 (jiffies-per-second))`, true, false},
		{`(get-environment-variable)`, nil, true},
		{`(command-line 1)`, nil, true},
	})
}

func TestProcessContextAssistantRedaction(t *testing.T) {
	t.Setenv("RF_TEST_SECRET_TOKEN", "hunter2")
	t.Setenv("RF_TEST_PLAIN_VALUE", "visible")
	ev := NewEvaluator()
	ev.SetCaller(CallerAssistant)
	defer ev.SetCaller(CallerUser)

	if v := mustEvalSeq(t, ev, `(get-environment-variable "RF_TEST_SECRET_TOKEN")`); !deepEqual(v, String(redactedEnvValue)) {
		t.Errorf("assistant lookup of a secret: got %v", PrintValue(v))
	}
	if v := mustEvalSeq(t, ev, `(get-environment-variable "RF_TEST_PLAIN_VALUE")`); !deepEqual(v, String("visible")) {
		t.Errorf("assistant lookup of a plain value: got %v", PrintValue(v))
	}
	if v := mustEvalSeq(t, ev, `(cdr (assoc "RF_TEST_SECRET_TOKEN" (get-environment-variables)))`); !deepEqual(v, String(redactedEnvValue)) {
		t.Errorf("assistant listing masks secrets: got %v", PrintValue(v))
	}
	// exit is user-only: the model must not terminate the shell.
	for _, expr := range []string{`(exit)`, `(emergency-exit 1)`} {
		if _, err := evalExpr(expr, ev, ev.globalEnv); err == nil {
			t.Errorf("%s as assistant should error", expr)
		}
	}
}

func TestJiffiesAdvance(t *testing.T) {
	ev := NewEvaluator()
	v := mustEvalSeq(t, ev, `(let ((a (current-jiffy)))
	   (let loop ((i 0)) (when (< i 1000) (loop (+ i 1))))
	   (<= a (current-jiffy)))`)
	if !deepEqual(v, true) {
		t.Errorf("current-jiffy should be monotonic: got %v", PrintValue(v))
	}
}

func TestR5RSNumericAliases(t *testing.T) {
	runStdlibTests(t, []struct {
		expr      string
		expect    Value
		shouldErr bool
	}{
		{`(exact->inexact 1)`, Number(1.0), false},
		{`(inexact->exact 2.0)`, Integer(2), false},
		{`(exact? (inexact->exact 2.0))`, true, false},
		{`(inexact? (exact->inexact 1))`, true, false},
	})
}
