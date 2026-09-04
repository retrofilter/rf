;;; WC -- One of the Kernighan and Van Wyk benchmarks (ecraven/r7rs-benchmarks
;;; src/wc.scm, Will Clinger's rewrite): read-char over inputs/bib, counting
;;; lines, words, and characters. A per-character port + call benchmark.
(import (scheme base) (scheme char) (scheme file) (scheme write) (scheme time))
(include "harness.scm")
(define (wcport port)
  (define (loop nl nw nc inword?)
    (let ((x (read-char port)))
      (cond ((eof-object? x)
             (list nl nw nc))
            ((char=? x #\space)
             (loop nl nw (+ nc 1) #f))
            ((char=? x #\newline)
             (loop (+ nl 1) nw (+ nc 1) #f))
            (else
             (loop nl (if inword? nw (+ nw 1)) (+ nc 1) #t)))))
  (loop 0 0 0 #f))
(define (go x) (call-with-input-file x wcport))
(define result (go "inputs/bib"))
(if (not (equal? result '(31102 851820 4460056)))
    (begin (display "wc: wrong result ") (write result) (newline)))
(bench "wc" 2 (lambda () (go "inputs/bib")))
