package eval

import "testing"

func TestVectorBuiltins(t *testing.T) {
	runStdlibTests(t, []struct {
		expr      string
		expect    Value
		shouldErr bool
	}{
		{`(vector? #(1 2))`, true, false},
		{`(vector? (list 1 2))`, false, false},
		{`(vector-length (make-vector 3))`, Integer(3), false},
		{`(vector-ref (vector 1 2 3) 1)`, Integer(2), false},
		{`(vector-ref #(1) 5)`, nil, true},
		// vector-set! mutates through aliases (shared backing array).
		{`(let ((v (vector 1 2 3)) ) (vector-set! v 0 'x) (vector-ref v 0))`, Symbol("x"), false},
		{`(equal? (vector->list #(a b c) 1) '(b c))`, true, false},
		{`(equal? (vector->list #(a b c) 1 2) '(b))`, true, false},
		{`(vector->list #(a) 0 5)`, nil, true},
		{`(equal? (list->vector '(1 2)) #(1 2))`, true, false},
		{`(vector->string #(#\h #\i))`, String("hi"), false},
		{`(vector->string #(1))`, nil, true},
		{`(equal? (string->vector "hi") #(#\h #\i))`, true, false},
		{`(equal? (vector-copy #(a b c) 1) #(b c))`, true, false},
		{`(let ((v (vector 1 2 3 4 5))) (vector-copy! v 1 v 0 2) (equal? v #(1 1 2 4 5)))`, true, false},
		{`(equal? (vector-append #(a) #(b c)) #(a b c))`, true, false},
		{`(let ((v (make-vector 3 0))) (vector-fill! v 'x 1) (equal? v #(0 x x)))`, true, false},
		{`(equal? (vector-map + #(1 2) #(10 20 30)) #(11 22))`, true, false},
		{`(let ((n 0)) (vector-for-each (lambda (x) (set! n (+ n x))) #(1 2 3)) n)`, Integer(6), false},
	})
}

func TestBytevectorBuiltins(t *testing.T) {
	runStdlibTests(t, []struct {
		expr      string
		expect    Value
		shouldErr bool
	}{
		{`(bytevector? #u8(1 2))`, true, false},
		{`(bytevector? #(1 2))`, false, false},
		{`(bytevector-length (make-bytevector 4 255))`, Integer(4), false},
		{`(bytevector-u8-ref #u8(9 8) 1)`, Integer(8), false},
		{`(bytevector 0 256)`, nil, true},
		{`(let ((bv (bytevector 0 1 2))) (bytevector-u8-set! bv 1 255) (equal? bv #u8(0 255 2)))`, true, false},
		{`(equal? (bytevector-copy #u8(0 1 2) 1) #u8(1 2))`, true, false},
		{`(equal? (bytevector-append #u8(0) #u8(1 2)) #u8(0 1 2))`, true, false},
		{`(utf8->string #u8(206 187))`, String("λ"), false},
		{`(utf8->string #u8(255))`, nil, true},
		{`(equal? (string->utf8 "λ") #u8(206 187))`, true, false},
		{`(equal? (string->utf8 "abc" 1 2) #u8(98))`, true, false},
	})
}
