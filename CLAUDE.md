# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.
Have a look at @README for the main project pitch.

## What this is

retrofilter is a **programmable, persistent shell**: an interactive terminal REPL
where the user and an LLM share one language (Scheme) and one local SQLite database
(graphs, history, sessions). The LLM's primary tool is `scheme` — it writes programs
against the same interpreter and database you type into, rather than calling a menu
of JSON functions (three narrow file tools — `read`/`edit`/`write` — sit alongside it
so file *content* never passes through Scheme string escaping). State is first-class
and yours: memories, history, and graph data are all queryable with the same forms,
by you or by the model. `docs/SHELL.md` is the design record for command mode's
unix/Scheme boundary — read it before changing dispatch, registration, or the
pipeline reader.

## Development Commands

- `make shell` - Start the shell — the main entry point
- `rf console` - Serve the retrofilter console (agent fleet dashboard) on localhost
- `make test` - Run all Go tests, including PTY integration tests (`RF_SKIP_INTEGRATION=1` skips those)
- `make build` / `make install` - Build the `rf` binary / copy it to `$(BINDIR)` (Homebrew prefix on macOS, `/usr/local/bin` otherwise). `install` never compiles — build first, so `sudo make install` doesn't rebuild as root. `build` stamps `rf --version` (`cmd/version.go`: tag from `-X …/cmd.version` — `git describe --tags`, "devel" untagged — commit from Go's vcs stamp, `go install`'s module version as fallback)
- Releases: a `v*` tag push runs `.github/workflows/release.yml` → goreleaser (`.goreleaser.yaml`): tests, darwin/linux × amd64/arm64 tarballs + checksums on the GitHub release. Module path is `github.com/retrofilter/rf` — the last element names the `go install` binary
- `sudo make install-service` - Install and start the console systemd unit (`contrib/systemd/`) for the invoking user (Linux only)
- `make integration` - Just the PTY integration tests
- `make lint` - `go vet` plus the comment linter (`tools/commentlint`, a
  `go/analysis` analyzer; `make lint ARGS=-fix` deletes offenders). The rules:
  doc comments only on exported declarations (a method on an unexported type
  is unexported) and at most three lines; no free-floating comment blocks and
  no multi-line comment groups anywhere else; test files allow only brief
  (≤80 char) single-line comments inside function bodies. Directives
  (`//go:embed`, `//go:build`) and generated files are exempt. `TestRepo` in
  the analyzer's package runs it over the whole module, so `make test` fails
  on a violation too. Design prose belongs in the `docs/*.md` records, not in
  comments.

The shell loads environment variables from `~/.rf.env` at startup (optional;
e.g. `RF_ANTHROPIC_API_KEY`) and evaluates the optional prelude `~/.rf.scm` in the
shared global environment — prelude errors print but never block startup.

SQLite is `modernc.org/sqlite` (pure Go): FTS5 built in, no cgo, no build tags.

## Architecture

- `main.go` / `cmd/` — the single `rf` command. Shell loop and REPL state
  (`cmd/shell.go`), the **command-mode reader** (`cmd/reader.go` — one
  parse of each line with mvdan's shell grammar, dispatch over that AST:
  SHELL.md D11), terminal rendering — markdown, tables, the prompt
  (`cmd/render.go`), configure/resume pickers (`cmd/configure.go`, `cmd/resume.go`,
  `cmd/picker.go` — `pick` single-select, `pickMulti` checkboxes), job control
  (`cmd/jobs.go`), the `agent` builtin (`cmd/agent.go`). `cmd/onboard.go` is
  the **setup wizard** — `configure` runs it, and the first run (database
  created this process, no `~/.rf.scm`, a TTY — `RF_SKIP_ONBOARDING=1`
  suppresses it, the PTY harness sets that) triggers it: provider (a known
  name, or any anthropic-compatible endpoint by name/url/key), model,
  thinking, the Claude Code hooks. The agent grants are *written*, not
  asked — defaults (`:read` under the cwd, git status/diff/log) are seeded
  when unbound, and `configure --allowed-commands` is the checkbox editor
  for the command list. Everything machine-written lands in the *single*
  marked "rf config" block of `~/.rf.scm` (terse markers, no inner
  comments; `writePreludeBlock` replaces it in place, carrying over the
  half not being re-chosen from the live bindings, and refuses when the
  prelude doesn't parse); the prelude's existence is the onboarded
  marker, so esc still writes one.
  `cmd/help.go` + `cmd/man.go` are the **man-formatted manual**: `help` is
  RF(1) — the guide intro plus an index of every page — `help <command>` a
  command's man page generated from its CommandMeta, `help <topic>` a
  section-7 page cut from the embedded guide (`docs/GUIDE.md`, package
  `docs`; one page per `##` heading, matched by slug, with a doc line in
  `topicDocs`). Pages render man typography directly (no groff) and open
  in `$PAGER`/`less -R` for the user; the assistant gets plain text as a
  stream; `help --list` is the pages as rows for pipelines. `<command> -h`
  stays the short usage text (`eval.CommandHelp`) — the -h/man split.
  `TestGuideCoversRegistry` pins the guide to the command registry (a new
  command word needs a mention or an exemption), `TestManTopics` pins
  `topicDocs` to the guide's headings.
  `rf -e 'form'` (`cmd/eval.go`) is one-shot Scheme against the shared database —
  the graph surface agents shell out to (the /task skill's spellings). No prelude;
  caller identity passes through automatically: a harness marker in the environment
  (`CLAUDECODE`) runs the form as the assistant with a **fail-closed** approval
  gate — graph verbs work, gated filesystem builtins error instead of prompting —
  which is what makes a broad `rf -e` allowlist in the harness safe.
- `eval/` — the Scheme interpreter plus the shell layer, one subsystem per file with
  tests beside it. R7RS-small conformant; `eval/SCHEME.md` documents the plan,
  settled decisions (D1 numerics, D2 pairs, …), and deviations. Highlights:
  - `interpreter.go`/`parser.go` — the value types, environments (frames are
    slot arrays, globals are cells), macros, `pipe` (thread-last with `_`
    placeholders); `compile.go` — **closure compilation with lexical
    addressing** (SCHEME.md Phase 10): every form compiles once into a tree
    of Go nodes with variables pre-resolved to `(depth, slot)` or a global
    cell, macros expanded at the site, the core forms compiled natively
    (`compile_forms.go` — the binding and control forms: guard,
    parameterize, let-values, define-record-type, case-lambda, delay,
    let-syntax…) and a long tail (with-graph, include, cond-expand…)
    running through a fallback node over the old `SpecialFormFunc`
    signature; the run loop drives tail calls and owns the frame pool.
    `fastbuiltin.go` — allocation-free 1/2-arg paths for hot builtins;
    `gabriel_bench_test.go` — Gabriel benchmarks (SCHEME.md Phases 9–10
    record the chibi comparison and the optimization decisions)
  - `stream.go` — lazy single-use line streams; `shell.go` — `ls`/`cat`/`sh`/`glob`/…
    returning Scheme values; `grepdir.go`/`grepmatch.go` — grep over directory trees
  - `job.go` — background jobs with handles, one command word (`job` starts;
    `-s`/`-w`/`-o`/`-k` poll/join/peek-output/kill via
    `{:status ID}`/`{:wait ID}`/`{:output ID}`/`{:kill ID}`;
    `--in`/`--every` schedule delayed and recurring runs, process-lifetime only;
    process-global registry, capped output buffers, gated like `sh`)
  - `rows.go` — `where`/`sort-by`/`group-by`/`count-by`/`pick`; `diff.go` —
    `(diff old new [:text])`, change rows or unified text; `serialize.go` /
    `deserialize.go` — `json`/`text` out, `parse-json`/`from-csv` in
  - `similar.go`/`bm25.go`/`hybrid.go`/`chunk.go` — local semantic + keyword ranking
    (go-potion embeddings; live test gated behind `RF_LIVE_POTION=1`); `stopwords.go` —
    keyword rankers drop Lucene's stopwords unconditionally (settled empirically on
    the BRIGHT benchmark; stemming was tried and removed — see stopwords.go)
  - `port.go`/`read.go`/`write.go` — R7RS ports & I/O; `library.go` —
    `define-library`/`import`; `evalproc.go` — `eval`/`load`; `process.go` —
    process context, `exec`
  - `graph.go` — graph builtins (`with-graph`, `remember`/`recall`, …; the default
    graph comes from the `default-graph` binding); `registry.go` — the project
    registry (`register-project`/`unregister-project`: projects as `project` nodes
    in the default graph, consulted registry-first by `FindProject`); `task.go` —
    tasks as graph nodes (`task`/`tasks`/`task -c`, `for`/`blocks` edges);
    `note.go` — notes as graph nodes (`note`/`notes`: named markdown
    documents with YAML front matter, `$EDITOR`-edited, `for` edges);
    `ref.go` — the **reference grammar** (`task:ID`/`note:SLUG`/`project:NAME`/`#ID`,
    `[[ref]]` wikilinks in note bodies and task text → `links` edges,
    `node` resolves any ref) — `docs/NOTES.md` is the design record;
    `history.go`, `messages.go` (`messages` — search the unified
    chat-message corpus, rf + synced Claude Code; the candidate feed for
    `messages -d . -n 2000 | hybrid "q"`), `project.go` (`project` — enter/create/describe, `--clone`/`--init` hubs,
    `-e`/`--text` markdown notes on the node's `text` property, shown on the
    console overview; a bare name with nothing behind it files a pathless
    project node, no directory), `alias.go`,
    `inspect.go` (+ `persist`), `fetch.go`
  - `command.go` — the command-word registry: every builtin declares its
    command-mode presence beside its definition (`CommandMeta` — doc line,
    Command/Stage capability, arity, flag compat, globs); `command_test.go` pins
    registry ↔ environment coherence
- `core/` — GraphStore and Graph: reads/writes go straight to SQLite (no in-memory
  cache, so several rf processes can share the database); path algorithms run on a
  per-query snapshot (`core/graph_snapshot.go`). The one deliberate cache is the
  quantized-embedding cache (`core/embedding.go`): node embeddings quantized
  to 1 bit/dim (64B/doc at
  512d), kept in one mmap'd file per (graph, model) under
  `<db-dir>/cache/embeddings/` (`~/.rf/cache/embeddings` for the main db) —
  deliberately *outside* SQLite, since embeddings are derived data and the db
  is the backup surface, and mmap'd so every console tab shares one physical
  copy via the OS page cache (no RAM-budget knob: clean pages are
  kernel-reclaimable). Coherence: SQLite stays source of truth — each search
  point-reads the trigger-bumped `node_version` counter, and when it moves the
  process takes the file lock and reconciles (delta re-encode by `updated_at`
  watermark + append; deletes/updates tombstoned per-process, compacted by
  rewrite; per-record CRC32-C truncates torn or rotten tails back to the last
  good record, and the delta re-encodes the rest). Ingest does no embedding
  work at all. Search = Hamming scan over the mapping (POPCNT, sharded past
  64k docs) to top-D=500 → re-encode candidates (`EncodeMany`, float vectors
  are never stored) → exact cosine (tphakala/simd) → RRF fusion with the FTS
  arm in `HybridSearchNodes`; a first-reconcile sweep GCs files stranded by
  graph deletion or a long-unused model. On by default — the
  `graph-embeddings` binding opts out; the FiQA benchmark (`core/fiqa_test.go`, `RF_FIQA=1`)
  pins the recall/latency numbers. (A message-corpus arm on the same
  engine was built and removed by decision — the graph is the semantic
  surface; `messages | hybrid` covers transcripts ad hoc.)
- `models/` — SQLite models: graphs/nodes/edges (with `node_fts`), history,
  sessions. (No settings table: model configuration is the prelude's
  `default-model` dict, credentials are `~/.rf.env` — the database holds
  data, not config.) Migrations in `models/schema/migrations/` — one
  squashed baseline at public release (`20260904_baseline`, every
  statement idempotent, so a database carrying the pre-release chain's
  versions applies it as a no-op and those versions become orphans);
  `schema.Migrate` fails closed (`ErrSchemaNewer`, before any write) when the
  database records a migration *later than* the newest this binary carries — an
  older rf against a newer db must stop, not run old code over tables it
  doesn't understand. Unknown versions that sort *before* the newest known are
  orphans (migrations reverted during development after a dev build applied
  them) and are ignored, never fatal. Chat transcripts persist via an
  append-only event log (`models/session.go`, table `message`);
  `SessionTranscript` rebuilds the
  message list, the latest compaction summary standing in for everything before its cut.
  The same session/message tables hold transcripts synced from Claude Code
  (`session.source` 'claude'; only rf sessions are resumable/listed), with the
  origin line uuid as dedupe key and `message_sync` checkpointing byte offsets.
- `agent/` — the seam for foreign coding-agent harnesses whose transcripts rf
  mirrors into the message corpus: `agent.Source` (Name + Sync), driver-style
  init registration, `agent.Sync` sweeps every compiled-in source (callers
  blank-import the harnesses). `agent/claude/` is the first source: imports
  Claude Code's `~/.claude/projects/**/*.jsonl` transcripts —
  incremental (append-only files + offset checkpoints),
  idempotent (uuid dedupe), text-only (tool traffic, thinking, sidechains, meta
  lines, command wrappers, system reminders all dropped). Claude Code deletes
  transcripts after ~30 days; this mirror is what outlives them. Reached via
  `rf sync` (`cmd/sync.go`), `messages --sync`, and per turn via the Stop
  hook (`rf hook --fire` → `syncTranscript` → `claude.Source.SyncFile`).
  Text search over the corpus is a deliberate LIKE full scan (measured ~25ms
  per 100k messages; ranked search is the pipeline's job — `bm25`/`hybrid`
  read rows regardless of index), with `message_created_idx` keeping the
  recent-N default off the sort path; an FTS mirror à la `node_fts` is the
  documented retrofit if the corpus ever outgrows scanning.
- `llm/` — chat with function calling. Tools: `scheme` (executes in the shared
  evaluator) plus native `read`/`edit`/`write` (`tools.go`, `edit.go`). The
  system prompt (`prompt.txt`, embedded) is deliberately small — identity,
  the tools, the Scheme idioms `help` can't teach (dict literals, key-first
  `get`, `pipe`/`_`, lazy streams, the bare-string-is-a-path input
  convention), how to look things up, and the ground rules; the builtin
  catalog lives in `(help)` / `(help "name")` / `(help "topic")` and the
  model is told to consult it rather than carry it. Measured on a CPU-only
  Ollama box: ~1.5k input tokens with tool schemas, where the old 14KB
  catalog prompt was ~4k — which filled Ollama's default 4096 context
  outright, triggering context shift and a cache miss every turn.
  `TestPromptNamesTopics` (`cmd/help_test.go`) pins the prompt's topic
  list to `topicDocs`. Hand-rolled
  Anthropic Messages API client (`client.go` — no SDK); the provider seam
  is just (base URL, key), so Anthropic, Ollama's compat endpoint, and any
  other Messages-API-compatible endpoint are all one client
  (`NewClientForConfig`; the `default-model` dict names the provider and
  carries `:api-key` — configure writes `(env "RF_<PROVIDER>_API_KEY")`
  explicitly, and `models.APIKeyEnv` derives that same name when the
  dict has no key. Provider choice is config, never key presence: a
  bare `claude-*` id means Anthropic — `models.ProviderForModel` — else
  the Ollama default, so tests never unset host keys).
  Agentic loop with max 24 tool iterations (`chat.go`), SSE streaming via
  `OnDelta`/`OnEvent`, extended thinking (off unless the dict opts in — no
  provider gate; `:thinking` picks the wire shape and `:effort` feeds
  `output_config`;
  thinking blocks stream as `DeltaThinking`, surface as display-only
  `EventThinking` — never persisted — and replay signature-intact on tool-use
  continuations because the loop appends `resp.Content` verbatim),
  prompt caching (`:caching` — defaults on for Anthropic only),
  retry/backoff (`retry.go`), usage & cost
  tracking (`usage.go`), compaction — manual, auto near the context limit, and
  overflow recovery (`compact.go`), AGENTS.md system-prompt context (`agents.go`),
  prelude hooks (`hooks.go`), the `(llm ...)` builtin (`builtin.go`), the
  pipeline verbs `llm-map`/`classify` (`map.go` — batched numbered completions,
  per-item fallback, item-count cost guard), and `rerank` (`rerank.go` —
  RankGPT-style sliding-window listwise reranking, `bm25 "q" corpus -n 100 |
  rerank "q"`, `{:model "id"}` per-call override; scored on BRIGHT).
  `rf script.scm args...` wires the function-shaped LLM builtins from environment
  config alone (`NewChatForScript` — no db, no sessions, no prelude); script
  arguments surface via `(command-line)`.
- `logger/` — shared logging.
- `console/` — `rf console`, the retrofilter console: a localhost dashboard over a
  session fleet.
  **No tmux** — a session is a direct child PTY of rf console (`proc.go`: the
  Manager) running an rf shell spawned in `$HOME`, named by the user in the
  in-panel **new-session view** (a terminal-styled name input that replaces
  the terminal; ⌘K opens it, an existing name attaches instead of erroring).
  The landing view — fresh startup and whenever the fleet empties; nothing
  ever auto-spawns — is the **overview** (`console/overview.go`, a pinned
  "overview" tab): projects and their open tasks read straight from the
  knowledge-base graph (the same nodes `register-project`/`task` write;
  `rf console` opens `~/.rf/main.db` and passes a GraphStore in
  `Config.Store`). Rows are keyboard-driven (↑/↓/enter) or clicked: a task
  row spawns a session cwd'd to its project's path with `claude '<task
  text>'` typed into the fresh shell (the initial command waits for the
  shell's first *prompt* — the OSC title/cwd report rf emits with every
  prompt, strictly after its startup theme probe has released stdin, so
  the probe can't eat the line; 5s fallback for shells that never emit
  one — and is wrapped in bracketed-paste markers so readline inserts it
  in one edit instead of a slow key-by-key replay), a project row spawns a plain
  session there; names derive by slug, attach-or-create like the naming
  form; the listing re-fetches on show (`GET /ui/overview`), not per poll,
  so keyboard selection survives. Every tab except the pinned overview
  carries a close ×: a session tab's × hangs the session up (`POST
  /ui/close` → `Manager.Close`, SIGHUP to the process group plus PTY-master
  close — the mouse spelling of Ctrl-D, funneling into the reader's normal
  EOF cleanup), the graph tab's × just closes it, the pending naming tab's
  × cancels. The **graph view** (`console/graph.go` + `console/static/graph.js`) —
  the knowledge-base graph drawn as a force-layout canvas — opens from a
  quiet ink-on-grey icon button in the bottom status bar, just right of the
  user@host title block, as a closable "graph-view" tab; whether that tab exists is client state like the active view
  (`RF.graphOpen`, echoed on each poll). `GET /ui/graph` serves the whole graph —
  nodes, edges, properties — as one JSON payload (capped at 2000 nodes,
  flagged truncated), and graph.js owns everything inside the view: the
  simulation (cooling alpha, auto-fit until the user pans/zooms), pan/zoom/
  drag/hover, the type legend whose chips toggle visibility, and the node
  detail panel (properties + directed edges, neighbor rows navigate) —
  DOM-built with textContent, never innerHTML, since properties are user
  data. Node-type colors are the One Light ANSI hues in a fixed order
  validated CVD-safe (red and green never adjacent); positions persist at
  module scope so re-entering the view or a refetch never reshuffles the
  picture. Session *processes* live and die with
  the server, by design: keeping them alive across restarts was tried via tmux
  and rejected as an annoyance. What survives a restart is the fleet's shape
  (`console/state.go`): the Manager mirrors {spawn name, display name, cwd,
  claude session id} to `~/.rf/state.json` on every fleet change plus a
  1-minute ticker (cwd moves via OSC 7 without a fleet event), and startup
  respawns the file's sessions as fresh shells — except a shell that was
  inside a claude conversation, which comes back with `claude --resume <id>`
  typed in, so the conversation outlives the restart (skipped when the
  session's directory is gone — claude scopes transcripts per project dir).
  The id is hook evidence: every hook payload's `session_id` reaches
  `Manager.SetClaudeSession` via `/api/hook`, and SessionEnd retires it —
  matched against the id it ends, since claude's /clear and /resume rotate
  ids and the old id's end can land after the successor's start. A session
  exited while the server lives leaves
  the file, so it stays gone; scrollback and running programs don't come back. Terminal semantics are fully native — output
  flows into xterm.js's own buffer, so scrollback, selection, and wheel
  behavior need no machinery; a per-session **ring buffer** (2MB) replays on
  attach so a refreshed tab repaints, and multiple tabs on one session all
  subscribe and mirror. Session metadata is parsed from the byte stream
  (`scan`): OSC 0/2 titles and OSC 7 cwd reports (rf emits both each prompt —
  `terminalTitle` in `cmd/render.go`), alternate-screen toggles; the harness
  ("rf" vs "claude" — a session *turns into* a claude session when the user
  runs claude in it) comes from the PTY's foreground process group. Terminal
  queries from a detached session are answered by the server only when the
  answer is position-independent (CSI 5n status, CSI c device attributes);
  cursor-position probes (CSI 6n) are held and forwarded on each attach
  (until one answer actually reaches the PTY — only the primary tab's
  passes the response scrub), and the attaching tab's xterm answers from
  the replayed screen — a fabricated
  position broke readline (duplicate prompt at boot), and disabling the probe
  instead broke bottom-of-screen redraws (readline's no-probe degraded mode
  self-erases the prompt on the last rows). The asker blocks until attach,
  which is harmless while nobody is interacting — except a session spawned
  with an initial command, which is `unattended` until its first attach
  and gets `ESC[0;0R` (readline's "unknown", prompt-width fallback), since
  readline won't act on the pasted line before that answer. The rail
  (`registry.go`) joins that with git diff stats and status: Claude Code
  hooks (PreToolUse→RUNNING, Notification→BLOCKED, Stop→REVIEW; SessionStart/
  SessionEnd carry no status but bracket the claude session id above) layered
  over an output-recency heuristic. Spawned sessions get `RF_SESSION` (hook
  identity), `RF_WEB_PORT`, and `TERM`/`COLORTERM=truecolor` — no theme env:
  rf probes the terminal background itself at interactive-shell startup (an
  OSC 11 query, `cmd/theme.go`; `rf -e` and scripts never probe, so they
  stay instant) and pins light/dark chroma/glamour styles for the session.
  The attached tab's xterm answers the probe with its real theme; a session
  detached at boot gets the server's One Light stand-in (`scan`, beside the
  CSI 5n/DA answers). The terminal theme defaults to One Light; the status
  bar's sun/moon toggle flips the whole console to a One Dark counterpart,
  per-browser via localStorage `rf-theme` — the CSS tokens in style.css, the
  xterm ramp in term.js, and the graph canvas in graph.js all key off
  `data-theme` on `<html>`; already-running shells keep the look they booted
  with (a theme flip never re-probes). Claude Code status arrives via opt-in global
  hooks (`rf hook --install` → `~/.claude/settings.json` — the user-level
  settings file; Claude Code has no user-level settings.local.json, that
  name is only read as project-local settings; installer in
  `hooks.go`, CLI in `cmd/hook.go` — bare `rf hook` reports install state):
  every event runs the same logic-free line, `rf hook --fire`, and dispatch
  lives in the binary (`fireHook`) — status reports go through the `Console`
  client (port/token plumbing, skipped in-process without `RF_SESSION`, so
  the install is inert in non-rf claude sessions), and two events do real
  work, both deliberately *not* `RF_SESSION`-guarded (they cover every
  claude session on the machine and touch only `~/.rf/main.db`, no server
  needed): Stop syncs the turn's transcript into the message corpus, and
  SessionStart prints one line — the cwd's project and its open-task
  count, pointing at `(tasks)` and the `/task` skill for the spellings —
  to stdout, which Claude Code injects as session context (deliberately
  minimal: the skill carries the depth, the agent looks when it wants) —
  SessionStart is the one event whose installed line keeps stdout
  (`hookCommand`), everything else silences it. `rf hook --install` also
  writes the `/task` skill (`console/skills.go` →
  `~/.claude/skills/task/SKILL.md`), the depth behind that line:
  exact `rf -e '(task ...)'` / `{:complete ID}` / `(tasks)` spellings and
  when-to-use guidance. Auth is
  jupyter-style (`token.go`/`auth.go`/`server.go`): localhost is not a
  boundary — every request is authenticated, and every secret expires. A
  persistent token (`~/.rf/webtoken`, 0600) is the password: machine
  callers (hooks, the `Console` client) present it per request, freshly
  read from its file, which is what makes its weekly age-based rotation
  free — `LoadToken` rotates on read past `tokenMaxAge`, the server
  re-reads the file lazily (`authState.currentToken`), and `rf token`
  (`cmd/token.go`) prints the current value for signing a browser in.
  Browsers never hold the token: presenting it (the `/login` form's POST,
  or a pasted `?token=` URL — scrubbed from the address bar by redirect
  on any GET) mints a server-side session, a random secret whose sha256
  lives in `~/.rf/websessions.json` (0600, hashes only) with a sliding
  14-day idle window and a 30-day hard cap, so restarts keep browsers
  signed in but idle or old sessions die on their own. The auto-opened
  startup URL carries a single-use 2-minute launch nonce
  (`MintLaunchNonce`) instead of the token, so browser history and the
  journal never hold a live credential — `rf console` prints no secret at
  all. `/login` is the one unauthenticated page, deliberately static
  (inline styles — `/static` sits behind auth — no whoami, no fleet
  data); an unauthenticated browser GET of `/` redirects there, every
  other unauthenticated request stays a plain 401. Shells reach the console through cookie-less query-token `/api`
  endpoints (`api.go`: rename, list, spawn) via the `Console` client
  (`client.go` — port from `RF_WEB_PORT` or the default, token read from its
  file, never generated); the builtins `rename-session` (identifies the
  session by its immutable spawn name, so hooks and repeated renames keep
  working — `Spawn` reserves live spawn names against reuse), `sessions`
  (fleet as rows — live sessions, then scheduled ones with their next
  run), and `session` (the positional is the command line typed into the
  fresh shell, `--name` the session name — defaulting to
  `console.SessionSlug` of the command, the overview's task-row slug —
  `--dir` the cwd; approval-gated for assistant callers
  like `sh`) live in `cmd/sessions.go`, registered for the shell and for
  `rf -e`. **Scheduled sessions** (`console/schedule.go`): `session
  [COMMAND] --in/--at/--every` writes a `schedule` node (name, dir, command,
  cadence, next/last run) into the knowledge-base graph via
  `console.PutSchedule` — the console owns the schema and the firing, the
  shell only files. The server ticks every 20s (`tickSchedules`): a due
  schedule always spawns a fresh session (`spawnNumbered`: `ErrNameTaken`
  means the last run is still up, so the new run takes the next free
  `name-N`), a recurring one advances
  `next` past now on its own grid (a console down for a week fires a
  missed daily once) and stamps `last`, a one-shot is deleted once fired.
  Runs are sessions that stay up, not jobs. The span parser is shared
  with `job` (`eval.ParseSpan`; `eval.ParseAt` reads `--at`'s clock
  times), `--cancel` deletes by name, re-filing a name re-times it.
  Scheduled rows ride the rail (`scheduledRow`: the countdown as the
  headline — "In 16 hours" — SCHEDULED, the command as the detail line,
  name/cadence/dir in the meta line; `data-schedule-id`, so term.js's
  row click never tries to attach) and `/api/sessions`, but never tabs,
  the session count, or the poll's auto-select — `viewModel.Scheduled`
  is a separate list from `Sessions`. The header bar shows `user@host` plus Claude subscription usage
  (session 5h / week), pushed by Claude Code rather than fetched: `rf hook
  --install` also wires `statusLine` to `rf statusline` (wrapping — never
  clobbering — a pre-existing statusline via `--wrap`), whose payload's
  documented `rate_limits` block is recorded into the shared cache
  `~/.claude/usage-cache.json` (`console/statusline.go`); `usage.go` is a
  pure reader of that file — no credentials, no keychain, no network (the
  old OAuth-token fetch read as credential theft to endpoint security and
  is gone). The cache never expires for display — stale bars beat blank
  ones after idle hours or a restart — except that a window whose reset
  time has passed shows 0%. Frontend is
  server-rendered templ + htmx: components live in `console/ui.templ` (package
  `console`, colocated so they share the `Session`/`Usage` types; regenerate
  `ui_templ.go` with `make templ` — generated files are checked in, builds
  don't need the CLI). htmx polls `GET /ui/poll` every 2s (`fragments.go`):
  the rail rows are the main swap and the rest of the chrome — tabs, counts,
  harness label, usage bars — rides along as `hx-swap-oob` elements, so one
  request updates everything. The browser owns only what a server can't:
  which session the xterm is attached to and whether the new-session view is
  open (`window.RF`, echoed back on each poll via `hx-vals`); the server
  renders selection and answers with `HX-Trigger` events (`rf:select` /
  `rf:shownew` / `rf:detached`) that `console/static/term.js` — the only
  hand-written JS, owning the xterm instance and its websocket — turns into
  attaches. The new-session view is an htmx form (`POST /ui/sessions`,
  `hx-sync="this:drop"` is the double-enter guard; an existing name answers
  `rf:select` for that session — attach-or-create). Browser dependencies are
  vendored single files under `console/static/vendor/` (htmx.min.js, xterm.js;
  go:embed, no node toolchain), one module-scope terminal re-pointed per
  session; the terminal mounts in an unpadded inner div (`#term-mount`)
  because the fit addon measures the terminal's direct parent. Tests spawn
  `cat` PTYs and assert over rendered fragments — no tmux, no browser, no
  external dependencies.
- `rsh/` — rf's native shell executor (SHELL.md D8): mvdan.cc/sh's interpreter
  in-process, no `/bin/sh` child anywhere. `rsh.go` holds the `Session` — command
  lines hydrate from process state plus cross-line shell vars/functions, and
  harvest mutations back (`export`/`unset` → os env, `cd` → `os.Chdir`, the rest
  to the session) — plus the harvest-free `Run` used by pipeline stages and
  `(sh ...)` (fresh-shell semantics). `jobctl.go` is job control: each line's
  external children share one fresh process group owning the terminal, reaped
  `WUNTRACED`; ^Z parks the pgid into `cmd`'s job table (`OnStop` → `parkJob`)
  and abandons the rest of the line (`ErrStopped` is fatal to the interp run —
  bash's semantics), `fg`/`jobs` in `cmd/jobs.go` resume it. Non-file stdio is
  bridged through real pipes whose copiers outlive park/resume; any writer
  shared with rsh must be concurrency-safe (interp writes stdout/stderr from
  concurrent pipeline stages — `lockedBuilder`/`cappedBuffer` in eval).

## Key invariants

Check changes against these — they are the design:

- **One evaluator, two callers.** User input and LLM tool calls share one
  `eval.Evaluator` + `Environment`, so definitions persist across turns. The
  evaluator tracks a dynamic **caller identity** (`eval.SetCaller`, flipped to
  `assistant` for each tool call): builtins that touch the filesystem — reads
  (`cat`, `ls`, `stat`, `grep`, `glob`, `wc`, rank/parse file inputs, `load`, the
  `read` tool) and writes (`rm`, `mv`, `cp`, `mkdir`, `sh`, `fetch`, `write-file`,
  the `edit`/`write` tools) — hit the y/N approval gate only for assistant
  callers; user-typed code is never gated. File ports gate at **file level**: the
  open prompts (read- or write-gated by direction), then the port's operations run
  free. The **`agent-allow-working-dir` prelude binding** (`:read` or `:write`) grants standing
  access under the cwd captured at the chat's first message (the same moment the
  session row is lazily created); the grant is first-wins per evaluator. The
  **`agent-allow-commands` prelude binding** (a list of pattern strings) is the
  command-side grant: `sh` and `job` skip the prompt when the command's leading
  words equal a pattern's words (`"go test"` covers `go test ./... -run X`) and
  the command holds no shell metacharacters (`;`, `|`, `$(...)`, redirection —
  those always prompt, so a covered prefix can't smuggle a second command). Both
  grant bindings are user-only to bind — assistant `define`/`set!` errors — and
  the command gate reads only the *global* environment, so neither a spawned
  `(agent ...)` nor a `let`-shadow can widen a boundary. `RequireUser`
  builtins (`configure`, `resume`, `compact`, `clear`, `exec`, `exit`, `alias`,
  `delete-graph`, and every project word — `project`, `projects`,
  `register-project`, `unregister-project`: the project set is the user's
  scope boundary, immutable and invisible to the agent, which only files and
  reads tasks under it) reject assistant callers even through user-defined wrappers, and
  `env` force-redacts credential-looking values for the assistant. Irreversible
  builtins (`delete-graph`) additionally consult the evaluator's **confirmer**
  (`SetConfirmer` — the shell's inline y/N, showing node/edge counts); with no
  confirmer installed (scripts, non-terminal `rf -e`) they fail closed.
  `(with-approval ...)` batches gated ops behind one prompt. Hooks and nested tool calls save/restore
  caller identity and approver — never reset them. The `rf -e` harness marker
  (`CLAUDECODE`) is fail-open by construction — `env -u CLAUDECODE rf -e …` runs as
  user. It's a cooperation contract with the harness (its value is making a broad
  `rf -e` allowlist safe for the *cooperating* assistant), not a security boundary
  against a caller that strips the marker.
- **Command mode is a DSL that desugars to Scheme, read with the shell's
  own grammar.** Each line is parsed once by mvdan's parser (the one rsh
  executes with) and becomes exactly one visible Scheme form — command
  words via `CommandMeta`, user-defined functions, pipelines → the `pipe`
  special form, unquoted globs → `(glob ...)` under declaring heads, `>`/
  `>>`/`<` on a Scheme stage → `write-file`/`append-file`/`cat` — or goes
  to rsh whole (any compound line). There is no second tokenizer: word
  classification (bare, quoted, `$VAR` expansion), stage spans, and
  instruction tails all come from the AST. `where` compares with
  test(1)'s `-gt`/`-lt`/`-ge`/`-le` because `<` and `>` are redirects
  under every head, and no command word may be one of sh's reserved
  words. The DSL may own surface syntax, but never semantics
  without a Scheme spelling (`>`, `&&`, `$VAR` stay the shell's) and never invisible
  expansion. Unrecognized flags and arity
  mismatches error with a hint at the `!` escape rather than silently running a
  different program. **Options are declared once** (`CommandMeta.Options`, beside the
  builtin) and read three ways from that single table: command mode parses unix flags
  (`history -d . --limit 10`, `--` ends flags, `-h`/`--help` everywhere) and desugars
  them into one trailing dict — the visible form is `(history {:dir "." :limit 10})`;
  Scheme spells the same dict directly via `eval.ParseOptions` (a bare keyword is
  accepted sugar for a boolean — `(dir :all)`); and `help`/`-h` renders usage from it.
  Long flag name = dict key, always — translation between surfaces is mechanical.
  Min/MaxArgs bound *positional* arguments only. `eval/options_test.go` pins table
  coherence (unique longs/shorts, `-h` reserved, docs and placeholders present). Rows crossing to an external command serialize implicitly as
  TSV (`shStdin` → `textLine`) so `history | grep make` is native grep over data
  lines; `json` is the explicit spelling when structure must survive the crossing.
  A bare string still never feeds stdin. **Names that shadow a system binary never register
  command or stage capability** (`ls`, `cat`, `grep`, `rm`, `sh`, `wc`, `head`, …):
  in command mode those words always mean standard unix behavior — the PATH binary,
  or rsh's in-process builtin for the handful it implements (`echo`, `test`, `pwd`,
  `kill`, …), as in bash itself; the builtins stay
  reachable with parens, and `dir` is the non-shadowing rows spelling of `ls`
  (GNU coreutils' near-unused `dir` is the one accepted shadow). Only novel
  vocabulary (`dir`, `where`, `similar`, `take`, …) may be a command or stage
  word — `eval/command_test.go` pins the shadow list.
- **rf owns its shell semantics.** The fallthrough executor is rf's own
  interpreter (rsh), not a delegated `/bin/sh`, so the state loop (env, cwd,
  vars, functions) closes by construction and unix behavior is a dependency rf
  can patch, not a black box it impersonates. Precedent: every clean-break
  shell (fish, nushell) ended up bridging foreign shell state; rf bridges by
  *being* the interpreter.
- **Scheme builtins are rf's MCP equivalent.** New capability arrives as a
  builtin the user and model both call, not as a model-only tool. Skipped
  deliberately (weighed against pi): MCP, the provider matrix, images, themes,
  RPC mode, session-branching UI (the `parent_id` chain keeps the door open),
  pi's file-mutation queue.
- **Streams are lazy and single-use.** `cat`/`sh`/`fetch` return line streams;
  transforms propagate Close upstream, so `(take 2 (sh "yes"))` kills the child.
  `Materialize` is the REPL boundary and `PrintValueCapped` (50KB cap) the LLM
  boundary; a spent stream errors `ErrStreamConsumed`.
- **Environments are goroutine-confined — no locks.** Every `Environment`
  read/write happens on the goroutine driving the evaluator (the same
  discipline the tail cell and frame pool rely on); the per-env RWMutex was
  ~30% of interpreter CPU and is gone (`eval/SCHEME.md` Phase 9). Streams
  whose pull runs Scheme carry `Stream.userCode` (set by `where`'s callable
  branches, propagated by every derived-stream constructor) and are drained
  on the eval goroutine before crossing to another (`shStdinReader`). New
  code must not evaluate Scheme or touch environments from a spawned
  goroutine — bring confinement, not a lock.
- **Config is data.** Prelude bindings, not config files: `default-model`
  — one dict (or a bare model-id string) holding `:provider` `:id`
  `:base-url` `:api-key` `:thinking` `:effort` `:max-tokens` `:context` `:caching` —
  is the whole model choice, resolved per request by
  `llm.Chat.resolveConfig` (so it applies live without a chat rebuild),
  written into the marked "rf config" block by `configure`. A dict naming
  just `:provider` falls to that provider's defaults; unknown providers
  need `:base-url` and `:id`, and a malformed dict fails the next model
  call loudly (`configErr`) instead of running defaults over a typo. The
  environment carries credentials only — `RF_<PROVIDER>_API_KEY`, derived
  from the provider name and read by the dict's `:api-key (env ...)`
  form (or implicitly when the dict has none); the `RF_` prefix keeps
  rf's key from reaching a vendor's own tool started from the shell
  (Claude Code takes an inherited `ANTHROPIC_API_KEY` over its login).
  Other bindings: `default-graph`,
  `project-root`, `embedding-model`,
  `graph-embeddings` (the semantic search arm — synced into the GraphStore
  at `resolveGraphArg`, the chokepoint every graph builtin passes),
  `agent-allow-working-dir`, `agent-allow-commands`, and the hook functions
  (`before-tool-hook` / `after-tool-hook` / `after-turn-hook`).
- **Sessions are an immutable log.** Compaction appends a summary entry, never
  deletes; a tool call and its result are one log row, so a cut can never split them.

## Database

SQLite with FTS5, file `~/.rf/main.db` (created automatically). `node_fts` indexes
node type plus every string value in the node's properties JSON, kept in sync by
triggers on `node`; `Node.SearchText` is the Go spelling of the same population,
and what the embedding arm encodes. The quantized-embedding cache lives *outside*
the database (`~/.rf/cache/embeddings/` — derived data stays out of the backup
surface); SQLite carries only `node_version`, a one-row-per-graph change counter
bumped by triggers on `node`, plus the `(graph_id, updated_at)` index the
cache's delta reads range over. Foreign key constraints enabled.

**Secrets never live in the database — and neither does config.** API
keys are environment-only: `~/.rf.env` (edited by `configure` via
`core.SetEnvVar`, loaded at startup) or the inherited environment, one
`RF_<PROVIDER>_API_KEY` per provider name; the model choice is the prelude's
`default-model` dict. Every entry point opens the
db through `openMainDB` (`cmd/db.go`).

## Testing

- Unit tests throughout with `_test.go` files beside each subsystem.
- `integration/` drives the built `rf` binary through a real PTY (creack/pty),
  matching expectations against ANSI-stripped output — see
  `integration/harness_test.go`. Each test runs with its own temp dir as both cwd and
  `HOME`, so the database, env file, and `~/src` are isolated per test. Add new
  end-to-end tests here, not as expect scripts. Runs as part of `go test ./...`
  (builds the binary in TestMain); skip with `RF_SKIP_INTEGRATION=1`.
- `llm/` tests use a scripted fake `Model` plus `httptest` SSE servers for the wire
  client; the live-Ollama test is gated behind `RF_LIVE_LLM=1`.
- `make webshot` (`console/webshot/`) is a dev tool, **not a test** — it never runs
  under `go test` and the suite stays browser-free. It boots a scripted console
  in-process (temp db seeded with projects/tasks, sessions spawn `sh -i`),
  drives headless Chrome through the real flows (overview → naming view →
  spawn → tab switch), and saves a PNG per state to `tmp/webshots/`. Don't
  run it routinely after web UI changes — only when asked, or when a change
  is visually risky enough that seeing it rendered is the only way to check;
  a scenario whose DOM state never renders fails loudly, which is the sanity
  signal.
