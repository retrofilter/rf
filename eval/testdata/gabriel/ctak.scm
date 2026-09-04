(import (scheme base) (scheme write) (scheme time))
(include "harness.scm")
(define (ctak x y z)
  (call-with-current-continuation
   (lambda (k) (ctak-aux k x y z))))
(define (ctak-aux k x y z)
  (cond ((not (< y x)) (k z))
        (else (call-with-current-continuation
               (ctak-aux
                k
                (call-with-current-continuation
                 (lambda (k) (ctak-aux k (- x 1) y z)))
                (call-with-current-continuation
                 (lambda (k) (ctak-aux k (- y 1) z x)))
                (call-with-current-continuation
                 (lambda (k) (ctak-aux k (- z 1) x y))))))))
(bench "ctak" 10 (lambda () (ctak 18 12 6)))
