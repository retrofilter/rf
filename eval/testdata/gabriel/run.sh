#!/bin/sh
# Side-by-side Gabriel timings: chibi-scheme (bytecode VM) vs rf (closure-compiled
# node tree, SCHEME.md Phase 10). The .scm files are self-timing R7RS programs that
# run unchanged on both. Setup, from the repo root:
#   git clone --depth 1 https://github.com/ashinn/chibi-scheme tmp/chibi-scheme && make -C tmp/chibi-scheme
#   go build -o tmp/rf .
#   eval/testdata/gabriel/run.sh
cd "$(dirname "$0")" || exit 1
ROOT=../../..
CHIBI=$ROOT/tmp/chibi-scheme
# wc and read1 (ecraven/r7rs-benchmarks) read the suite's own input files,
# fetched on first run into inputs/ (gitignored: bib is 4.4MB).
mkdir -p inputs
for f in bib parsing.data; do
  [ -s inputs/$f ] || curl -sL "https://raw.githubusercontent.com/ecraven/r7rs-benchmarks/master/inputs/$f" -o inputs/$f
done
printf '%-10s %10s %10s %8s\n' bench chibi-ms rf-ms ratio
for b in tak ctak nqueens deriv destruct fib wc read1; do
  c=$(LD_LIBRARY_PATH=$CHIBI CHIBI_MODULE_PATH=$CHIBI/lib $CHIBI/chibi-scheme $b.scm | awk '{print $2}')
  r=$($ROOT/tmp/rf $b.scm | awk '{print $2}')
  printf '%-10s %10.2f %10.2f %8.2fx\n' "$b" "$c" "$r" "$(echo "$r / $c" | bc -l)"
done
