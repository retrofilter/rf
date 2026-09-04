package eval

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func evalSeq(t *testing.T, ev *Evaluator, exprs ...string) (Value, error) {
	t.Helper()
	var res Value
	for _, e := range exprs {
		var err error
		res, err = evalExpr(e, ev, ev.globalEnv)
		if err != nil {
			return nil, err
		}
	}
	return res, nil
}

func TestStringPorts(t *testing.T) {
	ev := NewEvaluator()
	tests := []struct {
		expr   string
		expect Value
	}{
		{`(let ((p (open-input-string "hé llo"))) (list (read-char p) (peek-char p) (read-char p) (read-string 2 p)))`,
			[]Value{Char('h'), Char('é'), Char('é'), String(" l")}},
		{`(read-line (open-input-string "one\ntwo"))`, String("one")},
		{`(eof-object? (read-line (open-input-string "")))`, true},
		{`(let ((out (open-output-string))) (write-string "abc def" out 4) (get-output-string out))`, String("def")},
		{`(let ((out (open-output-string))) (write-char #\λ out) (get-output-string out))`, String("λ")},
		{`(port? (open-input-string "x"))`, true},
		{`(input-port? (current-input-port))`, true},
		{`(output-port? (current-output-port))`, true},
		{`(textual-port? (open-output-string))`, true},
		{`(binary-port? (open-input-bytevector #u8(1)))`, true},
		{`(let ((p (open-input-string "x"))) (close-port p) (input-port-open? p))`, false},
		{`(call-with-port (open-input-string "hi") read-line)`, String("hi")},
		{`(let ((p (open-input-bytevector #u8(1 2 3)))) (list (peek-u8 p) (read-u8 p) (read-u8 p)))`,
			[]Value{Integer(1), Integer(1), Integer(2)}},
		{`(let ((out (open-output-bytevector))) (write-u8 7 out) (write-bytevector #u8(8 9) out) (get-output-bytevector out))`,
			Bytevector{7, 8, 9}},
	}
	for _, tc := range tests {
		got, err := evalExpr(tc.expr, ev, ev.globalEnv)
		if err != nil {
			t.Errorf("%s: %v", tc.expr, err)
			continue
		}
		if !deepEqual(got, tc.expect) {
			t.Errorf("%s: got %s, want %s", tc.expr, PrintValue(got), PrintValue(tc.expect))
		}
	}

	// Reading a closed port errors; get-output-string still works after close.
	if _, err := evalSeq(t, ev, `(define cp (open-input-string "x"))`, `(close-port cp)`, `(read-char cp)`); err == nil {
		t.Error("read-char on a closed port should error")
	}
}

func TestReadDatums(t *testing.T) {
	ev := NewEvaluator()
	tests := []struct {
		expr   string
		expect Value
	}{
		{`(read (open-input-string " 42 "))`, Integer(42)},
		{`(read (open-input-string "(1 (2) \"three\")"))`, []Value{Integer(1), []Value{Integer(2)}, String("three")}},
		{`(eof-object? (read (open-input-string "")))`, true},
		// One datum per read; the delimiter stays for the next call.
		{`(let ((p (open-input-string "#t(5)"))) (list (read p) (read p)))`, []Value{true, []Value{Integer(5)}}},
		// Datum labels build shared and circular structure.
		{`(cadr (read (open-input-string "#0=(1 . #0#)")))`, Integer(1)},
		{`(cadr (read (open-input-string "(#0=(1 2 3) #0#)")))`, []Value{Integer(1), Integer(2), Integer(3)}},
		// Fold-case directives persist on the port.
		{`(read (open-input-string "#!fold-case ABC"))`, Symbol("abc")},
		{`(read (open-input-string "#!fold-case #!no-fold-case ABC"))`, Symbol("ABC")},
		// Comments before the datum are consumed with it.
		{`(read (open-input-string "#| note |# #;(skip) def"))`, Symbol("def")},
	}
	for _, tc := range tests {
		got, err := evalExpr(tc.expr, ev, ev.globalEnv)
		if err != nil {
			t.Errorf("%s: %v", tc.expr, err)
			continue
		}
		if !deepEqual(got, tc.expect) {
			t.Errorf("%s: got %s, want %s", tc.expr, PrintValue(got), PrintValue(tc.expect))
		}
	}

	// Read failures are read errors, catchable and classified.
	got, err := evalExpr(`(read-error? (guard (exn (#t exn)) (read (open-input-string ")"))))`, ev, ev.globalEnv)
	if err != nil || got != true {
		t.Errorf("unbalanced input should raise a read error, got %v err=%v", got, err)
	}
}

func TestWriteForms(t *testing.T) {
	ev := NewEvaluator()
	tests := []struct {
		expr   string
		expect Value
	}{
		{`(let ((out (open-output-string))) (write "a\nb" out) (get-output-string out))`, String(`"a\nb"`)},
		{`(let ((out (open-output-string))) (write 'abc out) (get-output-string out))`, String("abc")},
		{`(let ((out (open-output-string))) (write '|a b| out) (get-output-string out))`, String("|a b|")},
		{`(let ((out (open-output-string))) (write #\a out) (get-output-string out))`, String("#\\a")},
		{`(let ((out (open-output-string))) (write 4.0 out) (get-output-string out))`, String("4.0")},
		{`(let ((out (open-output-string))) (display "a\nb" out) (get-output-string out))`, String("a\nb")},
		{`(let ((out (open-output-string))) (display '(1 "a" #\b) out) (get-output-string out))`, String("(1 a b)")},
		// write labels only cycles; write-shared labels all sharing.
		{`(let ((out (open-output-string)) (x (list 1 2))) (write (list x x) out) (get-output-string out))`,
			String("((1 2) (1 2))")},
		{`(let ((out (open-output-string)) (x (list 1 2))) (write-shared (list x x) out) (get-output-string out))`,
			String("(#0=(1 2) #0#)")},
		{`(let ((out (open-output-string)) (x (list 1))) (set-cdr! x x) (write x out) (get-output-string out))`,
			String("#0=(1 . #0#)")},
	}
	for _, tc := range tests {
		got, err := evalExpr(tc.expr, ev, ev.globalEnv)
		if err != nil {
			t.Errorf("%s: %v", tc.expr, err)
			continue
		}
		if !deepEqual(got, tc.expect) {
			t.Errorf("%s: got %s, want %s", tc.expr, PrintValue(got), PrintValue(tc.expect))
		}
	}
}

func TestFilePortsRoundTrip(t *testing.T) {
	withTempDir(t, func(dir string, ev *Evaluator, env *Environment) {
		got, err := evalSeq(t, ev,
			`(with-output-to-file "out.txt" (lambda () (display "hello ") (display 42) (newline)))`,
			`(call-with-input-file "out.txt" read-line)`)
		if err != nil {
			t.Fatalf("file round trip: %v", err)
		}
		if got != String("hello 42") {
			t.Errorf("read back %s, want \"hello 42\"", PrintValue(got))
		}

		if got, err = evalSeq(t, ev, `(file-exists? "out.txt")`); err != nil || got != true {
			t.Errorf("file-exists? got %v err=%v", got, err)
		}
		if got, err = evalSeq(t, ev, `(delete-file "out.txt")`, `(file-exists? "out.txt")`); err != nil || got != false {
			t.Errorf("after delete-file, file-exists? got %v err=%v", got, err)
		}

		// with-input-from-file rebinds current-input-port for read's default.
		if _, err := evalSeq(t, ev, `(with-output-to-file "in.txt" (lambda () (display "(1 2 3)")))`); err != nil {
			t.Fatal(err)
		}
		got, err = evalSeq(t, ev, `(with-input-from-file "in.txt" read)`)
		if err != nil {
			t.Fatal(err)
		}
		if !deepEqual(got, []Value{Integer(1), Integer(2), Integer(3)}) {
			t.Errorf("with-input-from-file read got %s", PrintValue(got))
		}

		// A missing file is a file-error? condition.
		got, err = evalSeq(t, ev, `(file-error? (guard (exn (#t exn)) (open-input-file "nope.txt")))`)
		if err != nil || got != true {
			t.Errorf("missing file should be a file error, got %v err=%v", got, err)
		}
	})
}

func TestFilePortGating(t *testing.T) {
	withTempDir(t, func(dir string, ev *Evaluator, env *Environment) {
		if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("hi\n"), 0644); err != nil {
			t.Fatal(err)
		}

		var asked []string
		ev.SetApprover(func(action string) bool {
			asked = append(asked, action)
			return true
		})
		if _, err := evalSeq(t, ev, `(define fp (open-input-file "a.txt"))`, `(read-char fp)`, `(read-line fp)`, `(close-port fp)`); err != nil {
			t.Fatalf("gated reads failed: %v", err)
		}
		want := []string{`open-input-file "a.txt"`}
		if len(asked) != len(want) {
			t.Fatalf("approver saw %v, want %v", asked, want)
		}
		for i, w := range want {
			if asked[i] != w {
				t.Errorf("action %d: got %q, want %q", i, asked[i], w)
			}
		}

		ev.SetApprover(nil)
		if _, err := evalSeq(t, ev, `(define up (open-input-file "a.txt"))`); err != nil {
			t.Fatal(err)
		}
		asked = nil
		ev.SetApprover(func(action string) bool {
			asked = append(asked, action)
			return true
		})
		if _, err := evalSeq(t, ev, `(read-char up)`, `(read-char up)`, `(close-port up)`); err != nil {
			t.Fatalf("reads on user-opened port failed: %v", err)
		}
		if len(asked) != 1 || asked[0] != `read-char "a.txt"` {
			t.Errorf("first assistant touch should prompt once, approver saw %v", asked)
		}

		// String ports are sandboxed: no prompts.
		asked = nil
		if _, err := evalSeq(t, ev, `(read-line (open-input-string "x"))`,
			`(let ((o (open-output-string))) (write-string "y" o) (get-output-string o))`); err != nil {
			t.Fatal(err)
		}
		if len(asked) != 0 {
			t.Errorf("string ports should not prompt, approver saw %v", asked)
		}

		// Denial blocks the operation and surfaces as an error.
		ev.SetApprover(func(action string) bool { return false })
		if _, err := evalSeq(t, ev, `(open-output-file "b.txt")`); err == nil || !strings.Contains(err.Error(), "denied") {
			t.Errorf("denied open-output-file should error, got %v", err)
		}
		if _, err := os.Stat(filepath.Join(dir, "b.txt")); err == nil {
			t.Error("denied open-output-file must not create the file")
		}
		if _, err := evalSeq(t, ev, `(delete-file "a.txt")`); err == nil || !strings.Contains(err.Error(), "denied") {
			t.Errorf("denied delete-file should error, got %v", err)
		}
		if _, err := evalSeq(t, ev, `(write-file "c.txt" "x")`); err == nil || !strings.Contains(err.Error(), "denied") {
			t.Errorf("denied write-file should error, got %v", err)
		}
	})
}

func TestWithApproval(t *testing.T) {
	withTempDir(t, func(dir string, ev *Evaluator, env *Environment) {
		var asked []string
		ev.SetApprover(func(action string) bool {
			asked = append(asked, action)
			return true
		})
		got, err := evalSeq(t, ev,
			`(with-approval (write-file "a.txt" "hi") (read-line (open-input-file "a.txt")))`)
		if err != nil {
			t.Fatalf("with-approval: %v", err)
		}
		if got != String("hi") {
			t.Errorf("with-approval result: %s", PrintValue(got))
		}
		if len(asked) != 1 || !strings.Contains(asked[0], "with-approval") || !strings.Contains(asked[0], `(write-file "a.txt" "hi")`) {
			t.Errorf("expected one with-approval prompt showing the source, got %v", asked)
		}

		// The gate is restored afterwards — the next gated op prompts again.
		asked = nil
		if _, err := evalSeq(t, ev, `(delete-file "a.txt")`); err != nil {
			t.Fatal(err)
		}
		if len(asked) != 1 {
			t.Errorf("gate not restored after with-approval: %v", asked)
		}

		// The gate is restored even when the body errors.
		asked = nil
		if _, err := evalSeq(t, ev, `(with-approval (error "boom"))`); err == nil {
			t.Error("body error should propagate")
		}
		if _, err := evalSeq(t, ev, `(write-file "b.txt" "x")`); err != nil {
			t.Fatal(err)
		}
		if len(asked) != 2 { // the with-approval prompt, then write-file's own
			t.Errorf("gate not restored after erroring body: %v", asked)
		}

		// Denying the block runs nothing.
		ev.SetApprover(func(action string) bool { return false })
		if _, err := evalSeq(t, ev, `(with-approval (write-file "c.txt" "x"))`); err == nil || !strings.Contains(err.Error(), "denied") {
			t.Errorf("denied with-approval should error, got %v", err)
		}
		if _, err := os.Stat(filepath.Join(dir, "c.txt")); err == nil {
			t.Error("denied with-approval must not run its body")
		}

		// Without an approver (user-typed code) it is just begin.
		ev.SetApprover(nil)
		got, err = evalSeq(t, ev, `(with-approval (+ 1 2))`)
		if err != nil || got != Integer(3) {
			t.Errorf("ungated with-approval: got %v err=%v", got, err)
		}
	})
}

func TestStreamToPort(t *testing.T) {
	ev := NewEvaluator()
	ev.globalEnv.Set("s", streamFromList([]Value{String("one"), String("two")}, true))
	got, err := evalSeq(t, ev, `(define p (stream->port s))`, `(list (read-line p) (read-line p) (eof-object? (read-line p)))`)
	if err != nil {
		t.Fatalf("stream->port: %v", err)
	}
	if !deepEqual(got, []Value{String("one"), String("two"), true}) {
		t.Errorf("stream->port read %s", PrintValue(got))
	}

	// read parses datums straight off a stream's text.
	ev.globalEnv.Set("s2", streamFromList([]Value{String("(a b)")}, true))
	got, err = evalSeq(t, ev, `(read (stream->port s2))`)
	if err != nil {
		t.Fatal(err)
	}
	if !deepEqual(got, []Value{Symbol("a"), Symbol("b")}) {
		t.Errorf("read from stream port got %s", PrintValue(got))
	}
}
