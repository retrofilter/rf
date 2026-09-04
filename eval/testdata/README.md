# testdata

- `r7rs-tests.scm` — the R7RS-small conformance suite, vendored verbatim from
  [chibi-scheme](https://github.com/ashinn/chibi-scheme) `tests/r7rs-tests.scm`
  (BSD 3-clause, see chibi's COPYING). Driven by `eval/r7rs_test.go` as a
  ratchet: per-section pass counts are pinned in `r7rs_status.txt` and must
  never regress. After improving conformance, regenerate the status file with:

      RF_UPDATE_R7RS=1 go test ./eval -run TestR7RS

- `r7rs_status.txt` — the checked-in ratchet state (pass counts per suite
  section). Machine-written; do not edit by hand.
