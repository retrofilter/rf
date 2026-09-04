# SHELL.md — command mode's relationship to unix

How rf's command mode divides the world between the shell and Scheme: the
principles, the decisions that got here (August 2026 redesign), the
alternatives we rejected and why, and the work that remains. Since D8 the
shell side is **rsh**, rf's own in-process interpreter (`rsh/`, built on
mvdan.cc/sh) — rf never forks `/bin/sh`; earlier decisions reference
`/bin/sh` as the executor they were made against. `eval/SCHEME.md`
documents the interpreter; `CLAUDE.md` carries the product thesis and the
enforced invariants. This file is the design record for the
shell layer.

## Why the redesign happened

The original design intercepted familiar shell names: `ls` returned rows
rendered as a table, `grep` filtered lines or rows, flag tables translated
muscle memory (`ls -la` → `(ls :all)`, `rm -rf` → recursive), and unknown
flags errored with a hint at the `!` escape. Structurally sound — but it
sat in an uncanny valley. The flag tables were an inventory of anticipated
annoyances, and the unpatched set was unbounded: `ls -lt`, `grep -rn`,
`wc -l` all interrupted with an error that reads as "the shell is broken"
to anyone with shell muscle memory — which is the entire audience. Worse,
dispatch depended on token shapes users can't see (`ls *.go` ran the
builtin; `grep foo *.go` silently ran system grep).

The fix was not to emulate better. It was to stop impersonating.

## The principles

1. **Every word that looks like unix is unix.** No name that shadows a
   system binary registers any command-mode presence — head or stage.
   `ls`, `cat`, `grep`, `cp`, `mv`, `rm`, `mkdir`, `stat`, `wc`, `which`,
   `env`, `ps`, `sh`, `head`, `basename`, `dirname`, `pwd` always mean
   standard unix behavior, every flag intact (since D8 a handful — `echo`,
   `test`, `pwd`, `kill`, … — run as rsh's in-process builtins rather than
   PATH binaries, like they would in bash itself). The Scheme builtins
   stay bound under the same names, reachable with parens: `(ls)`,
   `(grep pat x)`. `eval/command_test.go` pins the shadow list.

2. **The structured world is novel vocabulary.** `dir` (rows spelling of
   ls), `where`, `sort-by`, `group-by`, `count-by`, `pick`, `similar`,
   `bm25`, `hybrid`, `chunk`, `take`, `get`, `length`, `agent`, `llm`,
   `remember`, `history`, … — words with no prior unix meaning, so a word
   in a pipeline is either unambiguously unix or unambiguously Scheme.
   Admission test for any future command or stage word: *could this token
   ever appear in a line the user meant for sh?* If yes, it doesn't get
   registered.

3. **Rows flatten to text at the boundary, implicitly.** When rows cross
   to an external command they serialize as header-less TSV (`shStdin` →
   `textLine`), so `history | grep make` is native grep over data lines.
   What printed as a table crosses as greppable text — the conversion is
   never the surprise; the old error was. `json` is the explicit spelling
   when structure must survive the crossing (`dir | json | jq -r .name`);
   `text` remains as the self-documenting form of the default. A bare
   string still never feeds stdin (guards command-mode word-splitting).
   Precedent: nushell and PowerShell both settled here.

4. **Per-line effects belong to the shell; cross-line state belongs to
   rf.** Pipes, redirects, `$VAR`, globs, `&&` — one-line effects, all
   native. `cd`, env, PATH, aliases, definitions, history — state that
   must survive to the next line — is rf's, spelled in Scheme (`set-env`,
   `add-path`, `alias`, `define`) *or* mutated by any shell line: since D8
   the executor runs in-process, so `export`, `cd`, `source`, bare
   assignments, and function definitions harvest back after every line.
   Children are hydrated from rf's state, which is why `echo $PATH` just
   works — and the loop is closed in both directions.

## Decisions

- **D1 — Shadow purge** (chosen). Strip `Command`/`Stage`/`Flags`/`Globs`
  from every coreutils shadow; they fall through to `/bin/sh` whole.
  Scheme bindings, the model's tools, and approval gating are untouched —
  the LLM always lived in parens, so the rollback cost it nothing.

- **D2 — `dir` as the rows entry point** (chosen; first named `files`).
  The `ls` builtin registered under a non-shadowing name, with `Globs` and
  `:all` but no flag table. Known trade-offs, accepted: GNU coreutils
  ships a near-unused `dir` binary (intercepted on Linux), and DOS muscle
  memory types `dir` expecting a plain listing.

- **D3 — Keep stage words; reject head-only dispatch** (considered,
  rejected). Head-only ("Scheme words count only in first position") was
  attractive for parser simplicity, but post-purge the stage landmines are
  already gone — every remaining stage word fails the admission test's
  inverse, so it can't collide with a real sh pipeline. Head-only would
  have killed the product's best moves (`make test 2>&1 | agent why did
  this fail`, `git log | similar "auth"`) and created a new landmine:
  `history | grep make` dying with "history: command not found". The
  objection that motivated head-only was really about that pipe crossing —
  solved properly by D4 instead.

- **D4 — Implicit TSV out** (chosen). Replaced the "rows crossing to an
  external command error naming json/text" invariant. Errors now surface
  downstream if structure was actually needed (`dir | grep md | where …`
  fails at `where`, clearly). Display values and TSV values differ where
  the table humanizes (`4.1K` vs `4198`) — raw wins at the machine
  boundary, per the display-first-defaults stance.

- **D5 — Fresh sh per line is the model; persistent sh child rejected**
  (superseded by D8). A long-lived sh child keeps state for free but
  creates two sources of truth (its env/cwd vs rf's), needs bidirectional
  sync anyway, complicates job control and pipeline stdout capture, and a
  wedged child takes session state with it. Fresh-per-line matches bash's
  own fork/subshell rules — pipeline segments don't mutate the parent in
  bash either. What was missing was only the harvest direction (D6).

- **D6 — Hydrate/harvest state loop** (chosen direction, superseded by D8
  before being built). Whole-line sh fallthroughs would run wrapped so the
  child reports final state on a spare fd, stdout/stderr untouched:
  `{ <line> ; } ; rc=$?; { pwd && env -0 && umask; } >&3; exit $rc`;
  rf diffs the report and absorbs env/cwd/umask changes. D8 reaches the
  same goal by construction (the executor runs in-process), and more:
  functions and non-exported vars persist too, which no child-report
  design could deliver. D6's semantic choices carried over: only
  whole-line executions mutate rf — pipeline segments and `(sh ...)` keep
  fresh-shell semantics, bash's own subshell rule.

- **D7 — Declared options: unix flags outside, one dict inside** (chosen,
  August 2026). The novel vocabulary had inherited ad-hoc option surfaces —
  keyword flags (`:all`, `:desc`), inline pairs (`rerank "q" :limit 20`),
  trailing dicts (rankers), a positional (`recall q 5`) — so `history -h`
  read as "the shell is broken" exactly the way the flag-table era did.
  Now every command's options are one declaration
  (`CommandMeta.Options`: long name, one-letter short, kind, doc) read
  three ways: command mode parses `-n 10` / `--limit 10` / `--limit=10`
  (`--` ends flags, dash-digit words like `-5` stay arguments) and
  desugars the set into a single trailing dict — the one visible Scheme
  form is `(history "make" {:dir "." :limit 10})`; Scheme spells that
  dict directly (`eval.ParseOptions`, dict recognized by type so stage
  position works, bare keyword accepted as boolean sugar); and
  `-h`/`--help`/`help name` render usage from the same table, scheme
  spelling included. The invariant that makes LLM↔user translation
  mechanical: **the long flag name is the dict key**. Instruction heads
  (`agent`, `llm`) answer only a lone `-h` — longer tails stay prose.
  Old spellings were removed, not aliased (one release, one convention);
  Min/MaxArgs now bound positionals only. Rejected: per-command bespoke
  parsing (the drift this replaces) and clustering short flags (`-rn`) —
  unowned complexity until someone misses it.

- **D8 — rsh: rf is its own shell executor** (chosen, built, August
  2026). All three `/bin/sh` call sites (the fallthrough+`!` path, stream
  stages, `sh :full`) replaced by an in-process interpreter (`rsh/`,
  wrapping mvdan.cc/sh — pure Go, matching the modernc SQLite stance).
  Why: delegating per-line execution to `/bin/sh` closed no state loop
  (D6's whole reason), and every clean-break shell that delegates ends up
  building an env-diff bridge anyway (fish's `bass`); owning the
  interpreter closes the loop by construction and makes unix semantics a
  patchable dependency instead of an impersonated black box.
  Mechanics: each line hydrates a fresh runner from process state plus the
  session's shell vars and functions; after the run, exported vars →
  `os.Setenv`/`os.Unsetenv`, cwd → `os.Chdir`, the rest → the session.
  Job control is a custom exec handler (`rsh/jobctl.go`): a line's
  external children share one fresh process group owning the terminal,
  reaped with `WUNTRACED`; ^Z parks the group into the shell's job table
  and abandons the rest of the line (bash's semantics — a suspended job is
  its processes, the continuation is discarded), `fg`/`jobs` unchanged.
  Accepted corners: `umask` is unsupported in the interpreter (errors
  loudly — deliberately not shimmed, since no external binary can mutate
  this process's mask); names the interpreter reserves as builtins but
  doesn't implement get routed to their PATH binary by a call handler when
  one exists (`kill` — so `kill <pid>` works on the jobs the shell
  announces; user-defined functions still win; the set is kept minimal
  because an entry shadows any future interp implementation); words the
  interpreter does implement as builtins (`echo`, `test`, `pwd`, …) run
  in-process with standard semantics rather than as PATH binaries —
  including on `!` lines; mixed builtin|external pipelines park only the
  external stages on
  ^Z (fish's architecture, fish's answer); background `&` survivors get a
  `[bg pid]` notice and a `jobs` listing via the session's live-children
  registry (reap removes; a settle window lands backgrounded builtins'
  output before the prompt), but no `Done` notices or `fg`/`wait` on
  running jobs — and the line's context deliberately outlives the line so
  cancellation can't shoot survivors down; semantics target modern bash,
  not macOS's bash 3.2. Shared stdout/stderr writers are concurrent across pipeline stages
  by interp's contract — non-file writers at rsh boundaries must be
  locked.

- **D9 — `!!` history expansion, `!!` only** (chosen, August 2026).
  Bash's model at bash's layer: history expansion is the *first* step of
  reading a command-mode line — before Scheme detection, the `!` escape,
  and aliases — so the expansion routes through normal dispatch as if
  typed, and `!sudo !!` composes with the escape. The expansion echoes
  (dim) before running, and the history row is rewritten to the expanded
  line (bash's own behavior): up-arrow and autosuggestions replay the
  runnable command, and `!!` can never recall a literal `!!`. The event
  is the previous history line under the autosuggestion mode fence
  (command lines, plus scheme; never chat prose); no event is an error
  and the line never runs. Standalone occurrences only (bounded by
  whitespace, pipes, or the line ends; quotes suppress). Deliberately
  just `!!`: the rest of bash's designator set (`!$`, `!-2`, `!grep`) is
  the unbounded-subset trap the flag-table era taught us to refuse — and
  mvdan's interpreter has no history expansion (bash's own `-c` doesn't
  either), so the reader is this feature's only honest home. Pinned by
  `cmd/shell_test.go` (TestExpandHistoryBangs, TestExpandHistory) and
  `integration/shell_integration_test.go` (TestHistoryExpansion).

- **D10 — Leading flags on instruction heads that declare options**
  (chosen, September 2026). D7 left instruction heads (`agent`, `llm`)
  answering only a lone `-h`, so their tails stay prose — but folding the
  job quartet (`job-status`/`job-wait`/`job-kill`) into one `job` word
  needed flags on an instruction head. The rule: an instruction head
  with a declared Option table parses flags *leading-only* — `job -k 3`,
  `job --in 4h make test` — stopping at the first word that doesn't read
  as a flag, so the instruction tail keeps every dash literal (`job echo
  -n hi`); `--` ends flags explicitly for a command that starts with a
  dash. Heads with no Options are untouched — `agent explain what -h
  does` stays prose, D7's property preserved where it mattered. The old
  quartet was removed, not aliased (D7's one-convention stance);
  scheduling (`--in`, `--every`) rode in on the same table. Reader:
  `instructionFlags` in `cmd/shell.go`; pinned by
  `integration/jobs_test.go` (TestJobCommandWords).

- **D11 — One lexer: dispatch over the shell's AST** (chosen, built,
  September 2026). The reader decides what a line *is*
  with its own lexer (`splitPipeline` + `splitSimpleCommand`, a partial
  re-implementation of sh word-splitting that bails on redirects, `$`,
  backticks, `;`, `&`, braces, parens, and backslashes), then rsh's mvdan
  parser reads the same text again to *run* it. Two lexers over one line
  is a class of bugs, in two shapes. Approximation gaps: any bail sends
  the whole line to rsh, where a Scheme head is "command not found" —
  the fallthrough hint (remaining-work item 2), the Globs rule, the
  `where` Operators carve-out, and D10's leading-flag rule are each a
  patch to the approximation. Reconstruction: anything going tokens →
  text is lossy — instruction tails were rejoined with spaces, so `job
  claude "what time is it?"` reached rsh unquoted, four arguments
  instead of one (first patched by carrying byte spans on tokens and
  slicing the source; D11 subsumed it).
  The decision: lex once, with the real grammar. rf already owns it —
  mvdan's `syntax` package, the parser rsh executes with. Parse the line
  to an AST and dispatch on it: a word whose `Lit()` is non-empty is a
  bare word (eligible as head, flag, keyword, or glob pattern), any
  other word is a literal argument (today's `quoted`); pipelines are
  `BinaryCmd` chains, so stage boundaries and the raw spans handed to
  rsh for external stages come from the AST, not a quote-aware scan for
  `|`; an instruction tail is the source span from the first non-flag
  word to the statement's end, verbatim — redirects included, so `job
  make test > out.log` hands rsh the whole command line; redirects,
  `;`, `&&`, and expansions become explicit nodes the reader *decides*
  about. As built: `$VAR` (and a leading `~`) under a Scheme head
  expands through mvdan's `expand` against rsh's session overlay
  (`Session.Environ`) and arrives as a literal argument — an
  expansion's value is data, never a flag or pattern; command
  substitution, arithmetic, process substitution, brace expansion, and
  extended globs under a Scheme head are a precise error naming the
  feature; a backslash-escaped word is literal (sh's third quoting
  mechanism); a compound line (`;`, `&&`, `||`, `&`, `!`, `|&`) is the
  shell's whole, and the fallthrough hint stays for exactly that case. Scheme lines are untouched:
  `isSchemeLine` is a first-byte decision made before any parser, and
  the Scheme reader parses those — already the one-lexer shape; it must
  stay in front of the sh parser (mvdan reads `(+ 2 3)` as a subshell).
  The backtick is the one overlap (quasiquote to rf, substitution to
  sh) and rf's reading wins, a surface claim in the spirit of the
  carve-out below.
  Two consequences settle together. First, **`where`'s comparison
  spellings change to test(1)'s**: `where` is the only Operators head,
  and under sh's grammar `where size > 100000` is a redirect to a file
  named `100000` and `>= 5` a redirect to a file named `=`. The angle
  brackets are replaced by `-gt -lt -ge -le`; `=` and `!=` stay (plain
  words to sh, and test(1)'s own string operators): `dir | where size
  -gt 10MB`, `where type = file and size -gt 100MB`. To sh `-gt` is
  just a word, so the declaration moves out of the lexer into the flag
  parser the DSL already owns — `where` declares its operator words in
  `CommandMeta.Operators` (now a word list) the way options are
  declared, D7's unknown-flag error holds for everything else, and the
  lexer rule plus the `fallthroughHint` special case are gone. The
  symbolic spellings survive in parens (`(where "size" ">" 100000
  rows)`); `-eq`/`-ne` ride along for test(1) symmetry. Old
  command-mode spellings removed, not aliased (D7's stance) — with one
  migration guard: a `>` or `<` redirect on a `where` head errors
  naming the new spellings, since silently writing a file named
  `100000` is the one failure worse than an error. Second, **redirects on Scheme stages desugar
  to Scheme spellings** — the DSL owns surface syntax, never semantics
  without a Scheme spelling: `history > out.tsv` is `(pipe (history)
  (write-file "out.tsv" _))` (TSV, the same text that crosses a pipe —
  principle 3 covers it), `>>` is `append-file`, `< file` feeds the
  stage `(file ...)`; `2>`, `2>&1`, `&>` on a Scheme stage error
  precisely, naming the redirect — a Scheme stage has one output
  channel, a value. A `>` on a middle stage errors too (it would starve
  the pipe), a `<` makes a first head a stage — `take 1 < names.tsv`
  is `(pipe (cat "names.tsv") (take 1))`, so stage words qualify there
  — and `write-file`/`append-file` learned to take rows, serializing
  them as the TSV a pipe crossing emits. External stages keep both
  channels and sh owns them unchanged (`make test 2>&1 | agent why did
  this fail` already works, that stage runs in rsh untouched). Rejected: running the whole line
  in rsh with each run of Scheme stages as an in-process command. It
  would give Scheme heads `2>&1`/`&&`/`;`/`&` for free, but mvdan
  drives pipeline stages concurrently and Scheme may only run on the
  eval goroutine (SCHEME.md Phase 9 confinement); a rendezvous back to
  that goroutine deadlocks once two Scheme runs share a pipeline with a
  bounded pipe between them. The Scheme form stays the spine, external
  segments stay `(sh ...)` stages — confinement by construction, and
  the desugar fits inside it with no new machinery. A consequence
  found on landing: **no command word may be one of sh's reserved
  words** — `done 1` is a stray loop end to the parser — so the task
  closer `done` was removed in favor of `task -c` (already the agent
  spelling) rather than masked around; `eval/command_test.go` pins the
  reserved set out of the registry. Also rejected:
  registering command words as rsh builtins (the shell as the only
  parser) — argv arrives post-expansion, so bare-vs-quoted is gone, and
  rows between Scheme stages would serialize to bytes through rsh's
  pipes. Sequencing: the `where` spelling change and the redirect
  desugar could have landed on the old reader first; in the event all
  three landed together, the AST reader (`cmd/reader.go`:
  `parseCommandLine` → `dispatchCommandLine`) replacing
  `splitPipeline`/`splitSimpleCommand`/`tryBuiltinCommand`/
  `tryUserCommand`/`tryBuiltinPipeline` wholesale and retiring the bail
  list. Pinned by `cmd/shell_test.go` (TestParseCommandLine, and
  TestSplitSimpleCommand as the word-classification parity table) and
  `integration/shell_integration_test.go` (TestSchemeRedirects,
  TestRowVerbsPipeline, TestFallthroughHint).

## Remaining work

Roughly in order of how often the edge bites:

1. ~~**Build D6, the hydrate/harvest loop.**~~ Done via D8 — `export
   FOO=1`, `FOO=bar`, and `cd x && make` are native state now (`umask`
   remains an accepted, low-priority corner). Pinned by
   `integration/state_test.go` and `integration/venv_test.go`.

2. ~~**Fallthrough hint when a Scheme head hits shell syntax.**~~ Done:
   the exec handler records the names it can't resolve
   (`rsh.Session.NotFound`, reset per line), and a fallthrough whose
   not-found name is a command word or user-defined function appends one
   gray hint line — the `| write-file` spelling when the line redirects,
   the parens spelling otherwise (Operators heads like `where` always get
   parens: their `>` is a comparison, not a redirect). Evidence-based, so
   an unrelated failing line never hints — and a Scheme word shadowed by
   a real PATH binary (`dir` on GNU systems) runs that binary without a
   false hint. Pinned by `rsh/jobctl_test.go` (TestNotFoundRecorded) and
   `integration/shell_integration_test.go` (TestFallthroughHint).

3. **Definition-time shadow warning.** The invariant protects builtins,
   not users: `(define (test) …)` hijacks `/usr/bin/test`. Warn when a
   `define` or `alias` name matches a PATH executable.

4. ~~**Prune registry cruft.**~~ Done: the `Flags` field of `CommandMeta`
   deleted (replaced by D7's `Options` table), and the R7RS strays
   (`string-foldcase`, `list->vector`, `vector->list`, `member`, `assoc`,
   `reverse`) lost their stage registration. `string-upcase`/`-downcase`
   kept theirs — `printf x | string-upcase` is a real text stage (and
   integration-pinned).

5. **Discoverability.** Partly addressed by D7: bare `help` lists every
   command word with its doc line (as rows, so `help | grep rank`
   composes), and every command answers `-h`. Remaining: nothing at the
   prompt *unprompted* hints the structured world exists — a welcome line
   is still worth considering.

6. ~~**Build D11.**~~ Done: `where`'s test(1) spellings, redirect
   desugar on Scheme heads, and the AST reader over mvdan's `syntax`
   package (`cmd/reader.go`). The bail list is gone; the fallthrough
   hint (item 2) stays, now only for compound lines. Open edges worth
   watching: `#` starts a comment under every head now (sh's reading —
   `agent what does # mean` loses its tail), and `$VAR` expands under
   Scheme heads while `$(...)` errors — the door to running command
   substitution through rsh is open but unwalked.

7. **Watch `| where` etc. in practice.** If stage words still feel like
   landmines after real use, cutting them (D3's rejected branch) remains a
   one-way door that can be walked through later — but only after seeing
   an actual misfire, not on principle.

## Where the invariants live

- Shadow list and command/stage coherence: `eval/command_test.go`
- Native dispatch, TSV crossing, pipelines end-to-end:
  `integration/shell_integration_test.go` (`TestNativeCoreutils`,
  `TestNativeDispatch`, `TestBuiltinPipeline`, `TestExternalHeadPipeline`)
- TSV stdin unit pin: `eval/interop_test.go` (`TestShStdinRowsTSV`)
- The reader (D11): `cmd/reader.go` (`parseCommandLine` over mvdan's
  AST, `dispatchCommandLine`, `stageRedirects`); the flag and argument
  readers it feeds stay in `cmd/shell.go` (`commandCallArgs`,
  `instructionFlags`); registration beside each builtin
  (`eval.Register`, `eval/command.go`)
- The executor (D8): `rsh/` (session/harvest in `rsh.go`, job control in
  `jobctl.go`, tests beside); state loop pinned by `rsh/rsh_test.go`,
  `integration/state_test.go`, `integration/venv_test.go`; ^Z parking by
  `rsh/jobctl_test.go` and `integration/jobs_test.go`
- Enforced summary: `CLAUDE.md` "Key invariants"
