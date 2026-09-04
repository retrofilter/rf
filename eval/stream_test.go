package eval

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

func TestStreamSingleUse(t *testing.T) {
	s := streamFromList([]Value{String("a")}, true)
	if _, ok, err := s.Next(); !ok || err != nil {
		t.Fatalf("first Next: ok=%v err=%v", ok, err)
	}
	if _, ok, err := s.Next(); ok || err != nil {
		t.Fatalf("end Next: ok=%v err=%v", ok, err) // clean end
	}
	if _, _, err := s.Next(); err != ErrStreamConsumed {
		t.Fatalf("spent Next: err=%v, want ErrStreamConsumed", err)
	}
}

func TestPipeShYesTake(t *testing.T) {
	done := make(chan struct{})
	var got Value
	var err error
	go func() {
		defer close(done)
		ev := NewEvaluator()
		got, err = evalExpr(`(pipe (sh "yes") (take 2))`, ev, ev.globalEnv)
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("(pipe (sh \"yes\") (take 2)) did not terminate")
	}
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	if got != String("y\ny\n") {
		t.Fatalf("pipe result: %#v", got)
	}
}

func TestGrepShStreamTake(t *testing.T) {
	done := make(chan struct{})
	var got Value
	var err error
	go func() {
		defer close(done)
		ev := NewEvaluator()
		got, err = evalExpr(`(pipe (sh "yes hello") (grep "ell") (take 1))`, ev, ev.globalEnv)
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("lazy grep pipeline did not terminate")
	}
	if err != nil || got != String("hello\n") {
		t.Fatalf("got %#v err %v", got, err)
	}
}

func TestGrepFilename(t *testing.T) {
	withTempDir(t, func(dir string, ev *Evaluator, env *Environment) {
		if err := os.WriteFile(filepath.Join(dir, "f.txt"), []byte("alpha\nbeta\n"), 0644); err != nil {
			t.Fatal(err)
		}
		got, err := evalExpr(`(grep "bet" "f.txt")`, ev, env)
		if err != nil || got != String("beta\n") {
			t.Fatalf("grep filename: got %#v err %v", got, err)
		}
		_, err = evalExpr(`(grep "x" "missing.txt")`, ev, env)
		if err == nil || !strings.Contains(err.Error(), "lines") {
			t.Fatalf("missing file error should mention (lines ...): %v", err)
		}
	})
}

func TestLinesBuiltin(t *testing.T) {
	ev := NewEvaluator()
	got, err := evalExpr(`(lines "a\nb\n")`, ev, ev.globalEnv)
	if err != nil {
		t.Fatal(err)
	}
	lst, ok := got.([]Value)
	if !ok || len(lst) != 2 || lst[0] != String("a") || lst[1] != String("b") {
		t.Fatalf("lines: %#v", got)
	}
}

func TestStreamCoercions(t *testing.T) {
	withTempDir(t, func(dir string, ev *Evaluator, env *Environment) {
		if err := os.WriteFile(filepath.Join(dir, "f.txt"), []byte("one\ntwo\nthree\n"), 0644); err != nil {
			t.Fatal(err)
		}
		for _, tc := range []struct {
			expr   string
			expect Value
		}{
			{`(length (cat "f.txt"))`, Integer(3)},                     // native wc -l
			{`(string-contains? (cat "f.txt") "two")`, true},           // AsString rejoin
			{`(get 1 (cat "f.txt"))`, String("two")},                   // indexing materializes
			{`(car (match "t(w)o" (cat "f.txt")))`, String("two")},     // match on stream
			{`(get 0 (map string-upcase (cat "f.txt")))`, nil},         // placeholder, checked below
			{`(string-trim (cat "f.txt"))`, String("one\ntwo\nthree")}, // trims trailing newline
			{`(take 2 (cat "f.txt"))`, String("one\ntwo\n")},           // lazy take stays a line stream
			{`(similar "" (list "a"))`, nil},                           // placeholder, not exercised here
		} {
			if tc.expect == nil {
				continue
			}
			got, err := evalExpr(tc.expr, ev, env)
			if err != nil {
				t.Errorf("%s: %v", tc.expr, err)
				continue
			}
			if !deepEqual(got, tc.expect) {
				t.Errorf("%s = %#v, want %#v", tc.expr, got, tc.expect)
			}
		}
		// map over a stream applies per line
		got, err := evalExpr(`(map string-upcase (cat "f.txt"))`, ev, env)
		if err != nil {
			t.Fatal(err)
		}
		lst := got.([]Value)
		if len(lst) != 3 || lst[0] != String("ONE") {
			t.Fatalf("map over stream: %#v", got)
		}
	})
}

func TestWriteFileStream(t *testing.T) {
	withTempDir(t, func(dir string, ev *Evaluator, env *Environment) {
		if err := os.WriteFile(filepath.Join(dir, "in.txt"), []byte("a\nb\nc\n"), 0644); err != nil {
			t.Fatal(err)
		}
		got, err := evalExpr(`(pipe (cat "in.txt") (grep "[ab]") (write-file "out.txt"))`, ev, env)
		if err != nil {
			t.Fatal(err)
		}
		if got != Integer(4) {
			t.Fatalf("write-file byte count: %v", got)
		}
		data, err := os.ReadFile(filepath.Join(dir, "out.txt"))
		if err != nil || string(data) != "a\nb\n" {
			t.Fatalf("out.txt = %q err %v", data, err)
		}
	})
}

func TestPrintValueCapped(t *testing.T) {
	// non-stream over cap truncates with the notice
	long := String(strings.Repeat("x", 100))
	out, err := PrintValueCapped(long, 10)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "[truncated") || len(out) > 10+len(streamTruncNotice)+2 {
		t.Fatalf("capped print: %q", out)
	}

	// a line stream drains to a quoted text block
	s := streamFromList([]Value{String("a"), String("b")}, true)
	out, err = PrintValueCapped(s, 1000)
	if err != nil || out != "\"a\nb\n\"" {
		t.Fatalf("stream print: %q err %v", out, err)
	}

	// a row stream drains to a printed list
	rows := streamFromList([]Value{Dictionary{"k": String("v")}}, false)
	out, err = PrintValueCapped(rows, 1000)
	if err != nil || out != `({:k "v"})` {
		t.Fatalf("row stream print: %q err %v", out, err)
	}

	// one oversized item can't overshoot the budget: the final text clamps
	huge := streamFromList([]Value{String(strings.Repeat("z", 5000))}, true)
	out, err = PrintValueCapped(huge, 100)
	if err != nil || !strings.Contains(out, "[truncated") || len(out) > 100+len(streamTruncNotice)+2 {
		t.Fatalf("oversized item print: len=%d err=%v", len(out), err)
	}

	multi := String(strings.Repeat("é", 100)) // 2 bytes per rune
	out, err = PrintValueCapped(multi, 11)    // odd cap falls mid-rune
	if err != nil || !utf8.ValidString(out) {
		t.Fatalf("mid-rune cap (non-stream): %q err %v", out, err)
	}
	multiStream := streamFromList([]Value{String(strings.Repeat("é", 5000))}, true)
	out, err = PrintValueCapped(multiStream, 101)
	if err != nil || !utf8.ValidString(out) {
		t.Fatalf("mid-rune cap (stream clamp): %q err %v", out, err)
	}

	// an infinite stream stops at the cap (and kills its producer)
	done := make(chan struct{})
	go func() {
		defer close(done)
		ev := NewEvaluator()
		v, evalErr := ev.Eval(mustParse(t, `(sh "yes")`), ev.globalEnv)
		if evalErr != nil {
			t.Errorf("sh: %v", evalErr)
			return
		}
		out, err = PrintValueCapped(v, 1024)
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("PrintValueCapped on (sh \"yes\") did not terminate")
	}
	if err != nil || !strings.Contains(out, "[truncated") {
		t.Fatalf("infinite stream capped print: %q err %v", out, err)
	}
}

func TestFormatTableWidthWraps(t *testing.T) {
	long := strings.TrimSpace(strings.Repeat("lorem ipsum dolor ", 20)) // ~360 cols
	rows := []Value{
		Dictionary{"text": String(long), "score": Number(0.9)},
		Dictionary{"text": String("short"), "score": Number(0.5)},
	}

	out, ok := FormatTableWidth(rows, 80)
	if !ok {
		t.Fatal("FormatTableWidth rejected rows")
	}
	lines := strings.Split(out, "\n")
	if len(lines) < 4 {
		t.Fatalf("long cell should wrap onto continuation lines:\n%s", out)
	}
	for i, line := range lines {
		if w := len([]rune(line)); w > 80 {
			t.Errorf("line %d exceeds budget (%d cols): %q", i, w, line)
		}
	}
	// the score column survives untouched on each row's first line
	if !strings.Contains(out, "0.9") || !strings.Contains(out, "0.5") {
		t.Errorf("scores missing:\n%s", out)
	}
	// full text is preserved across the wrapped lines
	if joined := strings.Join(strings.Fields(out), " "); !strings.Contains(joined, "lorem ipsum dolor lorem") {
		t.Errorf("wrapped text lost content:\n%s", out)
	}

	// zero budget = unlimited, single line per row as before
	out, ok = FormatTableWidth(rows, 0)
	if !ok || len(strings.Split(out, "\n")) != 3 {
		t.Fatalf("unlimited width should keep one line per row:\n%s", out)
	}

	// a hard word (no spaces) longer than the width also breaks
	solid := []Value{Dictionary{"text": String(strings.Repeat("x", 200))}}
	out, ok = FormatTableWidth(solid, 60)
	if !ok {
		t.Fatal("FormatTableWidth rejected solid row")
	}
	for i, line := range strings.Split(out, "\n") {
		if w := len([]rune(line)); w > 60 {
			t.Errorf("solid line %d exceeds budget (%d cols)", i, w)
		}
	}
}

func mustParse(t *testing.T, code string) Value {
	t.Helper()
	expr, err := Parse(code)
	if err != nil {
		t.Fatal(err)
	}
	return expr
}
