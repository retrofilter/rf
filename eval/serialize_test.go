package eval

import (
	"strings"
	"testing"
	"time"
)

func TestJSONRows(t *testing.T) {
	ev := NewEvaluator()
	got, err := evalExpr(`(json (list {:name "a" :size 2} {:name "b" :size 3}))`, ev, ev.globalEnv)
	if err != nil {
		t.Fatal(err)
	}
	want := String("{\"name\":\"a\",\"size\":2}\n{\"name\":\"b\",\"size\":3}\n")
	if got != want {
		t.Fatalf("json rows: %#v", got)
	}
}

func TestJSONScalars(t *testing.T) {
	ev := NewEvaluator()
	for expr, want := range map[string]String{
		`(json 42)`:           "42\n",
		`(json "hi")`:         "\"hi\"\n",
		`(json {:k "v"})`:     "{\"k\":\"v\"}\n",
		`(json (list "x" 5))`: "\"x\"\n5\n",
		`(json "two\nlines")`: "\"two\"\n\"lines\"\n", // strings serialize per line
	} {
		got, err := evalExpr(expr, ev, ev.globalEnv)
		if err != nil {
			t.Fatalf("%s: %v", expr, err)
		}
		if got != Value(want) {
			t.Fatalf("%s: got %#v, want %q", expr, got, want)
		}
	}
}

func TestJSONStreamLazy(t *testing.T) {
	done := make(chan struct{})
	var got Value
	var err error
	go func() {
		defer close(done)
		ev := NewEvaluator()
		got, err = evalExpr(`(pipe (sh "yes") (json) (take 2))`, ev, ev.globalEnv)
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("(pipe (sh \"yes\") (json) (take 2)) did not terminate")
	}
	if err != nil || got != String("\"y\"\n\"y\"\n") {
		t.Fatalf("got %#v err %v", got, err)
	}
}

func TestTextRows(t *testing.T) {
	ev := NewEvaluator()
	got, err := evalExpr(`(text (list {:name "a" :type "file" :size 2} {:name "b" :type "dir" :size 3}))`, ev, ev.globalEnv)
	if err != nil {
		t.Fatal(err)
	}
	want := String("a\tfile\t2\nb\tdir\t3\n")
	if got != want {
		t.Fatalf("text rows: %#v", got)
	}
}

func TestTextNonRows(t *testing.T) {
	ev := NewEvaluator()
	for expr, want := range map[string]String{
		`(text "a\nb")`:                "a\nb\n",
		`(text (list "x" 5))`:          "x\n5\n",
		`(pipe (sh "echo hi") (text))`: "hi\n",
	} {
		got, err := evalExpr(expr, ev, ev.globalEnv)
		if err != nil {
			t.Fatalf("%s: %v", expr, err)
		}
		if got != Value(want) {
			t.Fatalf("%s: got %#v, want %q", expr, got, want)
		}
	}
}

func TestTextFeedsShStdin(t *testing.T) {
	ev := NewEvaluator()
	got, err := evalExpr(`(pipe (text (list {:name "a" :size 2})) (sh "tr a-z A-Z"))`, ev, ev.globalEnv)
	if err != nil {
		t.Fatal(err)
	}
	if s, _ := got.(String); !strings.HasPrefix(string(s), "A\t") {
		t.Fatalf("text|sh: %#v", got)
	}
}
