package eval

import (
	"strings"
	"testing"
)

func evalStream(t *testing.T, ev *Evaluator, src string, lines ...string) *Stream {
	t.Helper()
	vals := make([]Value, len(lines))
	for i, l := range lines {
		vals[i] = String(l)
	}
	ev.globalEnv.Set("src-stream", streamFromList(vals, true))
	exprs, err := ParseAll(src)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	v, err := ev.EvalAll(exprs, ev.globalEnv)
	if err != nil {
		t.Fatalf("eval: %v", err)
	}
	s, ok := v.(*Stream)
	if !ok {
		t.Fatalf("want a stream, got %s", PrintValue(v))
	}
	return s
}

func TestWhereLambdaStreamMarkedUserCode(t *testing.T) {
	ev := NewEvaluator()
	s := evalStream(t, ev, `(where (lambda (l) #t) src-stream)`, "a", "b", "c")
	if !s.userCode {
		t.Fatal("where-lambda stream not marked userCode")
	}
	// A plain field comparison runs no Scheme: unmarked.
	rows := evalStream(t, ev, `(where "n" ">" 1 src-stream)`, "x")
	if rows.userCode {
		t.Fatal("field-comparison where wrongly marked userCode")
	}
}

func TestUserCodePropagatesThroughDerivedStreams(t *testing.T) {
	ev := NewEvaluator()
	s := evalStream(t, ev, `(take 2 (where (lambda (l) #t) src-stream))`, "a", "b", "c")
	if !s.userCode {
		t.Fatal("take dropped the userCode mark")
	}
}

func TestShStdinDrainsUserCodeStream(t *testing.T) {
	ev := NewEvaluator()
	s := evalStream(t, ev, `(where (lambda (l) (not (equal? l "b"))) src-stream)`, "a", "b", "c")
	r, closefn, err := shStdinReader(s)
	if err != nil {
		t.Fatalf("shStdinReader: %v", err)
	}
	if _, ok := r.(*strings.Reader); !ok {
		t.Fatalf("userCode stream not drained: reader is %T", r)
	}
	if closefn != nil {
		t.Fatal("drained stream should need no closer")
	}
	var b strings.Builder
	buf := make([]byte, 64)
	for {
		n, err := r.Read(buf)
		b.Write(buf[:n])
		if err != nil {
			break
		}
	}
	if b.String() != "a\nc\n" {
		t.Fatalf("drained text = %q, want %q", b.String(), "a\nc\n")
	}
	// A plain line stream keeps the lazy pump.
	plain := evalStream(t, ev, `src-stream`, "a", "b")
	r2, close2, err := shStdinReader(plain)
	if err != nil {
		t.Fatalf("shStdinReader: %v", err)
	}
	if _, ok := r2.(*strings.Reader); ok {
		t.Fatal("plain stream should stay lazy, not drain")
	}
	if close2 == nil {
		t.Fatal("lazy stream needs its closer")
	}
	_ = close2()
}
