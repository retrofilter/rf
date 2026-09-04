package eval

import "testing"

func BenchmarkFormGuard(b *testing.B) {
	benchProgram(b, `
		(define (g n acc)
		  (if (= n 0) acc
		      (g (- n 1) (guard (e (#t acc)) (+ acc 1)))))`,
		`(g 20000 0)`, "20000")
}

func BenchmarkFormGuardRaise(b *testing.B) {
	benchProgram(b, `
		(define (g n acc)
		  (if (= n 0) acc
		      (g (- n 1) (guard (e ((symbol? e) (+ acc 1))) (raise 'oops)))))`,
		`(g 20000 0)`, "20000")
}

func BenchmarkFormParameterize(b *testing.B) {
	benchProgram(b, `
		(define p (make-parameter 0))
		(define (h n acc)
		  (if (= n 0) acc
		      (h (- n 1) (parameterize ((p n)) (+ acc (p))))))`,
		`(h 20000 0)`, "200010000")
}

func BenchmarkFormLetValues(b *testing.B) {
	benchProgram(b, `
		(define (lv n acc)
		  (if (= n 0) acc
		      (let-values (((a b) (values n acc)) ((c . rest) (values 1 2 3)))
		        (lv (- a 1) (+ a b c)))))`,
		`(lv 20000 0)`, "200030000")
}

func BenchmarkFormLetStarValues(b *testing.B) {
	benchProgram(b, `
		(define (lv n acc)
		  (if (= n 0) acc
		      (let*-values (((a b) (values n acc)) ((c) (values (+ a b))))
		        (lv (- a 1) c))))`,
		`(lv 20000 0)`, "200010000")
}

func BenchmarkFormDefineValues(b *testing.B) {
	benchProgram(b, `
		(define (dv n acc)
		  (define-values (a b) (values n acc))
		  (if (= a 0) b (dv (- a 1) (+ a b))))`,
		`(dv 20000 0)`, "200010000")
}

func BenchmarkFormCaseLambda(b *testing.B) {
	benchProgram(b, `
		(define (cl n acc)
		  (if (= n 0) acc
		      (let ((f (case-lambda ((x) x) ((x y) (+ x y)))))
		        (cl (- n 1) (f acc 1)))))`,
		`(cl 20000 0)`, "20000")
}

func BenchmarkFormDelayForce(b *testing.B) {
	benchProgram(b, `
		(define (df n acc)
		  (if (= n 0) acc
		      (df (- n 1) (force (delay (+ acc 1))))))`,
		`(df 20000 0)`, "20000")
}

func BenchmarkFormDefineRecordType(b *testing.B) {
	benchProgram(b, `
		(define (dr n acc)
		  (define-record-type <pt> (mk x) pt? (x pt-x))
		  (if (= n 0) acc (dr (- n 1) (+ acc (pt-x (mk 1))))))`,
		`(dr 20000 0)`, "20000")
}

func BenchmarkFormLetSyntax(b *testing.B) {
	benchProgram(b, `
		(define (ls n acc)
		  (if (= n 0) acc
		      (let-syntax ((inc (syntax-rules () ((_ x) (+ x 1)))))
		        (ls (- n 1) (inc acc)))))`,
		`(ls 20000 0)`, "20000")
}
