package llm

import (
	"strings"
	"testing"
)

func TestApplyEditsExact(t *testing.T) {
	content := "func main() {\n\tfmt.Println(\"hi\")\n}\n"
	out, err := applyEdits(content, []editSpec{{OldText: "\"hi\"", NewText: "\"hello\""}})
	if err != nil {
		t.Fatal(err)
	}
	want := "func main() {\n\tfmt.Println(\"hello\")\n}\n"
	if out != want {
		t.Fatalf("got %q, want %q", out, want)
	}
}

func TestApplyEditsMultiple(t *testing.T) {
	content := "alpha\nbeta\ngamma\n"
	out, err := applyEdits(content, []editSpec{
		{OldText: "gamma", NewText: "delta"}, // order independent: matched against original
		{OldText: "alpha", NewText: "omega"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if out != "omega\nbeta\ndelta\n" {
		t.Fatalf("got %q", out)
	}
}

func TestApplyEditsNotUnique(t *testing.T) {
	_, err := applyEdits("x = 1\nx = 1\n", []editSpec{{OldText: "x = 1", NewText: "x = 2"}})
	if err == nil || !strings.Contains(err.Error(), "must be unique") {
		t.Fatalf("expected uniqueness error, got %v", err)
	}
}

func TestApplyEditsNotFound(t *testing.T) {
	_, err := applyEdits("hello\n", []editSpec{{OldText: "goodbye", NewText: "x"}})
	if err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("expected not-found error, got %v", err)
	}
}

func TestApplyEditsOverlap(t *testing.T) {
	_, err := applyEdits("abcdef\n", []editSpec{
		{OldText: "abcd", NewText: "x"},
		{OldText: "cdef", NewText: "y"},
	})
	if err == nil || !strings.Contains(err.Error(), "overlap") {
		t.Fatalf("expected overlap error, got %v", err)
	}
}

func TestApplyEditsFuzzyTrailingWhitespace(t *testing.T) {
	content := "line one   \nline two\t\nline three\n"
	out, err := applyEdits(content, []editSpec{{OldText: "line one\nline two", NewText: "replaced"}})
	if err != nil {
		t.Fatal(err)
	}
	if out != "replaced\nline three\n" {
		t.Fatalf("got %q", out)
	}
}

func TestApplyEditsCRLF(t *testing.T) {
	content := "one\r\ntwo\r\nthree\r\n"
	out, err := applyEdits(content, []editSpec{{OldText: "two", NewText: "2"}})
	if err != nil {
		t.Fatal(err)
	}
	if out != "one\r\n2\r\nthree\r\n" {
		t.Fatalf("got %q", out)
	}
}

func TestApplyEditsBOM(t *testing.T) {
	content := "\uFEFFhead\nbody\n"
	out, err := applyEdits(content, []editSpec{{OldText: "head", NewText: "title"}})
	if err != nil {
		t.Fatal(err)
	}
	if out != "\uFEFFtitle\nbody\n" {
		t.Fatalf("got %q", out)
	}
}

func TestApplyEditsEmptyOldText(t *testing.T) {
	_, err := applyEdits("x\n", []editSpec{{OldText: "", NewText: "y"}})
	if err == nil || !strings.Contains(err.Error(), "non-empty") {
		t.Fatalf("expected non-empty error, got %v", err)
	}
}

func TestApplyEditsNoEdits(t *testing.T) {
	_, err := applyEdits("x\n", nil)
	if err == nil {
		t.Fatal("expected error for empty edits")
	}
}

func TestApplyEditsDeletion(t *testing.T) {
	out, err := applyEdits("keep\ndrop\nkeep2\n", []editSpec{{OldText: "drop\n", NewText: ""}})
	if err != nil {
		t.Fatal(err)
	}
	if out != "keep\nkeep2\n" {
		t.Fatalf("got %q", out)
	}
}
