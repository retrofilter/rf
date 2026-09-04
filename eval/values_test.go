package eval

import "testing"

func TestValuesAndEOF(t *testing.T) {
	runStdlibTests(t, []struct {
		expr      string
		expect    Value
		shouldErr bool
	}{
		// A single value stays unwrapped everywhere.
		{`(values 7)`, Integer(7), false},
		{`(+ 1 (values 2))`, Integer(3), false},
		{`(call-with-values (lambda () (values 1 2)) +)`, Integer(3), false},
		{`(call-with-values (lambda () (values 4 5)) (lambda (a b) (list a b)))`, listFromSlice([]Value{Integer(4), Integer(5)}), false},
		{`(call-with-values (lambda () 6) (lambda (x) x))`, Integer(6), false},
		{`(call-with-values (lambda () (values)) (lambda () 'none))`, Symbol("none"), false},
		{`(eof-object? (eof-object))`, true, false},
		{`(eof-object? 'eof)`, false, false},
		{`(eq? (eof-object) (eof-object))`, true, false},
	})

	if s := PrintValue(&MultipleValues{Vals: []Value{Integer(1), String("a")}}); s != `1 "a"` {
		t.Errorf("values printed as %q", s)
	}
	if s := PrintValue(theEOFObject); s != "#<eof>" {
		t.Errorf("eof printed as %q", s)
	}
}
