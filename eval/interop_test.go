package eval

import (
	"strings"
	"testing"
	"time"
)

func TestShStdinStream(t *testing.T) {
	ev := NewEvaluator()
	got, err := evalExpr(`(pipe (sh "printf 'a\nb\n'") (sh "tr a-z A-Z"))`, ev, ev.globalEnv)
	if err != nil {
		t.Fatal(err)
	}
	if got != String("A\nB\n") {
		t.Fatalf("stream stdin: %#v", got)
	}
}

func TestShStdinList(t *testing.T) {
	ev := NewEvaluator()
	got, err := evalExpr(`(sh "tr a-z A-Z" (lines "a\nb"))`, ev, ev.globalEnv)
	if err != nil {
		t.Fatal(err)
	}
	materialized, err := Materialize(got)
	if err != nil {
		t.Fatal(err)
	}
	if materialized != String("A\nB\n") {
		t.Fatalf("list stdin: %#v", materialized)
	}
}

func TestShStdinFull(t *testing.T) {
	ev := NewEvaluator()
	got, err := evalExpr(`(get "stdout" (sh "tr a-z A-Z" :full (lines "hi")))`, ev, ev.globalEnv)
	if err != nil {
		t.Fatal(err)
	}
	if got != String("HI") {
		t.Fatalf(":full stdin: %#v", got)
	}
}

func TestShStdinRowsTSV(t *testing.T) {
	ev := NewEvaluator()
	got, err := evalExpr(`(get 0 (sh "cat" (list {:name "a" :size 5})))`, ev, ev.globalEnv)
	if err != nil {
		t.Fatalf("rows stdin: %v", err)
	}
	if got != String("a\t5") {
		t.Fatalf("rows stdin TSV = %q, want \"a\\t5\"", PrintValue(got))
	}
}

func TestShStdinStringError(t *testing.T) {
	ev := NewEvaluator()
	_, err := evalExpr(`(sh "echo" "hi")`, ev, ev.globalEnv)
	if err == nil || !strings.Contains(err.Error(), "lines") {
		t.Fatalf("string stdin error: %v", err)
	}
}

func TestShStdinEarlyExit(t *testing.T) {
	done := make(chan struct{})
	var got Value
	var err error
	go func() {
		defer close(done)
		ev := NewEvaluator()
		got, err = evalExpr(`(pipe (sh "yes") (sh "head -2"))`, ev, ev.globalEnv)
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("(pipe (sh \"yes\") (sh \"head -2\")) did not terminate")
	}
	if err != nil || got != String("y\ny\n") {
		t.Fatalf("got %#v err %v", got, err)
	}
}

func TestSimilarOverShStream(t *testing.T) {
	installFakeEncoder(t, &fakeEncoder{vectors: map[string][]float32{
		"auth":          {1, 0, 0, 0},
		"fix auth flow": {0.9, 0.1, 0, 0},
		"readme tweaks": {0, 1, 0, 0},
	}})
	ev := NewEvaluator()
	got, err := evalExpr(`(pipe (sh "echo fix auth flow; echo readme tweaks") (similar "auth"))`, ev, ev.globalEnv)
	if err != nil {
		t.Fatal(err)
	}
	rows, ok := got.([]Value)
	if !ok || len(rows) != 2 {
		t.Fatalf("similar over sh: %#v", got)
	}
	if rows[0].(Dictionary)["text"] != String("fix auth flow") {
		t.Fatalf("ranking: %#v", rows)
	}
}
