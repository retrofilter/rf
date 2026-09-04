package eval

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDiffRows(t *testing.T) {
	ev := NewEvaluator()
	got, err := evalExpr(`(diff (list "a" "b" "c") (list "a" "x" "c" "d"))`, ev, ev.globalEnv)
	if err != nil {
		t.Fatal(err)
	}
	rows, ok := got.([]Value)
	if !ok {
		t.Fatalf("expected rows, got %#v", got)
	}
	type change struct {
		op, text string
		line     int
	}
	want := []change{{"-", "b", 2}, {"+", "x", 2}, {"+", "d", 4}}
	if len(rows) != len(want) {
		t.Fatalf("got %d rows, want %d: %v", len(rows), len(want), PrintValue(got))
	}
	for i, w := range want {
		row := rows[i].(Dictionary)
		if string(row["op"].(String)) != w.op || string(row["text"].(String)) != w.text || int(row["line"].(Integer)) != w.line {
			t.Errorf("row %d: got %s, want %+v", i, PrintValue(row), w)
		}
	}
}

func TestDiffEqual(t *testing.T) {
	ev := NewEvaluator()
	got, err := evalExpr(`(diff (list "a" "b") (list "a" "b"))`, ev, ev.globalEnv)
	if err != nil {
		t.Fatal(err)
	}
	if rows, ok := got.([]Value); !ok || len(rows) != 0 {
		t.Errorf("equal inputs: got %v, want empty rows", PrintValue(got))
	}
	got, err = evalExpr(`(diff (list "a") (list "a") :text)`, ev, ev.globalEnv)
	if err != nil {
		t.Fatal(err)
	}
	if s, ok := got.(String); !ok || string(s) != "" {
		t.Errorf("equal inputs :text: got %v, want \"\"", PrintValue(got))
	}
}

func TestDiffText(t *testing.T) {
	withTempDir(t, func(dir string, ev *Evaluator, env *Environment) {
		if err := os.WriteFile(filepath.Join(dir, "old.txt"), []byte("a\nb\nc\n"), 0644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "new.txt"), []byte("a\nx\nc\n"), 0644); err != nil {
			t.Fatal(err)
		}
		got, err := evalSeq(t, ev, `(diff "old.txt" "new.txt" :text)`)
		if err != nil {
			t.Fatal(err)
		}
		text := string(got.(String))
		for _, want := range []string{"--- old.txt", "+++ new.txt", "@@", "-b", "+x", " c"} {
			if !strings.Contains(text, want) {
				t.Errorf("unified diff missing %q:\n%s", want, text)
			}
		}
	})
}

func TestDiffInputShapes(t *testing.T) {
	withTempDir(t, func(dir string, ev *Evaluator, env *Environment) {
		got, err := evalSeq(t, ev, `(diff (lines "a\nb") (lines "a\nc"))`)
		if err != nil {
			t.Fatal(err)
		}
		if rows := got.([]Value); len(rows) != 2 {
			t.Errorf("stream diff: got %s, want 2 change rows", PrintValue(got))
		}

		if err := os.WriteFile(filepath.Join(dir, "empty.txt"), nil, 0644); err != nil {
			t.Fatal(err)
		}
		got, err = evalSeq(t, ev, `(diff "empty.txt" (list "a"))`)
		if err != nil {
			t.Fatal(err)
		}
		rows := got.([]Value)
		if len(rows) != 1 || string(rows[0].(Dictionary)["op"].(String)) != "+" {
			t.Errorf("empty file diff: got %s, want one + row", PrintValue(got))
		}
	})
}

func TestDiffGated(t *testing.T) {
	withTempDir(t, func(dir string, ev *Evaluator, env *Environment) {
		for _, name := range []string{"a.txt", "b.txt"} {
			if err := os.WriteFile(filepath.Join(dir, name), []byte("x\n"), 0644); err != nil {
				t.Fatal(err)
			}
		}
		var asked []string
		ev.SetApprover(func(action string) bool {
			asked = append(asked, action)
			return true
		})
		if _, err := evalSeq(t, ev, `(diff "a.txt" "b.txt")`); err != nil {
			t.Fatal(err)
		}
		if len(asked) != 1 || asked[0] != `diff "a.txt" "b.txt"` {
			t.Errorf("approver saw %v, want one combined diff prompt", asked)
		}

		ev.SetApprover(func(action string) bool { return false })
		if _, err := evalSeq(t, ev, `(diff "a.txt" "b.txt")`); err == nil || !strings.Contains(err.Error(), "denied") {
			t.Errorf("denied diff should error, got %v", err)
		}
	})
}

func TestDiffErrors(t *testing.T) {
	ev := NewEvaluator()
	if _, err := evalExpr(`(diff (list "a"))`, ev, ev.globalEnv); err == nil || !strings.Contains(err.Error(), "2 inputs") {
		t.Errorf("one input: got %v", err)
	}
	if _, err := evalExpr(`(diff (list "a") (list "b") :nope)`, ev, ev.globalEnv); err == nil || !strings.Contains(err.Error(), ":nope") {
		t.Errorf("unknown option: got %v", err)
	}
	if _, err := evalExpr(`(diff 1 2)`, ev, ev.globalEnv); err == nil || !strings.Contains(err.Error(), "file path, a stream, or a list") {
		t.Errorf("bad input type: got %v", err)
	}
}
