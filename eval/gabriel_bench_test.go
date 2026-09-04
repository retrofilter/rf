package eval

import "testing"

func benchProgram(b *testing.B, setup, expr string, want string) {
	b.Helper()
	ev := NewEvaluator()
	env := ev.globalEnv
	if setup != "" {
		setupExprs, err := ParseAll(setup)
		if err != nil {
			b.Fatalf("parse setup: %v", err)
		}
		if _, err := ev.EvalAll(setupExprs, env); err != nil {
			b.Fatalf("eval setup: %v", err)
		}
	}
	exprs, err := ParseAll(expr)
	if err != nil {
		b.Fatalf("parse: %v", err)
	}
	got, err := ev.EvalAll(exprs, env)
	if err != nil {
		b.Fatalf("eval: %v", err)
	}
	if PrintValue(got) != want {
		b.Fatalf("result changed: got %s, want %s", PrintValue(got), want)
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := ev.EvalAll(exprs, env); err != nil {
			b.Fatalf("eval: %v", err)
		}
	}
}

func BenchmarkGabrielTak(b *testing.B) {
	benchProgram(b, `
		(define (tak x y z)
		  (if (not (< y x))
		      z
		      (tak (tak (- x 1) y z)
		           (tak (- y 1) z x)
		           (tak (- z 1) x y))))`,
		`(tak 18 12 6)`, "7")
}

func BenchmarkGabrielNqueens(b *testing.B) {
	benchProgram(b, `
		(define (nqueens n)
		  (define (one-to n)
		    (let loop ((i n) (l '()))
		      (if (= i 0) l (loop (- i 1) (cons i l)))))
		  (define (try-it x y z)
		    (if (null? x)
		        (if (null? y) 1 0)
		        (+ (if (ok? (car x) 1 z)
		               (try-it (append (cdr x) y) '() (cons (car x) z))
		               0)
		           (try-it (cdr x) (cons (car x) y) z))))
		  (define (ok? row dist placed)
		    (if (null? placed)
		        #t
		        (and (not (= (car placed) (+ row dist)))
		             (not (= (car placed) (- row dist)))
		             (ok? row (+ dist 1) (cdr placed)))))
		  (try-it (one-to n) '() '()))`,
		`(nqueens 8)`, "92")
}

func BenchmarkGabrielDeriv(b *testing.B) {
	benchProgram(b, `
		(define (deriv-aux a) (list '/ (deriv a) a))
		(define (deriv a)
		  (cond
		    ((not (pair? a))
		     (cond ((eq? a 'x) 1) (else 0)))
		    ((eq? (car a) '+)
		     (cons '+ (map deriv (cdr a))))
		    ((eq? (car a) '-)
		     (cons '- (map deriv (cdr a))))
		    ((eq? (car a) '*)
		     (list '* a (cons '+ (map deriv-aux (cdr a)))))
		    ((eq? (car a) '/)
		     (list '-
		           (list '/ (deriv (cadr a)) (caddr a))
		           (list '/ (cadr a)
		                 (list '* (caddr a) (caddr a) (deriv (caddr a))))))
		    (else 'error)))`,
		`(do ((i 0 (+ i 1))) ((= i 200) 'done)
		   (deriv '(+ (* 3 x x) (* a x x) (* b x) 5)))`, "done")
}

func BenchmarkGabrielDestruct(b *testing.B) {
	benchProgram(b, `
		(define (my-append! x y)
		  (if (null? x)
		      y
		      (do ((a x b)
		           (b (cdr x) (cdr b)))
		          ((null? b)
		           (set-cdr! a y)
		           x))))
		(define (destructive n m)
		  (let ((l (do ((i 10 (- i 1))
		                (a '() (cons '() a)))
		               ((= i 0) a))))
		    (do ((i n (- i 1)))
		        ((= i 0))
		      (cond ((null? (car l))
		             (do ((l l (cdr l)))
		                 ((null? l))
		               (or (car l)
		                   (set-car! l (cons '() '())))
		               (my-append! (car l)
		                      (do ((j m (- j 1))
		                           (a '() (cons '() a)))
		                          ((= j 0) a)))))
		            (else
		             (do ((l1 l (cdr l1))
		                  (l2 (cdr l) (cdr l2)))
		                 ((null? l2))
		               (set-cdr! (do ((j (quotient (length (car l2)) 2) (- j 1))
		                            (a (car l2) (cdr a)))
		                           ((zero? j) a)
		                         (set-car! a i))
		                       (let ((n (quotient (length (car l1)) 2)))
		                         (cond ((= n 0) (set-car! l1 '())
		                                (car l1))
		                               (else
		                                (do ((j n (- j 1))
		                                     (a (car l1) (cdr a)))
		                                    ((= j 1)
		                                     (let ((x (cdr a)))
		                                            (set-cdr! a '())
		                                          x))
		                                  (set-car! a i))))))))))))`,
		`(begin (destructive 600 50) 'done)`, "done")
}
