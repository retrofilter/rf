(import (scheme base) (scheme write) (scheme time))
(include "harness.scm")
(define (tak x y z)
  (if (not (< y x))
      z
      (tak (tak (- x 1) y z)
           (tak (- y 1) z x)
           (tak (- z 1) x y))))
(bench "tak" 20 (lambda () (tak 18 12 6)))
