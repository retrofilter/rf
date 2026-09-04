package eval

import (
	"strings"
	"testing"
)

func TestInspectLambdaRoundTrip(t *testing.T) {
	ev := NewEvaluator()
	env := ev.globalEnv
	if _, err := evalExpr(`(define (fib n) (if (< n 2) n (+ (fib (- n 1)) (fib (- n 2)))))`, ev, env); err != nil {
		t.Fatal(err)
	}
	got, err := evalExpr(`(inspect fib)`, ev, env)
	if err != nil {
		t.Fatal(err)
	}
	src, ok := got.(String)
	if !ok {
		t.Fatalf("(inspect fib) = %T, want String", got)
	}
	if !strings.HasPrefix(string(src), "(define (fib n)") {
		t.Errorf("source = %q, want a (define (fib n) ...) form", src)
	}

	fresh := NewEvaluator()
	forms, err := ParseAll(string(src))
	if err != nil {
		t.Fatalf("inspect output does not re-parse: %v\n%s", err, src)
	}
	if _, err := fresh.EvalAll(forms, fresh.globalEnv); err != nil {
		t.Fatalf("inspect output does not re-evaluate: %v\n%s", err, src)
	}
	v, err := evalExpr(`(fib 10)`, fresh, fresh.globalEnv)
	if err != nil {
		t.Fatal(err)
	}
	if v != Integer(55) {
		t.Errorf("rebuilt (fib 10) = %v, want 55", v)
	}
}

func TestInspectEscapesStrings(t *testing.T) {
	ev := NewEvaluator()
	env := ev.globalEnv
	if _, err := evalExpr(`(define (greet name) (string-append "hi \"" name "\"\n"))`, ev, env); err != nil {
		t.Fatal(err)
	}
	got, err := evalExpr(`(inspect greet)`, ev, env)
	if err != nil {
		t.Fatal(err)
	}
	src := string(got.(String))

	fresh := NewEvaluator()
	forms, err := ParseAll(src)
	if err != nil {
		t.Fatalf("escaped source does not re-parse: %v\n%s", err, src)
	}
	if _, err := fresh.EvalAll(forms, fresh.globalEnv); err != nil {
		t.Fatal(err)
	}
	v, err := evalExpr(`(greet "bob")`, fresh, fresh.globalEnv)
	if err != nil {
		t.Fatal(err)
	}
	if v != String("hi \"bob\"\n") {
		t.Errorf("rebuilt (greet \"bob\") = %q, want %q", v, "hi \"bob\"\n")
	}
}

func TestInspectVariableDefine(t *testing.T) {
	ev := NewEvaluator()
	env := ev.globalEnv
	if _, err := evalExpr(`(define default-graph "personal")`, ev, env); err != nil {
		t.Fatal(err)
	}
	got, err := evalExpr(`(inspect default-graph)`, ev, env)
	if err != nil {
		t.Fatal(err)
	}
	if got != String(`(define default-graph "personal")`) {
		t.Errorf("(inspect default-graph) = %v", got)
	}
}

func TestInspectStringName(t *testing.T) {
	// Command mode desugars `inspect fib` to (inspect "fib").
	ev := NewEvaluator()
	env := ev.globalEnv
	if _, err := evalExpr(`(define (id x) x)`, ev, env); err != nil {
		t.Fatal(err)
	}
	got, err := evalExpr(`(inspect "id")`, ev, env)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(got.(String)), "(define (id x)") {
		t.Errorf("(inspect \"id\") = %v", got)
	}
}

func TestInspectRefusals(t *testing.T) {
	ev := NewEvaluator()
	env := ev.globalEnv
	if _, err := evalExpr(`(define adder (let ((n 5)) (lambda (x) (+ x n))))`, ev, env); err != nil {
		t.Fatal(err)
	}
	for expr, want := range map[string]string{
		`(inspect ls)`:      "builtin",
		`(inspect define)`:  "special form",
		`(inspect let)`:     "macro",
		`(inspect adder)`:   "local state",
		`(inspect missing)`: "unbound",
		`(inspect 42)`:      "expects a name",
	} {
		_, err := evalExpr(expr, ev, env)
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s error = %v, want mention of %q", expr, err, want)
		}
	}
}

func TestPersistExpandsToAgent(t *testing.T) {
	ev := NewEvaluator()
	env := ev.globalEnv
	if _, err := evalExpr(`(define (square x) (* x x))`, ev, env); err != nil {
		t.Fatal(err)
	}

	var gotArgs []Value
	env.Set("agent", BuiltinFunc(func(args []Value, env *Environment) (Value, error) {
		gotArgs = args
		return String("persisted"), nil
	}))

	v, err := evalExpr(`(persist square)`, ev, env)
	if err != nil {
		t.Fatal(err)
	}
	if v != Value(true) {
		t.Errorf("(persist square) = %v, want true", v)
	}
	if len(gotArgs) != 2 {
		t.Fatalf("agent called with %d args, want instruction + source", len(gotArgs))
	}
	instr, ok := gotArgs[0].(String)
	if !ok || !strings.Contains(string(instr), "square") || !strings.Contains(string(instr), ".rf.scm") {
		t.Errorf("instruction = %v, want it to name square and the prelude", gotArgs[0])
	}
	src, ok := gotArgs[1].(String)
	if !ok || !strings.HasPrefix(string(src), "(define (square x)") {
		t.Errorf("source arg = %v, want the inspect output", gotArgs[1])
	}
}
