;;; READ1 -- ecraven/r7rs-benchmarks src/read1.scm: read every datum of
;;; inputs/parsing.data (a 28KB Scheme program) with the reader, returning the
;;; last one. Exercises the datum reader — what load and include go through.
(import (scheme base) (scheme file) (scheme read) (scheme write) (scheme time))
(include "harness.scm")
(define (read-from-file-benchmark input)
  (call-with-input-file
      input
    (lambda (in)
      (do ((x (read in) (read in))
           (y #f x)
           (i 0 (+ i 1)))
          ((eof-object? x) y)))))
(define result (read-from-file-benchmark "inputs/parsing.data"))
(if (not (equal? result '(should return this list)))
    (begin (display "read1: wrong result ") (write result) (newline)))
(bench "read1" 20 (lambda () (read-from-file-benchmark "inputs/parsing.data")))
