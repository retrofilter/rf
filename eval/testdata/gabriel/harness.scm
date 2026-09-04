;; Shared self-timing driver: (bench "name" iterations thunk) prints ms per iteration.
(define (bench name n thunk)
  (thunk) ; warm up
  (let ((t0 (current-jiffy)))
    (let loop ((i 0))
      (if (< i n) (begin (thunk) (loop (+ i 1)))))
    (let* ((t1 (current-jiffy))
           (ms (/ (* 1000.0 (- t1 t0)) (* n (jiffies-per-second)))))
      (display name) (display " ") (display ms) (newline))))
