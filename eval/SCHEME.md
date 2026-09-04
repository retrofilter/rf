# SCHEME.md — R7RS-small conformance plan

Goal: extend rf's interpreter (`eval/`) to support all of R7RS-small (r7rs.pdf, July 2013).
This is a gap analysis (spec Appendix A export lists vs the current `eval/` inventory)
turned into an ordered task list. rf stays rf: conformance is additive, and the shell
DSL, streams, dictionaries, and pipe threading are non-negotiable extensions.

## Design constraints (decided)

- **Dictionaries and keywords stay.** `{:label "Foo"}` and `:keyword` syntax are
  retained as reader extensions. This is conformant: R7RS-small §1.3.3 reserves
  `[ ] { }` for future language extensions, so an implementation may claim them.
  Consequences to handle deliberately: `equal?`, `write`/`display`, and quasiquote
  must have defined (documented) behavior over dictionaries, keywords, and streams,
  even though the spec is silent on them.
- **The pipe DSL stays, and new builtins should join it.** `pipe` threads the piped
  value in as the *last* argument (or at `_`). R7RS is already collection-last for
  its higher-order procedures — `(map proc list)`, `(for-each proc list)`,
  `(vector-map proc vec)`, `(string-map proc str)`, `(member obj list)`,
  `(assoc obj alist)` — so those work as pipeline stages with their exact spec
  signatures. Rule: the R7RS signature always wins at the Scheme layer; pipe
  friendliness comes from `CommandMeta` Stage declarations and the `_` placeholder,
  never from reordering spec arguments. Subject-first accessors (`string-ref`,
  `vector-ref`, `list-tail`) thread via `_` when needed.
- **Existing rf builtins keep their names.** Where an rf name collides with an R7RS
  name, the R7RS semantics win (see the collision audit in Phase 8). Non-standard
  builtins (`grep`, `where`, `similar`, graph/project/llm words…) are untouched.
- **Truthiness already matches**: only `#f` is false (`isFalsy`); nil is truthy. Keep.
- **Performance is a goal, not a casualty.** This should remain a highly performant
  implementation within its constraints (a tree-walking Go interpreter, kept
  flexible). Conformance work must preserve the existing fast paths: the
  zero-allocation TCO cell, slice-backed list iteration in hot builtins, lazy
  streams with early termination, and prompt-cache-friendly table rendering. Every
  decision below was made with this in mind — new type machinery lives behind
  coercion boundaries (the pattern streams already use) so the common case pays
  nothing; benchmark-sensitive changes (pairs, the numeric split) land with
  before/after `go test -bench` numbers on list-heavy pipelines.

## Decisions (settled)

- **D1 — Numbers: int64 exact + float64 inexact; overflow promotes to inexact;
  no rationals, complex, or bignums.** An `Integer`/`Float` pair covers everything
  a shell touches (sizes, counts, exit codes, timestamps). R7RS permits a limited
  exact range when documented, so skipping bignums is legal — we just don't claim
  the `exact-closed` feature. Overflow promoting to inexact is exactly today's
  float64 behavior, so nothing regresses; erroring would be a regression.
  `math/big` is rejected on performance grounds: it would put an allocation and
  indirection into every arithmetic op for a case a shell essentially never hits —
  int64 fast-path arithmetic stays branch-cheap. Exact integers print without
  `.0`, which fixes table number formatting permanently. Ripple: every numeric
  builtin, `eqv?`/`=` across the split, radix support in
  `number->string`/`string->number`.
- **D2 — Pairs: real `*Pair` cons cells as the canonical *data* list type,
  migrated in two stages.** There is no conformant way to fake `set-car!`
  visibility and dotted pairs on Go slices, so `*Pair` must exist. Stage one:
  all list builtins produce/consume pairs, with `AsList`-style coercion keeping
  slices, streams, and rows working unchanged (the same trick streams use) —
  slice-backed rows keep their contiguous fast path for tables and the row verbs,
  so pipelines over big row sets don't pay pointer-chasing costs. `quote`/
  `quasiquote` convert slice-AST to pairs at the boundary. Stage two: migrate the
  evaluator AST itself only when `syntax-rules` and `eval` force it (Phases 4/7)
  — they are the only consumers that truly need code-as-pairs. This keeps the
  big-bang refactor off the critical path.
- **D3 — Strings: a second, `[]rune`-backed mutable string type; literals stay
  immutable Go strings.** Both satisfy `string?`; only `make-string`/`string`/
  `string-copy` allocate the mutable kind; `string-set!` on a literal errors,
  which the spec sanctions ("it is an error" to mutate literals). The existing
  Go-string hot path — every builtin, the tokenizer, streams — is untouched;
  `[]rune` backing gives correct character indexing that byte-indexed Go strings
  can't. Coercion at builtin boundaries.
- **D4 — call/cc: escape-only continuations (panic/recover with a token). Full
  re-entrant call/cc is explicitly out of scope.** Escape-only covers what
  continuations are used for in practice — early exit, the exception machinery,
  `guard`, non-local loops — and composes with `dynamic-wind` and the existing
  interrupt/unwind story. A CPS transform would destroy the zero-allocation TCO
  design (every call would allocate a continuation) for a feature no shell user
  invokes. This is the road most embedded Schemes take. Document the deviation:
  re-invoking a continuation after its extent has exited errors. Chibi's few
  re-entrant call/cc tests are marked expected-skip.
- **D5 — Libraries: a thin aliasing layer over the one global env.** The shared
  live environment is rf's product — user and LLM deliberately share one
  namespace — so isolated library environments would fight the thesis. `import`
  with `only`/`except`/`prefix`/`rename` becomes controlled aliasing into the
  global env; `define-library` evaluates its body and records an export set; all
  `(scheme *)` imports are preloaded no-ops (zero cost at startup and at import);
  `cond-expand`/`features` implemented honestly. Enough to run conformance-suite
  programs and third-party `.scm` files, which is the goal. Documented: user
  libraries share one store underneath.
- **D6 — Value representation: keep `type Value interface{}`; no tagged-struct
  rewrite.** Profiled on `BenchmarkFibEval` (fib(30), ~2.7M calls — a boxing
  worst case): one run allocates 588MB / 15.6M allocations, split 51% per-call
  `Environment` frames (`newCallEnv`), 44% per-call `args []Value` slices
  (`evalList` — the frame takes ownership of the args slice, so these two are
  really one design), and only ~5% `Number` boxing in the arithmetic builtins.
  The CPU profile is GC work driven by that allocation rate; the interpreter
  logic itself barely registers. A tagged struct would eliminate the 5%, grow
  the 44% (24+ bytes/element vs 16), rewrite every builtin, and buy nothing on
  dispatch — type switches are ~1ns tag checks, not reflection (the core's
  only `reflect` is `identityEq`'s cold path). NaN-boxing is impossible under
  Go's precise GC. Interface values are what Starlark-go/Tengo/goja ship.
  Revisit only if, after the Phase 2.5 optimizations land, a profile still
  shows boxing >15–20% of allocations.

## Phase 0 — conformance harness (done)

- [x] Vendored chibi-scheme's `tests/r7rs-tests.scm` under `eval/testdata/`,
  driven by `eval/r7rs_test.go`: a lexical splitter isolates each top-level form
  (so unparseable forms fail alone), test/test-values/test-error/test-assert run
  as recording special forms, and per-section pass counts ratchet against
  `eval/testdata/r7rs_status.txt` — regressions fail, progress asks for
  `RF_UPDATE_R7RS=1 go test ./eval -run TestR7RS`. Baseline was 251 passing;
  Phase 1 lifted it to 272.
- [x] Bonus fix the harness found on first run: `eq?` used Go `==` on interface
  values and **panicked** on lists and builtins (`(eq? '(1) '(1))` crashed the
  shell); now `identityEq` with pointer identity for slices/maps/funcs. Also
  `equal?` learned dictionaries and keywords.

## Phase 1 — reader / lexer (`eval/` parser) — done except D2/Phase-6 holdovers

- [x] Booleans are reader datums: `#t`/`#f`/`#true`/`#false` parse to bool (they
  were *environment bindings*, so `'#t` used to evaluate to a symbol)
- [x] Character literals: `#\a` (any rune, `#\(` and `#\λ` included), the nine
  R7RS names, hex `#\x41` — new self-evaluating `Char` type, printed in write form
- [x] Numeric literal syntax: radix prefixes `#b #o #d #x`, exactness prefixes
  `#e #i` (accepted, spelling no-ops until D1), `+inf.0`/`-inf.0`/`+nan.0`;
  Go-isms strconv accepted but Scheme must not (`inf`, `nan` as bare words) are
  now symbols
- [x] Dotted-pair notation `(a . b)` in the reader and in lambda formals
  (landed with D2: improper data parses to `*Pair` chains, a dotted proper
  tail normalizes back to a slice list, and `(lambda (a . rest))` /
  `(lambda args)` / `(define (f a . rest))` all bind rest parameters)
- [x] Vector literals `#(...)` and bytevector literals `#u8(...)` — self-evaluating
  `Vector`/`Bytevector` types, `equal?`-aware, printed in reader syntax
- [x] Datum comments `#;` (any datum position) and nested `#| ... |#` block comments
- [x] `|identifier with spaces|` symbol syntax with escapes
- [x] String escapes: full R7RS set incl. `\xHH;` and line continuation; invalid
  escapes still keep raw content (command-mode regexes/paths rely on it); fixed
  the tokenizer mis-scanning strings ending in an escaped backslash (`"a\\"`)
- [x] `SchemeComplete` (multi-line REPL entry) understands block comments, char
  literals, and `|symbols|`, so `#\(` no longer counts as an open paren
- [x] Datum labels `#n=` / `#n#` (landed with Phase 6: a label table threaded
  through `parseDatum` with in-place placeholder patching, so `#0=(1 . #0#)`
  builds genuinely circular pairs; a bare/misplaced `.` is now a parse error,
  which the suite's read-error tests require)
- [x] Guard: dict `{...}` syntax and `:keywords` tokenize exactly as before
  (existing tests pass unchanged); `;` is now an atom delimiter (`abc;x` was one
  atom before, now comment rules apply uniformly)

## Phase 2 — core data types — done

- [x] **Exact/inexact numbers** (D1): type split (`Integer` int64 exact
  beside `Number` float64 inexact), all arithmetic revisited with
  overflow-promotes-to-inexact (`eval/number.go`), `exact`, `inexact`,
  `exact?`, `inexact?`, `exact-integer?`, `integer?`/`real?`/`rational?`/
  `complex?`, `number->string` / `string->number` with radix arg riding the
  reader's own number scanner (never a partial parse), `round` now ties to
  even, min/max exactness contagion, integral shell producers (sizes,
  counts, ids, exit codes, JSON/CSV cells) exact. Bonus: fib(30) allocs
  dropped 15.6M→12.1M (small exact ints box free via Go's static table)
- [x] **Characters** (`eval/char.go`): base predicates/conversions (`char?`,
  `char->integer`, `integer->char`, the variadic `char=?`/`char<?` chain
  family) plus the whole `(scheme char)` surface early — `char-ci` chains,
  `char-alphabetic?/-numeric?/-whitespace?/-upper-case?/-lower-case?` via
  Go's unicode tables, `char-upcase/downcase/foldcase` (simple folding:
  up-then-down), `digit-value` over the Nd ranges. 6.6 Characters passes
  completely (0→79)
- [x] **Pairs** (D2 stage one): real `*Pair` cons cells (`eval/pair.go`) —
  `cons`/`list` build mutable chains (set-cdr! visible through aliases, per
  the suite), `set-car!`/`set-cdr!` (slice-backed lists — literals and
  fresh builtin results — error: mutating literals is "an error" per spec),
  improper lists, cycle-safe `list?`/`length` (Floyd), `append` with
  non-list final argument sharing the tail, `equal?`/print/quasiquote
  pair-aware, `normalizeList` coercion at every []Value input boundary
  (rank/serialize/rows/shell/table), slices keep the row fast path
  (BenchmarkListPipeline unchanged: ~1.6ms/25k allocs)
- [x] **Vectors** (`eval/vector.go`): the full §6.8 set with start/end args
  (`startEnd` shared across the copy/fill/conversion builtins),
  `vector-map`/`vector-for-each` over multiple vectors with shortest-length
  termination, overlap-safe `vector-copy!`. 6.8 passes 1→42 (the one
  remaining failure needs Phase 5's `acos`)
- [x] **Bytevectors** (`eval/vector.go`): the full §6.9 set;
  `utf8->string` validates and errors on bad UTF-8, `string->utf8`'s
  start/end are character indexes per spec. 6.9 passes completely (0→39)
- [x] **Mutable strings** (D3, `eval/mutstring.go`): `*MutableString`
  ([]rune-backed) beside the immutable Go-string type, allocated only by
  `make-string`/`string`/`string-copy`; `string-set!`/`string-fill!`/
  `string-copy!` mutate in place, on a literal they error naming the
  allocating builtins. Both kinds satisfy `string?`, compare by content in
  `equal?`/`string=?`, and meet at the coercion boundaries (`stringText`,
  `AsString`, `PrintValue`) — the Go-string hot path is untouched. Also
  landed here (pulled forward from Phase 5, same machinery): `string-ref`,
  `string->list`/`list->string` with start/end, the variadic
  `string<?`-family and `string-ci` chains, `string-foldcase`. 6.7 passes
  40→125; the 5 stragglers need full Unicode case mapping (ß→SS), left for
  the Phase 5 audit
- [x] **EOF object** (`eval/values.go`): `eof-object`/`eof-object?`,
  disjoint singleton, prints `#<eof>` — Phase 6 ports return the same one
- [x] **Records** (`eval/record.go`): `define-record-type` as a special
  form; each definition is a disjoint type (predicate checks the type
  descriptor pointer, not just *Record), constructor may name a field
  subset, `equal?` is identity. Bindings go through `env.Set`, not
  `SetBuiltin`, so session-defined names never touch the package-level
  command registry. Records print `#<pare x: 1 y: 2>` (inspectable-values
  rule)
- [x] **Promises** (`eval/promise.go`): `delay`/`delay-force` as special
  forms, `force`/`make-promise`/`promise?` — memoizing, re-entrant-safe
  (a promise forced during its own computation keeps the first result, per
  spec), and SRFI-45-safe: `force` is a trampoline that chases
  `delay-force` chains iteratively (a 100k-deep chain runs in constant
  control stack, pinned in `eval/promise_test.go`); the loop polls the
  interrupt flag
- [x] **Multiple values** (`eval/values.go`): `values`/`call-with-values`
  with a `*MultipleValues` container that exists only for zero or ≥2
  values — `(values x)` is just `x`, so the rest of the interpreter never
  sees it. The r7rs harness's `test-values` compares containers
  element-wise with the float tolerance

## Phase 2.5 — allocation optimizations (after the D1/D2 churn settles) — done

The measured hotspots behind D6, attacked as local changes once the two big
refactors stop moving the ground (doing them earlier means redoing them).
Each lands with before/after `go test -bench -benchmem` numbers.

- [x] **Call-frame freelist** (the 51%, `eval/frame.go`): the planned
  syntactic "body contains a nested lambda?" check became **dynamic
  capture-marking**, which is both precise and immune to the scan's holes
  (macros expanding to lambda at runtime, aliased builtins): every place a
  `*Environment` is stored somewhere outliving the call sets a `captured`
  flag, walking the parent chain — exhaustively: `lambda`/`define`'s
  function form, `delay`/`delay-force`, and `where`'s lazy stream branch
  (the one builtin retaining its env; all other callable-takers use env
  eagerly). The Eval loop owns each frame a lambda call builds (a `fresh`
  bit set by `tailFrame`, packed with `captured` in Environment padding)
  and releases the previous one to a per-evaluator pool on adopting the
  next or returning; `releaseFrame` skips captured frames. A
  `((lambda ...) args)` head — what `let` expands to — is consumed by that
  one call, so evalList builds it unmarked; escapes from its body mark
  through the same sites. The pool hides behind a pointer so
  `callFunction`'s throwaway evaluators stay small (their nil pool just
  disables recycling). Regression tests churn the pool against every
  capture route (`eval/frame_test.go`).
- [x] **Arg-slice reuse** (the 44%): fixed-arity lambda calls now evaluate
  their arguments straight into the pooled frame's recycled `vals` array
  (`callLambda` in `eval/interpreter.go`); variadic frames hand their
  `bindArgs` slice to the pool on release. Builtin arg slices stay fresh —
  builtins like `list` retain them.
- [x] **Pre-boxed small integers**: `smallInts`/`boxInt` in
  `eval/number.go`, applied at the exact-arithmetic result sites
  (`numAdd2`/`numSub2`/`numMul2`/`numDiv2`). Isolated A/B: ~nothing on
  fib (its small values sit in Go's 0–255 static table, its results
  past 1024), −29% allocs / −6% time on a negative-counter loop
  (`(count 1000 0)` down to −1000) — the negatives-and-up-to-1024 range
  the table exists for.
- [x] **`BenchmarkListPipeline`** landed earlier as the D2 baseline
  (`eval/bench_test.go`), per the landing order.

Measured (Apple M4, count 3): `BenchmarkFibEval` 649ms / 560MB / 12.12M
allocs → 570ms / 215MB / 6.73M allocs per op (−12% time, −62% bytes,
−44% allocs — the recycled frames and their args slices; the remaining
allocations are builtin arg slices, out of scope). `BenchmarkListPipeline`
1.58ms / 2.01MB / 25025 allocs → 1.64ms / 2.09MB / 25025 (~+3%: the row
verbs don't use frames — the delta is the `Evaluator` struct crossing a
size class for `callFunction`'s per-call throwaway evaluators, itself a
pre-existing wart worth fixing on its own; noted for the D6 revisit).

## Phase 3 — special forms, control, exceptions — done

Ratchet 581 → 631. New callables (`*Continuation`, `*Parameter`,
`*CaseLambda`) are dispatched by `evalList` and by the now-unified
`Apply`/`callFunction` (one implementation; `apply` delegates too);
`procedure?` and rows' old `isCallable` share the one predicate.

- [x] **Fix `and` / `or`**: now short-circuiting special forms with the last
  expression in tail position (were eager builtins evaluating every argument;
  `(or)` also returned `#t` instead of `#f`).
- [x] `if` without else (2-arg form; result unspecified → nil)
- [x] `letrec*` — registered on letrec's existing expansion, which already
  assigns sequentially (letrec* semantics, a valid refinement of letrec's
  unspecified order); internal defines verified against §5.3.2 (bodies bind
  in order into the call frame — pinned in `eval/control_test.go`)
- [x] `case`: `=>` clauses (datum and else forms, receiver applied to the
  key); the `_case_tmp` expansion — and cond's `_cond_tmp`, do's `_do_loop` —
  now use `gensym` (interpreter.go), so expansion temporaries can't capture
  user bindings. Full hygiene waits for Phase 4; datum matching still rides
  `member`/equal? until Phase 5's `memv`.
- [x] `do`: `go vet ./...` is clean — the nit had already been cleared
- [x] `define-values`, `let-values`, `let*-values` (`eval/values.go`): special
  forms sharing lambda's `parseFormals`, so `(define-values (a b . rest) ...)`
  and symbol formals work; let-values evaluates inits in the outer env,
  let*-values in the accumulating child; bodies are their own scope with the
  last form in tail position
- [x] `make-parameter` / `parameterize` (`eval/control.go`): the dynamic
  extent lives as a value stack *on each parameter object* rather than the
  evaluator — immune to `callFunction`'s throwaway evaluators (a parameter
  read inside `map` sees the parameterize) — with converters applied at
  creation and rebind, and pops running on error unwinds. Phase 6's
  current-input/output/error-port build on this.
- [x] `dynamic-wind` (`eval/control.go`): needs no winder stack under D4 —
  every dynamic-wind frame sits on the Go stack between a raise point and its
  catch point, so after-thunks run as the unwind error propagates through.
  On a ctrl+c unwind the after thunk runs with the interrupt flag briefly
  cleared, then the flag re-arms and the unwind continues (a second ctrl+c
  still kills a runaway after thunk).
- [x] **Exceptions** (`eval/exception.go`): a raised condition travels as a
  Go error (`*raisedError`), so uncaught exceptions surface to the shell and
  LLM tool loop exactly as before; guard and with-exception-handler wrap any
  other Go error into an `*ErrorObject` at their boundaries (existing builtin
  failures are now catchable; `*fs.PathError` classifies as `file-error?`,
  `read-error?` arrives with Phase 6's `read`). `ErrInterrupted` and
  continuation unwinds are uncatchable barriers. The handler stack lives on
  the session Evaluator (builtins close over it, the execBuiltin pattern):
  `raise-continuable` runs the innermost handler *at the raise point* and
  resumes with its value; non-continuable `raise` unwinds, and
  `with-exception-handler` invokes its handler when the condition reaches its
  thunk boundary (a non-continuable handler can only escape or re-raise, so
  post-unwind invocation is observationally equivalent — the deviation:
  dynamic-wind afters fire first). `guard` pushes a stack marker so an inner
  guard wins over an outer handler and re-raising propagates correctly
  (SRFI-34 examples #5–#9 pass).
- [x] `call/cc` / `call-with-current-continuation` (D4, escape-only,
  `eval/control.go`): invoking the continuation unwinds via a token-carrying
  error the capturing frame recognizes; the token dies when the extent exits,
  so a saved continuation errors instead of resuming. Multiple values pass
  through as a values container. The suite's re-entrant dynamic-wind test
  fails as expected under D4.
- [x] `case-lambda` (`eval/control.go`): clauses compile to
  Lambdas once at evaluation; dispatch is by syntactic argument count, first
  matching clause in order (the suite's dead-clause test), entering
  `callLambda` so clause bodies keep full TCO (pinned: 100k self-calls).
- [x] `syntax-error` (raises an error object from its message + forms)

## Phase 4 — hygienic macros — done

Ratchet 631 → 656; suite section 4.3 Macros 0 → 24 of 25 (the holdout is
the aux-shadowing deviation below). D2 stage two turned out **not** to be
needed: the matcher/expander work over both list representations through
one decomposition boundary (`listParts`/`rebuildList` in `eval/syntax.go`),
so the slice AST stays; revisit only if `eval` (Phase 7) forces it.

- [x] `define-syntax` + `syntax-rules` (`eval/syntax.go`): literals list
  (exact-spelling membership, so the suite's bound-identifier test passes;
  literal-vs-input compares user-visible spellings), nested `...` with
  tail patterns after the ellipsis (proper and improper — `part-2x`),
  `(... ...)` escapes, custom ellipsis identifier, `_` wildcard (and `_`
  as a declared literal), vector patterns, `(x ... ...)` extra-level
  splicing. **Hygiene via renaming**: every identifier a template
  introduces is renamed to a marked spelling (`\x01<envid>.<inst>\x01orig`)
  unique to the expansion — introduced binders can't capture user code,
  and an unbound marked name falls back at the root to resolving its
  original in the macro's definition scope (`markFallbackEnv`; envid 0 =
  global, else the root's `macroEnvs` table, which also handles forward
  refs — `foo399`). Marks nest and strip one layer per resolution, so
  macro-defining macros (`jabberwocky`, `be-like-begin`) work. Quoted
  template sections skip renaming ('(a b) yields the author's symbols);
  `PrintValue` and error messages show base spellings; marked defines stay
  out of command dispatch and completion.
- [x] `let-syntax`, `letrec-syntax`: child env scoping both the macros and
  the body (defines don't leak, §4.3.1), transformers evaluated in the
  child, body tail-positioned.
- [x] Validation: `case`, `do`, `when`, `unless` re-expressed in
  `syntax-rules` (R7RS §7.3 style) with agreement against the Go macros
  pinned in `eval/syntax_test.go`; the Go implementations stay as the fast
  path.
- Also landed, forced by the suite:
  - **Depth-based head resolution** (`resolveHead`): the shallowest
    environment holding a name in any category wins, so a local binding
    now shadows an outer macro/special form (correct lexical scoping — the
    suite's `my-or` test with `(let odd?)` locals). Within one level a
    special form still outranks a macro outranks a binding, so a global
    define beside a global macro keeps the macro. The walk also early-exits
    at the first hit now (it used to scan the whole chain every call).
  - **Macro expansions ride the TCO cell** (`evalList`): recursive
    functions whose bodies are macro forms (`when`-loops) run in constant
    Go stack, and a runaway self-expanding macro is an interruptible loop
    instead of a fatal stack overflow. This exposed a latent frame-pool
    bug — an immediate `((lambda ...) args)` application (what `let`
    expands to) chains its frame through the current owned frame without
    a capture mark, and adoption released that parent mid-use — fixed in
    the Eval loop by skipping release when the new frame's unmarked parent
    chain reaches the owned frame (`TestMacroTailCalls` pins both).
  - Auxiliary keywords (`else`, `=>`, `unquote`...) recognized by base
    spelling across expansion layers (`symIsBase` in cond, case, guard,
    quasiquote).
- Documented deviations: auxiliary keywords are matched by spelling, not
  binding — `(let ((=> #f)) (cond (#t => 'ok)))` stays an error (the one
  4.3 failure); Go macros remain unhygienic (a local `if` binding can
  confuse a Go `when` expansion — the syntax-rules forms are the hygienic
  spelling); `(let-syntax ...)` transformer scopes are retained for the
  session in the root's `macroEnvs` table.

## Phase 5 — base library procedure gap (the long tail) — done

Ratchet 656 → 797, every touched section up, none down. New list surface
lives in `eval/list.go`; benchmarks unchanged against a HEAD-worktree
baseline (`BenchmarkListPipeline` byte-identical at 25025 allocs;
`BenchmarkFibEval` at the Phase 2.5 numbers).

Equivalence & booleans:
- [x] `eqv?` (`eqvEqual` in interpreter.go: numbers and characters by
  value with exactness — `(eqv? 2 2.0)` is `#f` — everything else eq?'s
  identity), `boolean=?`, `symbol=?` (variadic chains)
- [x] Audit `eq?`/`equal?` against §6.1: behavior over dictionaries,
  keywords, streams unchanged (deepEqual/identityEq already covered
  them — the Phase 8 doc section still owes the write-up)

Numbers (base + `(scheme inexact)`):
- [x] `gcd lcm floor/ floor-quotient floor-remainder truncate/
  truncate-quotient truncate-remainder exact-integer-sqrt square
  numerator denominator rationalize` — `square` canonical with `sqr` as
  alias; the division family shares one intDiv2 core with §6.2.6
  exactness contagion; lcm promotes to inexact on int64 overflow;
  numerator/denominator recover a float's exact binary rational via
  math/big (cold path only); rationalize is the reference
  continued-fraction search in float64, exact-in/exact-out only when the
  result is integral (D1: no rationals, documented error otherwise)
- [x] `(scheme inexact)`: `tan asin acos atan` (2-arg atan = Atan2),
  `finite? infinite? nan?` (exact integers always finite), `log` with
  base arg
- [x] `zero? positive? negative? even? odd?` audited (already handled
  the split via numFloat/numInt64), variadic `-`/`/` single-arg forms and
  `min`/`max` contagion confirmed landed with D1

Pairs & lists (`eval/list.go`):
- [x] `caar`…`cddddr`: all 28 forms generated over carOf/cdrOf, working
  on both list representations and dotted pairs
- [x] `memq memv assq assv`; `member`/`assoc` optional comparator third
  arg — and member now returns the R7RS *sublist* rather than rf's old
  boolean (the collision audit's "R7RS semantics win"; sublists are
  truthy so conditional uses keep working). Streams still coerce like
  before; walks are step-capped so circular lists error instead of hang.
  `case` datum matching switched from member to memv per §4.2.1.
- [x] `make-list list-copy list-set! list-tail` — make-list and
  list-copy produce mutable pair chains (so list-set! works on them);
  list-set! on a slice-backed literal errors like set-car!; list-copy
  shares improper tails and passes non-lists through per §6.4
- [x] `map`/`for-each` over multiple lists, shortest-list termination —
  multi-list stepping is lazy via seqNext, so one circular list is fine
  while another is finite (the §6.10 suite case); the single-list fast
  path (contiguous AsList iteration, stream coercion) is unchanged

Strings & chars:
- [x] `string-map string-for-each` (multi-string, shortest ends the
  walk, mixed representations)
- [x] `string-upcase/downcase/foldcase` full Unicode case mapping via
  x/text/cases (ß→SS, İ→i̇, ǰ→J̌, final sigma; already an indirect
  dependency, now direct). `foldString` — and with it the `string-ci`
  chain family — is full case folding per §6.7 (`(string-ci=? "Maß"
  "MASS")`); `char-foldcase` stays simple folding as the spec defines

Control:
- [x] `apply` with leading individual args — already worked; pinned by
  the suite's `(apply + 1 2 '(3 4))`-style tests
- [x] `features` (`r7rs`, `ieee-float`, `full-unicode`, `retrofilter`,
  posix/unix + GOOS, arch/endian/lp64; no `exact-closed` — D1). Phase 7's
  cond-expand consumes it.

## Phase 6 — ports and I/O (`(scheme base)` §6.13 + file/read/write libraries) — done

Ratchet 797 → 1115; 6.13 Input and output passes completely (1→63), Read
syntax 6→93 (green), Numeric syntax 16→167, 6.11 up 17→30 (its string-port
tests). Ports live in `eval/port.go`, the datum reader in `eval/read.go`,
the write-form printer in `eval/write.go`, `with-approval` in
`eval/approval.go`.

- [x] Port type (`eval/port.go`): one `*Port` covering textual/binary ×
  input/output, all predicates, `input-port-open?`/`output-port-open?`,
  the three closers (idempotent per §6.13.1), `call-with-port`
- [x] `current-input-port current-output-port current-error-port` as
  parameter objects (Go-constructed `*Parameter` with a port-validating
  converter, so `parameterize` can't install a non-port);
  `display`/`newline`/`write` take the optional port argument, and rf's
  `print` routes through `current-output-port` so `with-output-to-file`
  captures it. stdin's reader is process-level (one terminal per process,
  like the interrupt flag) and pulls one byte per syscall so it can't
  buffer ahead of readline
- [x] String & bytevector ports — the sandboxed (never-gated) surface
- [x] Reading/writing: the full §6.13.2/§6.13.3 op set; `char-ready?`/
  `u8-ready?` answer open-port truth (our ports never block indefinitely)
- [x] `(scheme read)`: `read` (`eval/read.go`) — a rune-level scanner
  consumes exactly one datum's characters from the port (so `#t(5)` reads
  as two datums and the delimiter survives for the next call), then the
  text runs through the ordinary tokenize/parse pipeline — reader and REPL
  parser can never disagree. `#!fold-case`/`#!no-fold-case` directives are
  per-port state folding symbol tokens only; scan/parse failures raise
  conditions with Kind "read" (`read-error?`)
- [x] `(scheme write)` (`eval/write.go`): `write` (cycle-labeling),
  `write-shared` (labels all sharing), `write-simple`, port-aware
  `display`. Write form escapes strings, pipes symbols whose spelling
  wouldn't read back (`|a b|`, `|2|`, `|+i|`), prints inexact integrals
  with `.0` and infinities as `+inf.0`/`+nan.0` — PrintValue stays the
  human-first REPL printer (display-first defaults untouched). Documented
  extensions: keywords write `:k`, dicts `{:k v}`, streams/records/
  procedures keep their `#<...>` forms
- [x] `(scheme file)`: the full set. **Gating is per-file** (revised from
  the original per-operation design): each open prompts — read-gated for
  input ports, write-gated for output — and an approved port's operations
  run free for its lifetime; a port that reached the assistant unapproved
  (stdin, a user-opened file) prompts once on first touch. `delete-file`
  is write-gated; `(with-approval body...)` batches a block behind one
  prompt showing the body's source (below). `write-file`/`append-file`
  carry the same gate, and the read-side builtins (`cat`, `ls`, `stat`,
  `grep`, `glob`, `wc`, rank/parse file inputs, `load`) are read-gated —
  the `agent-allow-working-dir` standing grant (`eval/approval.go`) lifts prompts under
  the session's starting directory. `file-exists?`/`exists?` stay ungated
  (boolean probes).
  Bonus (same numeric-syntax push): `s/f/d/l` exponent markers parse as
  inexact, integral rationals (`10/2`, `#x10/2`, `#i3/2`) parse per D1,
  and `+InF.0`/`#i+nan.0` spellings are case-insensitive with prefixes
- [x] **Streams ↔ ports bridge** (extension): `(stream->port s)` wraps a
  line stream as a sandboxed textual input port (the producer was gated at
  creation) — `(read-line (stream->port (sh "...")))` works; `cat`/`sh`/
  `fetch` keep returning streams — ports are the conformance surface,
  streams stay the shell surface
- [x] **`with-approval`** (`eval/approval.go`, beyond the spec): a special
  form registered post-construction as a closure over the *session*
  approval gate (like the exception builtins — `callFunction`'s throwaway
  evaluators can't ungate anything). With an approver installed it shows
  the body's printSource in one y/N prompt, then lifts the gate for the
  body's dynamic extent, restoring on every exit path; without one (user
  code) it is `begin`. The caller identity stays assistant inside, so
  user-only builtins remain blocked, and an `(agent ...)` spawned inside
  re-gates its own tool calls. Dynamic values (`(rm (f))`) are approved
  sight-unseen — the trade batching buys

## Phase 7 — program structure & remaining libraries — done

Ratchet 1115 → 1127; 6.12 Environments and evaluation passes completely
(0→4 — everything the suite enables), 6.14 System interface 5→13.
Libraries live in `eval/library.go`, eval/load in `eval/evalproc.go`,
process-context/time in `eval/process.go`; the suite harness now runs the
real `import` (its Phase-0 stub is gone — `(chibi test)` registers as a
library whose forms are the harness's own test special forms). Benchmarks
unchanged against a same-machine HEAD-worktree A/B (`BenchmarkFibEval` and
`BenchmarkListPipeline` byte-identical in allocs and bytes/op — the
registry is one pointer on the root Environment, never touched per call).

- [x] `define-library` / `import` / `export` (D5, `eval/library.go`): the
  library registry lives on the root environment (the macroEnvs pattern) —
  preloaded `(scheme *)` entries (complex included so conformance
  programs' import lines run; its procedures still don't exist) plus
  define-library's recorded export maps (external → internal name;
  `(rename ...)` export specs supported). Declarations — `begin`,
  `import`, `include`/`include-ci`, `include-library-declarations`,
  declaration-level `cond-expand`, processed recursively — evaluate
  straight into the global env; `import` is controlled aliasing:
  `only`/`except` validate and narrow, `prefix`/`rename` copy bindings
  (values or define-syntax macros) under new names, export existence
  checked lazily at import time. Documented deviations: `prefix`/`rename`
  over a preloaded library errors loudly (the thin design keeps no export
  enumeration to rename), and user libraries share the one store
  underneath — library isolation is naming, not storage.
- [x] `include` / `include-ci` as expressions: each file's forms evaluate
  in the current env, last value returned; include-ci folds symbol tokens
  through the reader's own `foldTokens` (strings/chars keep their
  spelling), sharing `parseAllTokens` with ParseAll
- [x] `cond-expand` (expression and library-declaration positions) with
  `library`/`and`/`or`/`not` requirements over the shared `featureNames()`
  list (the features builtin now rides it); no matching clause errors per
  §4.2.1; a matched body runs with begin semantics, last form in tail
  position
- [x] `(scheme eval)` / `(scheme repl)` (`eval/evalproc.go`): `eval` with
  optional environment, `environment` (import sets validated — unknown
  libraries error), `interaction-environment`; under D5 every environment
  specifier resolves to the one live global env, with `*Environment`
  itself the first-class environment value (prints `#<environment>`).
  Quoted data converts back to the evaluator's slice AST via `dataToAST`
  (proper pair chains → slices recursively; improper chains keep their
  pair spine — exactly the shape the parser gives dotted formals), so D2
  stage two stays unnecessary through the last phase that could have
  forced it. eval and load register post-construction as closures over
  the session evaluator (the exceptionBuiltins pattern), so evaluated
  code shares the handler stack, approval gate, and caller identity
- [x] `(scheme load)`: `load` with the optional environment arg — ungated
  like `cat` (the loaded code's own destructive calls still hit the
  per-op approval gates)
- [x] `(scheme process-context)` (`eval/process.go`): `command-line`;
  `exit`/`emergency-exit` **user-only** like exec — a model tool call
  must never terminate the shell (and identical to each other under D4:
  exit runs no dynamic-wind afters, documented deviation);
  `get-environment-variable`/`get-environment-variables` (sorted alist of
  real string pairs) carry the env builtin's forced assistant-caller
  secret redaction — the model sees `[redacted]` through every spelling
- [x] `(scheme time)`: `current-second` (inexact Unix seconds),
  `current-jiffy` (exact nanoseconds since process start — monotonic,
  overflow-safe), `jiffies-per-second` (10⁹)
- [x] `(scheme r5rs)`: `exact->inexact`/`inexact->exact` aliases,
  `scheme-report-environment`/`null-environment` (version 5 or 7, both
  the global env) — cheap, so yes, bothered

## Phase 8 — rf integration, pipe DSL, collision audit — done

- [x] **CommandMeta audit**: new `Stage` words — `reverse`, `member`,
  `assoc`, `vector->list`, `list->vector`, `string-upcase`,
  `string-downcase`, `string-foldcase` — all input-last with string or no
  extra arguments, so command-mode pipelines thread them naturally
  (`printf 'x\n' | string-upcase`; AsString/AsList coerce piped streams;
  pinned in the PTY integration suite). Audit outcome for the
  procedure-taking words (`map`, `for-each`, `apply`, `vector-map`,
  `string-map`): they stay Scheme-only — command mode passes words as
  strings, so a Stage declaration could only ever produce type errors,
  and `(pipe (ls) (map car _))` already works at the Scheme layer.
  Mutators (`set-car!`, `string-set!`, …) and port plumbing stay
  Scheme-only; none of the new words warrant standalone `Command` status.
- [x] **Name-collision audit** (R7RS semantics won at the Scheme layer —
  verified, all landed in earlier phases): `and`/`or` short-circuiting
  special forms (Phase 3); `member`/`assoc` return the R7RS
  sublist/entry with the optional comparator (Phase 5); `length`/`list?`
  cycle-safe with spec improper-list behavior (Phase 2);
  `string->number` rides the reader's own scanner — never a partial
  parse (Phase 2); `newline`/`display` take the port argument (Phase 6);
  `error` raises an `*ErrorObject` condition (Phase 3 — nothing depended
  on the old behavior); `square` canonical with `sqr` as alias (Phase 5).
  Extensions kept, no R7RS collision: `get`, `take`, `head`, `sort`,
  `filter`, `fold-left`/`fold-right`/`reduce`, `file`.
- [x] **Dict/stream conformance notes**: the "Extensions vs the spec"
  section below
- [x] Number formatting in tables after D1: exact integers print bare
  (`Integer.String`), inexact keep the display-first defaults — settled
  by the numeric split itself, no escape hatch needed
- [x] CLAUDE.md / PROJECT.md updated (Known gaps shrank to the documented
  deviations); the LLM system prompt states the interpreter is
  R7RS-small so the model uses standard Scheme freely
- [x] Conformance suite: all sections enabled, ratchet at 1129 (Phase 6
  left it at 1115), `features` returns `r7rs`. The audit's failure sweep
  surfaced two genuine gaps hiding among the deviations, both fixed:
  **vector quasiquote templates** (§4.2.8 — qqExpand had no Vector case;
  list and vector templates now share `qqExpandInto`, whose splicing head
  also matches by base spelling so macro-expanded templates splice), and
  **`=` across the exact/inexact split** (int64→float64 conversion rounded
  above 2^53, making `(= 9007199254740993 9.007199254740992e15)` true;
  `intFloatEqual` now compares on the integer side). Every remaining
  failure is a documented deviation: re-entrant call/cc (D4, 1 test),
  auxiliary keywords matched by spelling (Phase 4, 1 test), and D1's
  numeric tower cuts — no complex (~55 tests), no rationals (~28), no
  bignums (~5).

## Phase 9 — tree-walker speed against chibi (Gabriel benchmarks) — done

Chibi-scheme (a bytecode VM, `tmp/chibi-scheme` when cloned) is the
measuring stick: its `benchmarks/gabriel` suite, ported to self-timing
R7RS scripts that run unchanged on both implementations (`tmp/bench/`,
`run.sh` prints the side-by-side table), plus Go-bench ports of four of
them beside `BenchmarkFibEval`/`BenchmarkListPipeline`
(`eval/gabriel_bench_test.go`) so pprof and `-benchmem` see the same
workloads. Baseline: rf 7–20× slower than chibi (destruct the outlier);
profile: ~30% of all CPU in `Environment` RWMutex atomics, ~35% more in
`resolveHead`'s per-level triple map probe, the rest macro re-expansion
and builtin arg-slice allocation. Four optimizations landed, none adding
a compilation pass:

- **Environments are goroutine-confined; the per-Environment RWMutex is
  gone** (~30% of eval CPU). An audit of every goroutine launch in
  eval/cmd/llm/console/rsh found exactly one path where another goroutine
  could run env-touching code concurrently with evaluation: `sh` stdin
  pumped by os/exec's copier goroutine from a lazy `where`-lambda stream
  (already racy at the Evaluator level — shared tail cell, handler stack —
  so the mutex wasn't saving it). That path is closed structurally:
  `Stream.userCode` marks streams whose pull runs Scheme (set by `where`'s
  callable branches, propagated by every derived-stream constructor), and
  `shStdinReader` drains such a stream on the eval goroutine before the
  child spawns. Everything else was already same-goroutine (jobs and grep
  workers never touch Scheme; the completer runs between evaluations; the
  console doesn't import eval). The confinement contract is documented on
  the Environment struct: a new cross-goroutine path must bring its own
  confinement, not re-add a lock.
- **Macro call sites expand once** (`eval/macrocache.go`): a process-global
  cache keyed by AST-node identity (backing-array pointer + length) and
  the exact macro instance (funcval pointer — distinguishes two
  syntax-rules closures where a code pointer cannot, and the stored
  MacroFunc keeps the instance alive so ids can't be reused). Re-running
  `let`/`cond`/`do` transformers on every evaluation was pure waste;
  reuse per site is observably expand-once, which is what compiled
  Schemes do — distinct sites keep distinct hygiene marks. Redefinition,
  `eval` under a different environment, and let-syntax shadows all miss on
  the instance check and re-expand. Bounded (drop-and-refill past 16k
  entries).
- **One forms map, binding-first probing**: `specialForms` and `macros`
  merged into `forms map[string]formEntry` (an entry can hold both; the
  special form keeps priority, so `(define if 5)` still can't shadow
  `if` as a head). `formShadows` counts names in both `forms` and
  `bindings` at a level — always zero in practice, nothing deletes
  bindings — and while zero, `resolveHead` probes bindings before forms,
  making the common head (a function at the global level) a single map
  probe. Nil-map guards skip mapaccess calls entirely on call frames.
- **`FastBuiltin` unary/binary paths** (`eval/fastbuiltin.go`): the
  builtin arg-slice was ~80% of remaining allocations. The hottest
  builtins (`+ - * / = < > <= >=`, `car cdr cons`, `set-car!/set-cdr!`,
  `not null? pair? zero?`, `eq? eqv? equal?`) are upgraded in place to a
  wrapper carrying allocation-free `Fn1`/`Fn2` beside the untouched
  general `BuiltinFunc` — 1/2-argument call sites evaluate into locals,
  every other route (apply, map, other arities) uses the general path.
  Fast paths replicate the general path's semantics and error messages
  exactly; they are an implementation detail, never a semantic fork.

Measured (i3-8109U, Go benchmarks): tak 48→21ms, nqueens 49→23ms, deriv
6.2→3.2ms, destruct 493→170ms per op. Script-level vs chibi: the gap
narrowed from 7–20× to 3–6.5×, and ctak (escape-only call/cc as
panic/recover, D4) stays ~2.7× *faster* than chibi's full continuations.
Not pursued here, by the "no heavy machinery" constraint: a bytecode
compiler, lexical addressing / per-site symbol caches (the remaining ~50%
of eval time was global-map hashing that only pre-resolution would
remove — Phase 10 took that step), and arg-slice pooling for generic
builtins (builtins like `values` retain their slice).

## Phase 10 — closure compilation with lexical addressing — done

The wall Phase 9 stopped at: with the mutex gone and macros expanding
once, half of evaluation time was still name resolution — `resolveHead`'s
chain walk and map probe for every call head, `Lookup`'s for every
symbol, and a forms-map probe per evaluation to learn that `if` is still
`if`. Nothing local removes that; a compilation pass does. `eval/compile.go`
replaces the tree walker with **closure compilation** — the route cel-go
and most Go Lisps take, and the same win a bytecode VM would bring
without the VM (Go has no computed goto, so switch dispatch buys little
over a tree of small nodes, and heap continuations — the VM's real
payoff — are what D4 declined).

- **Two passes.** `Eval` compiles a form into a tree of `node` values,
  resolving every identifier once: a local becomes a `(depth, slot)` pair
  into the runtime frame chain (`localRef0`/`localRef1` for the two hot
  depths), a global a pointer to its **cell** (the global map is now
  `map[string]*cell`, so a compiled reference survives later `define`s
  and `set!`s — and a reference compiled before its global exists resolves
  lazily on first use, trying the hygiene-marked spellings outermost
  first), and a special form or macro is recognized at compile time.
  Macros expand exactly once, at their site, which retires
  `macrocache.go`. The run loop then does no name resolution at all.
- **The runtime shapes stay.** A frame is still an `*Environment` (slot
  array plus the names labelling it), so `Lookup` by name keeps working
  for builtins that take an env and for the completer/inspect; the frame
  pool, the tail cell, dynamic capture marking, escape-only `call/cc`,
  and goroutine confinement are untouched. That is what made the pass
  incremental: the core forms compile natively (`quote if define set!
  lambda begin and or pipe quasiquote define-syntax`, plus
  `((lambda ...) args)` — what `let` expands to — as a frame built in
  place with no closure and no capture mark), and every other special
  form runs through `sfNode`, which hands the existing `SpecialFormFunc`
  the raw forms and the runtime frame. A fallback form's nested `Eval`
  compiles against a scope rebuilt from that frame (`scopeFromEnv`, one
  level per Environment — the invariant depth addressing rests on) and
  is memoized per AST site and scope shape (`compileCache`), so `guard`
  in a loop compiles its body once.
- **Bodies pre-scan their definitions** (after head expansion, `begin`
  spliced), so mutually recursive internal defines resolve to slots and
  a fallback defining form's `env.Set` lands in the frame by name;
  `define-syntax` takes effect when compiled, body-level ones registering
  on the scope's **shadow** environment — the identity `syntax-rules`
  records in `macroEnvs` and the carrier of local macros — which frames
  built for the scope carry (`Environment.shadow`) so a compilation
  started from a live frame sees the same macros and resolves marks to
  the same level.
- **Callbacks run on the session evaluator**: `callFunction` reaches it
  through the root environment (`Environment.ev`), so `map`'s lambda
  frames recycle and the grant-binding guard (`define`/`set!` of
  `agent-allow-*`, now a compiled `grantGuard`) reads the real caller —
  the Phase 2.5 "throwaway evaluators" wart is gone.

Measured (i3-8109U, Go benchmarks, count 3): tak 19.7→5.0ms, nqueens
22.5→5.0ms, deriv 3.4→1.2ms, destruct 160→40ms, fib(30) 715→190ms
(3–4.5× on every Gabriel bench, on top of Phase 9); `BenchmarkListPipeline`
4.4→3.1ms with allocations 25k→10k. Fallback forms in loops sat at or
near the old numbers (guard 4.4→3.9ms, let-values 11.9→13.7ms,
parameterize 4.8→8.4ms) until the follow-up below ported them.

Against chibi (same machine, the self-timing scripts in
`eval/testdata/gabriel/` — `run.sh` there documents the setup and prints
the table): tak 3.3 vs 4.9ms (1.5×), nqueens 3.5 vs 5.2ms (1.5×), deriv
1.1 vs 1.4ms (1.3×), destruct 26 vs 44ms (1.7×), fib(30) 101 vs 194ms
(1.9×) — the Phase 9 gap of 3–6.5× is now 1.3–1.9× on a tree of
closures against a bytecode VM, and ctak (escape-only call/cc as
panic/recover, D4) runs 5× faster than chibi's full continuations
(173 vs 36ms). What remains is mostly the D6 boxing cost and the
generic-builtin arg slice (fib's `+` of two boxed results, nqueens'
`append`), not resolution. Two "io"-tagged programs from
ecraven/r7rs-benchmarks ride the same harness (inputs fetched on first
run): `wc` — a `read-char` loop over the suite's 4.4MB text, ~7 calls
per character — 6.4s chibi vs 2.7s rf (0.4×, ~600ns per character all
in), and `read1` — the datum reader over a 28KB program, what `load`
and `include` pay — 77ms vs 1.2ms (rf's reader is Go; both consume the
same five datums). Neither is a shell workload; they pin the port layer
and the reader against pathology.

Deviations that moved with compilation, all expand-once semantics every
compiled Scheme shares: a macro is expanded when its use site is
compiled, so redefining the macro does not re-expand functions compiled
against it (a re-evaluated `define` does); `define-syntax` registers its
transformer when compiled, not when its position is reached; syntax
errors inside a lambda body surface at definition rather than at the
first call; a macro whose recursion is bounded only by a runtime value
(`(ev? n)` expanding to `(od? (- n 1))`) cannot terminate under
expand-once semantics — expansion polls the interrupt flag per step, so
^C stops it within a step, and a step-and-node budget (`expandStep`,
compile.go — the node budget is the real bound: instantiation copies a
twice-used pattern variable's term, so the runaway doubles per step)
errors out non-interactive
callers (`expandStep`, compile.go); a definition nested in a non-body position
inside one of the remaining fallback forms (`(with-approval (define x
...))`) is visible by name within that form but resolves as a global from
natively compiled code around it (R7RS §5.3.2 requires definitions at
body level anyway).

### Phase 10 follow-up — the fallback forms ported, two ideas measured

`eval/compile_forms.go` compiles the forms that first rode `sfNode`:
`guard`, `parameterize`, `let-values`/`let*-values`, `define-values`,
`define-record-type`, `case-lambda`, `delay`/`delay-force`,
`let-syntax`/`letrec-syntax`. Each compiles its subforms once against
the lexical scope; the binding forms build pooled frames the way
`letNode` does (a guard clause body, a let-values body, and a let-syntax
body are tail positions handed to the run loop), the defining forms
store through the same three destinations `define` compiles to (a
static slot, a global cell, a live frame's map — and the grant-binding
guard now covers `define-values`/`define-record-type`, which the
fallback's `env.Set` bypassed), `let*-values` is its nesting of
`let-values` (one frame per clause, so an init shadowing a later
clause's name reads the outer binding rather than an unfilled slot — the
single-frame spelling gets that wrong), `let-syntax` registers its
transformers on a child scope's shadow environment exactly as a
body-level `define-syntax` does, and `define-record-type` parses at
compile time and instantiates a fresh disjoint type per evaluation.
What still rides `sfNode` is the tail no loop pays for: `with-graph`,
`with-approval`, `include`, `cond-expand`, `define-library`,
`syntax-error`.

Measured (`eval/forms_bench_test.go`, 20k iterations of one form on a
tail-recursive countdown, i3-8109U): guard 5.0→3.3ms (raise path
22.6→12.0ms), parameterize 14.7→2.8ms, let-values 32→14ms, let*-values
27→11ms, define-values 9.0→4.9ms, case-lambda 37→10ms, delay/force
9.0→7.2ms, define-record-type 13.8→12.4ms, let-syntax 30→4.8ms (the
fallback re-parsed the `syntax-rules` and registered a fresh `macroEnvs`
entry per evaluation). The Gabriel suite is unchanged, as expected.

Two follow-ups the port was to unblock were measured and declined:

- **Static capture analysis** (skip `markCaptured` for bodies with no
  closure-creating node and no env-retaining builtin): a clean CPU
  profile of nqueens/deriv/destruct shows `markCaptured` below the
  20ms-of-4.9s display cutoff — it walks one or two parents and stops
  at the first captured ancestor — and frames are absent from the top
  allocators (`acquireFrame` 3%, `releaseFrame` 5% of CPU, both the
  pool working). The analysis would save a bool store per closure
  creation; there is nothing to recover.
- **Arg-slice pooling for generic builtins**: `evalArgs` is 14% of
  allocated objects on nqueens/deriv, and `mallocgc` 18% of CPU in
  total, so pooling bounds at ~2.5% of CPU — against the retention
  audit it needs across 281 builtins (a grep pass finds `values`,
  `list`, `apply`, and the range-taking string/vector builtins slicing
  `args`, but only a full read rules the rest safe). Declined at that
  ratio; the remaining allocation is boxed numbers (D6) and the pairs
  the programs build, not the call path.

## Extensions vs the spec (dictionaries, keywords, streams)

R7RS-small §1.3.3 reserves `{ }` for language extensions; rf claims them
for dictionary literals and adds `:keyword` and stream types. Their
defined behavior over the spec's generic operations:

- `equal?` — dictionaries compare by content (same keys, `equal?`
  values); keywords by spelling; streams by identity (a stream is a
  stateful single-use producer, not a value).
- `eqv?` / `eq?` — identity for all three (dictionaries are Go maps:
  pointer identity).
- `write` — documented reader-syntax extensions: keywords write as `:k`,
  dictionaries as `{:k v}` with values in write form; streams, records,
  procedures, ports, and environments keep unreadable `#<...>` forms.
  `display` prints the same shapes display-first.
- quasiquote — does **not** descend into dictionary templates: a `{...}`
  inside a quasiquoted template passes through verbatim, unquote holes
  unexpanded. The plain `{:k expr}` literal already evaluates its values,
  so computed dictionaries are built with the literal, not quasiquote.
- `list?` / `pair?` — `#f` for dictionaries, keywords, and streams. The
  spec's list operations consume streams through the AsList coercion
  boundary instead — and a coerced stream is spent (single-use as ever).
- `case` datum matching uses `memv` (§4.2.1), so a dictionary datum
  matches only by identity — use `cond` with `equal?` for content
  matching.

## Landing order

Phase 0 harness ✓ → the `and`/`or` fix ✓ → Phase 1 reader ✓ →
`BenchmarkListPipeline` (Phase 2.5, needed as the D2 baseline) ✓ → D2-stage-one
pairs + D1 numbers (the two big refactors, sequentially, each with before/after
benchmarks) ✓ → Phase 2 remaining types ✓ → Phase 2.5 allocation work (frame
freelist + small-int table, once the ground stops moving) ✓ → Phase 3
control/exceptions ✓ → Phase 4 syntax-rules ✓ (taken before the Phase 5 tail;
D2 stage two turned out unnecessary — the matcher abstracts over both list
representations) → Phase 5 procedure tail ✓ (656 → 797) → Phase 6 ports ✓
(797 → 1115, with per-operation approval gating and `with-approval`) →
Phase 7 libraries ✓ (1115 → 1127; every section the suite enables now
runs) → Phase 8 integration audit ✓ (→ 1129: the failure sweep found and
fixed two real gaps — vector quasiquote, exact `=` across the split) →
Phase 9 speed ✓ (mutex gone, expand-once, fast builtins) → Phase 10
closure compilation ✓ (ratchet unchanged at 1129).
**The plan is complete** — every remaining suite failure is a documented
deviation (D1 numerics, D4 escape-only call/cc, Phase 4 aux-keyword
spelling), by design rather than by gap.

Rationale: the harness makes progress measurable from day one; pairs and the
numeric split touch everything, so they go before the long tail; syntax-rules is
self-contained and can slide; ports depend on parameters (Phase 3); the library
layer is last because everything it exports must exist first.
