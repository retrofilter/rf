# ERGONOMICS.md — could an agent work Scheme-only?

A design record from an experiment we didn't have to run on purpose: on
2026-08-20 a coding agent (Claude) worked through the full F9–F16 fix
session in this repo using a conventional Bash tool surface, then audited
its own transcript against the question *"what if the only surface had
been rf's Scheme?"* This document is that assessment, kept because it is
grounded in a real work sample rather than intuition, plus the gap list
that falls out of it. The thesis it supports is CLAUDE.md's: the agent
and the user sharing one language and one database is the point — this
asks what the language still owes the agent.

## 1. What the session actually used Bash for

Nearly everything fell into three buckets:

1. **Run a program, get facts back.** `go test`, `go vet`, `git status`,
   `git diff --stat`, `go env GOMODCACHE`. Dozens of invocations.
2. **Search and read code.** `grep -rn` across trees (including module
   sources under the Go module cache), almost always with `-B`/`-A`
   context windows; ranged reads of large files (lines 1700–1860 of
   `eval/interpreter.go`).
3. **Author and iterate.** Appending test functions via heredocs, editing
   files, then looping: a 10-iteration `-race` rerun with per-run output
   captured to files while chasing a flake.

## 2. Where Scheme is already equal or better

- **Structured process results.** `(sh "go test ./..." :full)` returning
  `{:stdout :stderr :exit}` beats a Bash tool: the exit code is data
  instead of something inferred from text, stderr arrives separated, and
  `PrintValueCapped` (50KB) bounds output at the LLM boundary far more
  gracefully than blunt tool-output truncation.
- **Filtering output.** The session's `tail`/`grep -B/-A` over test
  output is `pipe` + `where`/`take` over lines; over rows the pipeline
  verbs beat awk outright. Structured beats textual wherever rows exist.
- **Native capabilities Bash lacks.** The task graph, `messages`,
  `hybrid`/`bm25`/`similar`, `remember`/`recall` — an agent currently
  reconstructs weaker versions of these by parsing text.
- **Loops and retries.** A real language wins over shell control flow;
  the flake-hunt loop would have been *nicer* in Scheme.
- **Auditability as approval leverage.** Command mode's one-visible-form
  invariant means the user approves a *form*, not an opaque string — a
  structural advantage no Bash allowlist can match, and the foundation
  §4 builds on.

## 3. The gaps — what made Bash still necessary

Ranked by how often the session hit them.

1. **Code search ergonomics.** The single biggest daily-driver loss.
   `grep -rn` with `-B3 -A25` context windows carried the whole
   code-reading workflow. The grep/grepdir surface needs: line numbers in
   results, surrounding-context lines (before/after options), and
   performance that holds up on large trees (the module cache, not just
   the repo). Ranged reads want a first-class spelling too —
   `(pipe (cat f) (drop 1699) (take 160))` works but a `cat` range
   option says it better.
   *Shipped 2026-08-23*: `grep` takes `{:before N :after M}` / `{:context
   N}` / `{:line-numbers #t}` on line inputs (context rows unmarked, so
   only matches highlight; directory greps always carried line numbers),
   and `cat` takes `{:from N :to N :line-numbers #t}` — the range closes
   the file at `:to`, so a 160-line read of a huge file stops there.
2. **Process control.** `(sh ...)` wants a `:timeout` option and a
   first-class job handle — `(job "make test")` → poll / wait / kill —
   before long-running work is trustworthy without a terminal. The
   lazy-stream kill-on-abandon semantics are already the right instinct;
   this extends it to processes the agent deliberately leaves running.
   *Shipped 2026-08-23*: `(sh "cmd" {:timeout N})` in both shapes, and
   the job quartet — `job` / `job-status` / `job-wait` (sh `:full` shape,
   own `:timeout`) / `job-kill` — with `jobs` listing handles beside the
   shell's stopped and `&` children (eval/job.go).
3. **File authoring friction.** Heredoc-appends and multi-line writes
   need an equally frictionless Scheme spelling: multi-line string
   literals into `write-file`/`edit` without quoting pain. (The chat-mode
   native `read`/`edit`/`write` tools already cover this; the gap is the
   `rf -e` / script surface.)

The realistic goal is **not** eliminating the `(sh ...)` escape hatch —
the coreutils long tail is unwinnable and every surface gap becomes an
escape anyway. It is making escapes rare enough that approval prompts on
them stay tolerable.

## 4. Approval gating: the crux

The current design is well-positioned — fail-closed `rf -e` for assistant
callers, per-op y/N in chat, `with-approval` batching,
`agent-allow-working-dir` as a standing directory grant. What automation
needs is the policy layer between "prompt for everything" and a blanket
grant:

- **Declarative allowlists in the prelude.** `go test ./...`,
  `git status`, `git diff` are read-only in effect and ran dozens of
  times this session; each would have been a prompt. Config is data —
  the allowlist is a prelude binding, matching commands by pattern.
  *Shipped 2026-08-23* as `agent-allow-commands`: a list of word-prefix
  patterns consulted by `sh` and `job`; commands with shell
  metacharacters always prompt, the binding is user-only to bind, and
  the gate reads only the global environment (eval/approval.go).
- **Read-only auto-allow under the project root, with an audit trail.**
  Reads under the working directory logged rather than prompted;
  `agent-allow-working-dir :read` is the seed, the audit log is what
  makes widening it comfortable.
- **Named batch grants.** The agent requests a scoped grant in its own
  words ("running the test suite repeatedly for this task"); the user
  approves once; the grant is visible, revocable, and expires with the
  task. `with-approval` is the one-prompt ancestor of this.

## 5. Verdict

Scheme-only would be workable today with friction; with §3's three gaps
closed and §4's policy layer, it plausibly becomes *preferable* for this
codebase's own workflows — because most of what an agent does here is
reconstruct, by parsing text, structure that rf already has natively.
The honest residual cost is muscle memory: an agent knows the unix flag
long tail by heart, and every novel surface must earn its place against
that. rf's existing discipline — options declared once, loud arity
errors with hints, `help` generated from the same table — is exactly the
mitigation, since the agent can recover the surface from the tool itself
instead of from memory.
