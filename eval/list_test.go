package eval

import "testing"

func TestCxrForms(t *testing.T) {
	runStdlibTests(t, []struct {
		expr      string
		expect    Value
		shouldErr bool
	}{
		// Slice-backed literals and pair chains both work.
		{"(cadr '(1 2 3))", Integer(2), false},
		{"(caddr '(1 2 3))", Integer(3), false},
		{"(cadr (list 1 2 3))", Integer(2), false},
		{"(caar '((1 2) 3))", Integer(1), false},
		{"(cdar '((1 2) 3))", []Value{Integer(2)}, false},
		{"(cddr '(1 2 3 4))", []Value{Integer(3), Integer(4)}, false},
		{"(cadddr '(1 2 3 4))", Integer(4), false},
		{"(cddddr '(1 2 3 4))", []Value{}, false},
		// Dotted pairs.
		{"(cdar '((7 . 3) . 4))", Integer(3), false},
		// Too shallow errors.
		{"(cadr '(1))", nil, true},
		{"(caar '(1 2))", nil, true},
	})
}

func TestMemberFamily(t *testing.T) {
	runStdlibTests(t, []struct {
		expr      string
		expect    Value
		shouldErr bool
	}{
		// R7RS: the matching sublist, not a boolean.
		{"(memq 'a '(a b c))", []Value{Symbol("a"), Symbol("b"), Symbol("c")}, false},
		{"(memq 'b '(a b c))", []Value{Symbol("b"), Symbol("c")}, false},
		{"(memq 'a '(b c d))", false, false},
		// memq is identity: fresh lists are not eq?.
		{"(memq (list 'a) '(b (a) c))", false, false},
		// member is deep equality, and works across representations.
		{"(member (list 'a) '(b (a) c))", []Value{[]Value{Symbol("a")}, Symbol("c")}, false},
		{"(member 2 (cons 1 (cons 2 '())))", []Value{Integer(2)}, false},
		// memv: eqv? — numbers by value and exactness.
		{"(memv 101 '(100 101 102))", []Value{Integer(101), Integer(102)}, false},
		{"(memv 2.0 '(1 2 3))", false, false},
		// Optional comparator (member only).
		{`(member "B" '("a" "b" "c") string-ci=?)`, []Value{String("b"), String("c")}, false},
		{"(memq 'a '(a b) eq?)", nil, true},
		{"(member 'a 42)", nil, true},
	})
}

func TestAssocFamily(t *testing.T) {
	runStdlibTests(t, []struct {
		expr      string
		expect    Value
		shouldErr bool
	}{
		{"(assq 'a '((a 1) (b 2)))", []Value{Symbol("a"), Integer(1)}, false},
		{"(assq 'd '((a 1) (b 2)))", false, false},
		{"(assq (list 'a) '(((a)) ((b))))", false, false},
		{"(assoc (list 'a) '(((a)) ((b))))", []Value{[]Value{Symbol("a")}}, false},
		{"(assv 5 '((2 3) (5 7)))", []Value{Integer(5), Integer(7)}, false},
		// Comparator (assoc only), and cons-built entries.
		{"(assoc 2.0 '((1 1) (2 4)) =)", []Value{Integer(2), Integer(4)}, false},
		{"(cdr (assq 'b (list (cons 'a 1) (cons 'b 2))))", Integer(2), false},
		{"(assq 'a '(not-a-pair))", nil, true},
		{"(assoc 1 42)", nil, true},
	})
}

func TestListProcedures(t *testing.T) {
	runStdlibTests(t, []struct {
		expr      string
		expect    Value
		shouldErr bool
	}{
		{"(make-list 2 3)", []Value{Integer(3), Integer(3)}, false},
		{"(make-list 0)", []Value{}, false},
		// make-list is mutable: list-set! works on it.
		{"(let ((l (make-list 3 0))) (list-set! l 1 'x) l)", []Value{Integer(0), Symbol("x"), Integer(0)}, false},
		{"(list-tail '(a b c d e) 3)", []Value{Symbol("d"), Symbol("e")}, false},
		{"(list-tail '(a b) 2)", []Value{}, false},
		{"(list-tail '(a b) 3)", nil, true},
		{"(let ((l (list 1 2 3))) (list-set! l 0 'z) l)", []Value{Symbol("z"), Integer(2), Integer(3)}, false},
		// Slice-backed literals are immutable, like set-car!.
		{"(list-set! '(1 2 3) 0 'z)", nil, true},
		{"(list-copy '(1 2 3))", []Value{Integer(1), Integer(2), Integer(3)}, false},
		{"(list-copy '())", []Value{}, false},
		// Non-lists pass through; improper tails are preserved.
		{`(list-copy "foo")`, String("foo"), false},
		{"(cdr (list-copy (cons 3 4)))", Integer(4), false},
		{"(let* ((a (list 1 2)) (b (list-copy a))) (list-set! b 0 'z) (car a))", Integer(1), false},
		{"(let ((c (list-copy '(1 2)))) (list-set! c 1 'y) c)", []Value{Integer(1), Symbol("y")}, false},
	})
}

func TestMultiListMapForEach(t *testing.T) {
	runStdlibTests(t, []struct {
		expr      string
		expect    Value
		shouldErr bool
	}{
		// Single-list behavior unchanged.
		{"(map not (list #t #f))", []Value{false, true}, false},
		{"(map (lambda (x) x) 42)", nil, true},
		// Multiple lists, shortest terminates.
		{"(map + '(1 2 3) '(4 5 6 7))", []Value{Integer(5), Integer(7), Integer(9)}, false},
		{"(map * '(1 2) '(3 4) '(5 6))", []Value{Integer(15), Integer(48)}, false},
		// One circular list is fine while another is finite (§6.10).
		{`(let ((ls1 (list 10 100 1000)) (ls2 (list 1 2 3 4 5 6)))
		    (set-cdr! (cddr ls1) ls1)
		    (map * ls1 ls2))`,
			[]Value{Integer(10), Integer(200), Integer(3000), Integer(40), Integer(500), Integer(6000)}, false},
		{`(let ((x 0))
		    (for-each (lambda (a b) (set! x (+ x (* a b)))) '(1 2) '(3 4))
		    x)`, Integer(11), false},
		{"(for-each not)", nil, true},
		{"(for-each not 42)", nil, true},
	})
}

func TestEquivalencePredicates(t *testing.T) {
	runStdlibTests(t, []struct {
		expr      string
		expect    Value
		shouldErr bool
	}{
		{"(eqv? 'a 'a)", true, false},
		{"(eqv? 'a 'b)", false, false},
		{"(eqv? 100000000 100000000)", true, false},
		{"(eqv? 2 2.0)", false, false},
		{"(eqv? 2.0 2.0)", true, false},
		{"(eqv? #\\a #\\a)", true, false},
		{"(eqv? '() '())", true, false},
		{"(eqv? (cons 1 2) (cons 1 2))", false, false},
		{"(let ((p (cons 1 2))) (eqv? p p))", true, false},
		{"(eqv? car car)", true, false},
		{"(boolean=? #t #t)", true, false},
		{"(boolean=? #f #f #f)", true, false},
		{"(boolean=? #t #t #f)", false, false},
		{"(boolean=? #t 1)", nil, true},
		{"(boolean=? #t)", nil, true},
		{"(symbol=? 'a 'a 'a)", true, false},
		{"(symbol=? 'a 'b)", false, false},
		{"(symbol=? 'a \"a\")", nil, true},
	})
}
