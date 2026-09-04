package eval

import "testing"

func TestMutableStrings(t *testing.T) {
	runStdlibTests(t, []struct {
		expr      string
		expect    Value
		shouldErr bool
	}{
		// Both representations satisfy string? and compare by content.
		{`(string? (make-string 2))`, true, false},
		{`(string? "lit")`, true, false},
		{`(equal? (string #\h #\i) "hi")`, true, false},
		{`(equal? "hi" (string #\h #\i))`, true, false},
		{`(make-string 3 #\-)`, String("---"), false},
		// Mutation is visible through aliases; literals refuse it.
		{`(let ((s (string #\a #\b #\c))) (string-set! s 1 #\-) s)`, String("a-c"), false},
		{`(string-set! "abc" 1 #\-)`, nil, true},
		{`(let ((s (make-string 3 #\x))) (string-fill! s #\- 1) s)`, String("x--"), false},
		{`(let ((s (string-copy "12345"))) (string-copy! s 1 "abcde" 0 2) s)`, String("1ab45"), false},
		// string-copy allocates the mutable kind, from either kind.
		{`(let ((s (string-copy "abc" 1))) (string-set! s 0 #\z) s)`, String("zc"), false},
		// Rune indexing, not byte indexing.
		{`(string-ref "aλc" 1)`, Char('λ'), false},
		{`(string-length (string #\a #\x1F700))`, Integer(2), false},
		// Existing builtins accept the mutable kind through the boundary.
		{`(string-append (string #\a) "b")`, String("ab"), false},
		{`(string-upcase (string #\a))`, String("A"), false},
		{`(string=? (string #\h #\i) "hi")`, true, false},
		{`(equal? (string->list "abc" 1) '(#\b #\c))`, true, false},
		{`(list->string '(#\a #\b))`, String("ab"), false},
		{`(string<? "abc" "abd" "b")`, true, false},
		{`(string-ci=? "ABC" "abc")`, true, false},
	})
}

func TestStringMapForEachAndCasing(t *testing.T) {
	runStdlibTests(t, []struct {
		expr      string
		expect    Value
		shouldErr bool
	}{
		{`(string-map char-upcase "abc")`, String("ABC"), false},
		// Multiple strings, shortest terminates, mixed representations.
		{`(string-map (lambda (a b) (if (char<? a b) a b)) "adc" (string #\b #\b #\z #\q))`, String("abc"), false},
		{`(let ((n 0))
		    (string-for-each (lambda (c) (set! n (+ n 1))) "hello")
		    n)`, Integer(5), false},
		{`(string-map car "abc")`, nil, true},
		{`(string-map char-upcase)`, nil, true},
		{`(string-map char-upcase 42)`, nil, true},
		// Full Unicode case mapping and folding (§6.7).
		{`(string-upcase "ßa")`, String("SSA"), false},
		{`(string-upcase "ǰ")`, String("J̌"), false},
		{`(string-downcase "İ")`, String("i̇"), false},
		{`(string-downcase "ΜΈΛΟΣ")`, String("μέλος"), false},
		{`(string-foldcase "Maß")`, String("mass"), false},
		{`(string-ci=? "Maß" "MASS")`, true, false},
	})
}
