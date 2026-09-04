package eval

import (
	"strings"
	"testing"
)

func TestNotCallableErrors(t *testing.T) {
	ev := NewEvaluator()
	_, err := evalExpr(`(let ((r {:status 200})) (r :status))`, ev, ev.globalEnv)
	if err == nil {
		t.Fatal("expected an error applying a dictionary")
	}
	msg := err.Error()
	if !strings.Contains(msg, "{:status 200}") || strings.Contains(msg, "map[") {
		t.Fatalf("dictionary head should print in Scheme: %q", msg)
	}
	if !strings.Contains(msg, "(get :key dict)") {
		t.Fatalf("dictionary head should hint at get: %q", msg)
	}
	_, err = evalExpr(`(42 1)`, ev, ev.globalEnv)
	if err == nil || err.Error() != "not a function: 42" {
		t.Fatalf("plain non-callable: %v", err)
	}
}

func TestGetTypeErrorNamesValue(t *testing.T) {
	ev := NewEvaluator()
	_, err := evalExpr(`(get 1 (match "zzz" "abc"))`, ev, ev.globalEnv)
	if err == nil || !strings.Contains(err.Error(), "got #f (a failed match?)") {
		t.Fatalf("get on #f: %v", err)
	}
	_, err = evalExpr(`(get 1 42)`, ev, ev.globalEnv)
	if err == nil || !strings.Contains(err.Error(), "got 42") {
		t.Fatalf("get on a number: %v", err)
	}
}

func TestGetKeySpellings(t *testing.T) {
	ev := NewEvaluator()
	for _, expr := range []string{`(get :status {:status 200})`, `(get "status" {:status 200})`, `(get 'status {:status 200})`} {
		got, err := evalExpr(expr, ev, ev.globalEnv)
		if err != nil || got != Integer(200) {
			t.Fatalf("%s: %v %v", expr, got, err)
		}
	}
	got, err := evalExpr(`(get :missing {:status 200})`, ev, ev.globalEnv)
	if err != nil || got != false {
		t.Fatalf("missing key: %v %v", got, err)
	}
}

func TestShStdinAnyOrder(t *testing.T) {
	ev := NewEvaluator()
	for _, expr := range []string{
		`(get "stdout" (sh "tr a-z A-Z" :full (lines "hi")))`,
		`(get "stdout" (sh "tr a-z A-Z" (lines "hi") :full))`,
		`(get "stdout" (sh "tr a-z A-Z" (lines "hi") {:timeout 5} :full))`,
	} {
		got, err := evalExpr(expr, ev, ev.globalEnv)
		if err != nil || got != String("HI") {
			t.Fatalf("%s: %#v %v", expr, got, err)
		}
	}
}
