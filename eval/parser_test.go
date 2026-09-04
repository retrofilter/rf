package eval

import (
	"math"
	"reflect"
	"testing"
)

func TestTokenize(t *testing.T) {
	cases := []struct {
		input    string
		expected []string
	}{
		{"(+ 1 2)", []string{"(", "+", "1", "2", ")"}},
		{"'foo", []string{"'", "foo"}},
		{"(define :foo 42)", []string{"(", "define", ":foo", "42", ")"}},
		{"{:foo \"Test\" :age 232}", []string{"{", ":foo", "\"Test\"", ":age", "232", "}"}},
		{"(list {:foo 1 :bar 2})", []string{"(", "list", "{", ":foo", "1", ":bar", "2", "}", ")"}},
		{"\"string with spaces\"", []string{"\"string with spaces\""}},
	}
	for _, c := range cases {
		got := tokenize(c.input)
		if !reflect.DeepEqual(got, c.expected) {
			t.Errorf("tokenize(%q) = %v, want %v", c.input, got, c.expected)
		}
	}
}

func TestStringEscapes(t *testing.T) {
	cases := []struct {
		input    string
		expected Value
	}{
		{`"a\nb"`, String("a\nb")},
		{`"tab\there"`, String("tab\there")},
		{`"quote \" inside"`, String(`quote " inside`)},
		{`"back\\slash"`, String(`back\slash`)},
		// Invalid escapes keep the raw content instead of failing the parse
		{`"a\qb"`, String(`a\qb`)},
	}
	for _, c := range cases {
		got, err := Parse(c.input)
		if err != nil {
			t.Errorf("Parse(%q) error: %v", c.input, err)
			continue
		}
		if !reflect.DeepEqual(got, c.expected) {
			t.Errorf("Parse(%q) = %q, want %q", c.input, got, c.expected)
		}
	}
}

func TestParse(t *testing.T) {
	cases := []struct {
		input    string
		expected Value
	}{
		{"42", Integer(42)},
		{"\"hello\"", String("hello")},
		{"foo", Symbol("foo")},
		{":foo", Keyword("foo")},
		{"#t", true},
		{"#f", false},
		{"#true", true},
		{"#false", false},
		{"#\\a", Char('a')},
		{"#\\space", Char(' ')},
		{"#\\x41", Char('A')},
		{"#\\λ", Char('λ')},
		{"#\\(", Char('(')},
		{"#(1 2 3)", Vector{Integer(1), Integer(2), Integer(3)}},
		{"#()", Vector{}},
		{"'#(a b)", []Value{Symbol("quote"), Vector{Symbol("a"), Symbol("b")}}},
		{"#u8(0 255)", Bytevector{0, 255}},
		{"#x1A", Integer(26)},
		{"#b101", Integer(5)},
		{"#o17", Integer(15)},
		{"#d10", Integer(10)},
		{"#e10", Integer(10)},
		{"#i5", Number(5)},
		{"#x-F", Integer(-15)},
		{"+inf.0", Number(math.Inf(1))},
		{"-inf.0", Number(math.Inf(-1))},
		{"inf", Symbol("inf")},
		{"nan", Symbol("nan")},
		{"|two words|", Symbol("two words")},
		{"(a #;b c)", []Value{Symbol("a"), Symbol("c")}},
		{"(a #;(b 1) c)", []Value{Symbol("a"), Symbol("c")}},
		{"#|block\ncomment|# 7", Integer(7)},
		{"#| nested #| inner |# outer |# 8", Integer(8)},
		{"(+ 1 2)", []Value{Symbol("+"), Integer(1), Integer(2)}},
		{"'foo", []Value{Symbol("quote"), Symbol("foo")}},
		{"'(+ 1 2)", []Value{Symbol("quote"), []Value{Symbol("+"), Integer(1), Integer(2)}}},
		{"()", []Value{}},
		{"(list) ", []Value{Symbol("list")}},
		{"(a (b (c d)))", []Value{Symbol("a"), []Value{Symbol("b"), []Value{Symbol("c"), Symbol("d")}}}},
		{"(1 . 2)", &Pair{Car: Integer(1), Cdr: Integer(2)}},
		{"{:foo 1 :bar 2}", Dictionary{"foo": Integer(1), "bar": Integer(2)}},
		{"(list {:foo 1 :bar 2})", []Value{Symbol("list"), Dictionary{"foo": Integer(1), "bar": Integer(2)}}},
		{"{:ready}", Dictionary{"ready": true}},
		{"{:all :project \"x\" :done}", Dictionary{"all": true, "project": String("x"), "done": true}},
		{"(:symb1 :symb2)", []Value{Keyword("symb1"), Keyword("symb2")}},
		{"(= 1 2)", []Value{Symbol("="), Integer(1), Integer(2)}},
		{"(λ α β)", []Value{Symbol("λ"), Symbol("α"), Symbol("β")}}, // Unicode symbols
		{"(foo-bar! *baz? /qux_42)", []Value{Symbol("foo-bar!"), Symbol("*baz?"), Symbol("/qux_42")}},
	}
	for _, c := range cases {
		got, err := Parse(c.input)
		if err != nil {
			t.Errorf("Parse(%q) error: %v", c.input, err)
			continue
		}
		if !reflect.DeepEqual(got, c.expected) {
			t.Errorf("Parse(%q) = %#v, want %#v", c.input, got, c.expected)
		}
	}
}

func TestTokenize_SkipsComments(t *testing.T) {
	cases := []struct {
		input    string
		expected []string
	}{
		{"; full line comment\n(+ 1 2)", []string{"(", "+", "1", "2", ")"}},
		{"(+ 1 2) ; end of line comment", []string{"(", "+", "1", "2", ")"}},
		{"(+ ; inline comment\n2 3)", []string{"(", "+", "2", "3", ")"}},
		{"(+ \"; not a comment\" 2)", []string{"(", "+", "\"; not a comment\"", "2", ")"}},
	}
	for _, c := range cases {
		tokens := tokenize(c.input)
		if len(tokens) != len(c.expected) {
			t.Errorf("tokenize(%q) = %v, want %v", c.input, tokens, c.expected)
			continue
		}
		for i := range tokens {
			if tokens[i] != c.expected[i] {
				t.Errorf("tokenize(%q)[%d] = %q, want %q", c.input, i, tokens[i], c.expected[i])
			}
		}
	}
}

func TestParseAll_SkipsComments(t *testing.T) {
	input := `; comment
(+ 1 2) ; another
(* 2 3)
; last comment`
	exprs, err := ParseAll(input)
	if err != nil {
		t.Fatalf("ParseAll error: %v", err)
	}
	if len(exprs) != 2 {
		t.Fatalf("expected 2 expressions, got %d", len(exprs))
	}
}

func TestSchemeComplete(t *testing.T) {
	cases := map[string]bool{
		"(+ 1 2)":             true,
		"(define (f x)":       false,
		"(define (f x)\n  x)": true,
		"(f 1))":              true,  // over-closed: accept, parse error surfaces
		`(display "a(b")`:     true,  // paren inside a string doesn't count
		`(display "a\"(b")`:   true,  // nor behind an escaped quote
		`(display "untermina`: false, // open string keeps reading
		"(foo ; )":            false, // closer hidden by a comment
		"(foo ; )\n)":         true,
		"{:a 1":               false,
		"`(x ,(+ 1 1))":       true,
	}
	for src, want := range cases {
		if got := SchemeComplete(src); got != want {
			t.Errorf("SchemeComplete(%q) = %v, want %v", src, got, want)
		}
	}
}
