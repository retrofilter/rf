package eval

import "testing"

func TestCharBuiltins(t *testing.T) {
	runStdlibTests(t, []struct {
		expr      string
		expect    Value
		shouldErr bool
	}{
		{`(char? #\a)`, true, false},
		{`(char? "a")`, false, false},
		{`(char->integer #\a)`, Integer(97), false},
		{`(integer->char 955)`, Char('λ'), false},
		{`(integer->char -1)`, nil, true},
		{`(integer->char 55296)`, nil, true}, // surrogate
		{`(char=? #\a #\a #\a)`, true, false},
		{`(char<? #\a #\b #\c)`, true, false},
		{`(char<? #\a #\a)`, false, false},
		{`(char-ci=? #\a #\A)`, true, false},
		{`(char-upcase #\a)`, Char('A'), false},
		{`(char-foldcase #\ſ)`, Char('s'), false},
		{`(char-numeric? #\3)`, true, false},
		{`(digit-value #\7)`, Integer(7), false},
		{`(digit-value #\a)`, false, false},
		{`(char=? #\a)`, nil, true},
		{`(char=? #\a "b")`, nil, true},
	})
}
