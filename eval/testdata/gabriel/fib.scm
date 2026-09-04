(import (scheme base) (scheme write) (scheme time))
(include "harness.scm")
(define (fib n) (if (< n 2) n (+ (fib (- n 1)) (fib (- n 2)))))
(bench "fib30" 3 (lambda () (fib 30)))
