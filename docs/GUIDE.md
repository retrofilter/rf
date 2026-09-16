# rf guide

rf is a shell that remembers. History, tasks, sessions and notes live in
one SQLite database; pipes carry tables as well as text; Scheme is the
extension language, typed at the same prompt as unix commands. A language
model shares that language and that database as a second mode, so
anything it defines you can call, and anything you define it can use.

`help` opens this manual: the overview page lists every topic and every
command, `help <topic>` (say, `help pipelines`) opens a topic page, and
`help <command>` is that command's man page. `<command> -h` stays the
one-glance usage line.

## Modes

One prompt, two modes, toggled with **Ctrl+Space** (or Shift+Tab):

- **command mode** (`$` prompt) — a shell. `ls`, `git status`, pipelines,
  redirection, `cd`, `export`, and everything else you'd type in bash run
  the way bash runs them. A line that starts with `(` is Scheme instead,
  evaluated in the session's shared environment.
- **agent mode** (`⏺` prompt) — a chat with the model. It has one main
  tool, `scheme`, which runs in the same interpreter you type into, plus
  three narrow file tools (`read`, `edit`, `write`). Whatever it defines,
  you can call afterwards — and vice versa.

Scheme works in both modes. `(define (cube x) (* x x x))` at the command
prompt persists for the session; the model can call `(cube 3)` on its next
turn. **Ctrl+K** is the middle ground: it sends the current line to the
model and replaces it with an editable proposed command — nothing runs
until you press enter.

Other keys: **Ctrl+R** searches history, **→** accepts the ghost
suggestion (fish-style, drawn from history in this directory first),
**Ctrl+G** cancels a running chat turn, **Ctrl+C** interrupts the running
form or child, **Ctrl+Z** parks a foreground job (`fg` resumes, `jobs`
lists). `(exit)` or Ctrl+D quits.

## Command mode

Every command-mode line becomes exactly one Scheme form — you can always
see it. `history -d . -n 10` is `(history {:dir "." :limit 10})`; `help
history` shows both spellings side by side. The rules:

- **Flags become a dict.** Each command declares its options once; the
  long flag name is the dict key. `--limit 10` ⇢ `:limit 10`; a boolean
  flag ⇢ `:flag #t`. `--` ends flags; `-h`/`--help` works everywhere.
- **Words that name system programs stay system programs.** `ls`, `cat`,
  `grep`, `rm`, `wc`, `head`, `diff`, `sh` … always run the real binary
  in command mode. The Scheme versions (which return rows and streams)
  are reachable in parens: `(ls)` gives rows, `ls` prints columns. `dir`
  is the rows spelling of `ls` that needs no parens.
- **Unknown flags and wrong arity error** with a hint, rather than
  quietly running something else. `!` at the start of a line forces the
  whole line to the shell executor.
- **Globs expand** under commands that declare them (`dir *.go`).

Anything that isn't a command word, a user-defined function, or Scheme
goes to **rsh**, rf's own shell interpreter (no `/bin/sh` child). Because
it runs in-process, state persists across lines: `export FOO=1` sticks,
`cd build && make` moves the prompt, `source .venv/bin/activate` works and
so does `deactivate` later.

## Pipelines

`|` between command words builds a lazy Scheme pipeline; `|` into a unix
program is a real pipe. Both mix freely on one line:

```sh
dir | where size -gt 50000 | sort-by size    # rows through Scheme stages
history -n 20 > recent.tsv                   # > on a Scheme stage writes the rows as TSV
git log | similar "auth"                     # git runs in sh, similar ranks its lines
history | grep make                          # rows cross to unix grep as TSV
dir | json | jq -r .name                     # json keeps structure across the crossing
cat notes.md | tr a-z A-Z | take 5           # external cat and tr, lazy take at the end
fetch "https://example.com" | grep title     # HTTP bodies are line streams
make test 2>&1 | agent why did this fail     # pipe anything into the model
```

The row verbs are the vocabulary: `where` (field comparisons or a
predicate — chain with and/or using test(1)'s comparisons, size
suffixes scale: `where type = file and size -gt 100MB`), `sort-by`, `group-by`, `count-by`, `pick` (choose columns),
`take`, `json` and `text` (serialize out), `parse-json` and `from-csv`
(parse in), `diff` (rows of changes, or `:text` for a unified diff).
Rankers score lines or rows against a query: `bm25` (keyword), `similar`
(local embeddings — no API call), `hybrid` (both, fused), and `rerank`
(the model, listwise). `llm-map` and `classify` run a prompt over every
item in batches: `history | take 50 | classify "bug-fix or feature?" |
count-by label`. `llm` is one plain completion — `llm write a haiku`
standalone, or `git diff | llm summarize` with the piped value as
context — and `embed "text"` prints the local embedding vector.

Streams are lazy and single-use: `(take 2 (sh "yes"))` kills `yes` after
two lines, and a stream you hold in a variable can be read once. Results
echoed at the prompt are materialized, so `*1` (the last result) is safe.

## Approval

Set up the model with `configure` — the setup wizard: Anthropic, a local
Ollama server, or any Anthropic-compatible endpoint by name, URL, and
key. `usage` shows the session's tokens, cost, and context size. API
keys live in `~/.rf.env` (as `RF_<PROVIDER>_API_KEY`), never in the
database.

Everything the model does that touches your machine asks first. Reads
(`cat`, `ls`, `grep`, file ports, the `read` tool) and writes (`rm`,
`mv`, `sh`, `fetch`, `write-file`, the `edit`/`write` tools) each hit a
y/N prompt when the *model* calls them; your own typed code never does.
Some commands (`configure`, `resume`, `compact`, `clear`, `exec`, `exit`,
`delete-graph`, and the project words — `project`, `projects`,
`register-project`, `unregister-project`) are user-only and refuse the
model outright: the model works *inside* projects, it never shapes them.

Two prelude bindings widen that, deliberately, in data you can read.
The first run writes both with defaults — reads under the chat's
starting directory, plus `git status`/`git diff`/`git log` — and
`configure --allowed-commands` edits the command list:

```scheme
;; the model may read files under the directory a chat starts in
(define agent-allow-working-dir :read)      ; or :write
;; commands the model runs without asking, matched by leading words
(define agent-allow-commands '("git status" "git diff" "git log"))
```

`"go test"` covers `go test ./... -run X`; a command containing `|`,
`;`, `$(...)`, or redirection always prompts, so an allowed prefix can't
smuggle a second command. Only you can bind these — the model's `define`
of either name errors. `(with-approval ...)` batches several gated
operations behind one prompt.

The chat itself: `agent "instruction"` runs a sub-agent turn from either
mode (and from inside your own functions — a `(progress)` that asks the
model to summarize the week's commits is three lines). Sessions persist;
`resume` picks a past one, `compact` summarizes older turns when the
context gets long (it also happens automatically near the limit), `clear`
starts fresh. Extended thinking is off unless `default-model` says
`:thinking #t`.

## Prelude

`~/.rf.scm` is evaluated at startup in the shared environment. It is the
config file, and it's just Scheme:

```scheme
(add-path "~/go/bin" "~/.local/bin")   ; idempotent PATH additions
(set-env "EDITOR" "vim")
(alias e "nvim")
(define default-graph "notes")          ; where remember/recall/task go
(define graph-embeddings #f)            ; drop graph search to full-text only
(define (progress)
  (agent "Summarize this repo's git commits from the past week"))
```

Bindings the shell reads: `default-model` — which model you talk to,
one dict (or a bare model-id string); `configure` writes it into a
marked block here, and the environment carries only the API key:

```scheme
(define default-model
  {:provider "anthropic"        ; or "ollama", or any compatible endpoint's name
   :id "claude-sonnet-4-6"      ; the model id
   ;:base-url "https://..."     ; where the Messages API lives (known providers default)
   :api-key (env "RF_ANTHROPIC_API_KEY") ; the credential — this env var is the default
   :thinking #t                 ; or a token budget; off when absent
   :effort "medium"             ; output effort (API default when absent)
   :max-tokens 64000            ; per-response output cap
   :context 200000              ; context window, drives auto-compaction
   :caching #t})                ; prompt-cache markers (defaults on for anthropic)
```

`:api-key` is the credential itself, so a second dict can read a
different variable; when absent, the provider name derives the env var
(`deepseek` reads `RF_DEEPSEEK_API_KEY` from `~/.rf.env`), so any
Anthropic-compatible endpoint is a first-class provider. The `RF_`
prefix keeps rf's keys out of the vendor's own tools: a bare
`ANTHROPIC_API_KEY` in `~/.rf.env` would reach every `claude` started
from an rf shell. The provider itself is config, never key presence —
a bare `claude-*` id means Anthropic, anything else the Ollama
default. Other bindings: `default-graph`,
`project-root`, `embedding-model`, `graph-embeddings`,
`agent-allow-working-dir`, `agent-allow-commands`, and the hook
functions `before-tool-hook`, `after-tool-hook`, `after-turn-hook`.
Errors in the prelude print but never stop the shell.

When the model defines something useful in a chat, `persist name` asks it
to write that definition into the prelude (you approve the edit);
`inspect name` shows any definition's source. `aliases` lists aliases,
`(builtins)` everything callable, Scheme library included.
Functions you define are command words too: `(define (progress) …)` then
`progress` at the prompt.

## Memory and tasks

Everything lives in `~/.rf/main.db` (SQLite; `help backups` covers
copying it safely). The database holds property graphs, and the rest is
built on them:

- **Notes** — `remember some text` saves a note, `recall query` searches
  them (full-text plus embeddings; `graph-embeddings` set to `#f` opts
  down to full-text only).
- **Graph** — `nodes [query]`, `edges`, `graphs`, `graph`, `paths`,
  `create-graph`, `delete-node` … the raw surface. `neighbors id` walks
  one hop out (rows with a direction column), `top-incoming` /
  `top-outgoing` rank nodes by edge degree. `with-graph` scopes a form
  to a named graph; `default-graph` picks the one the convenience verbs
  use.
- **Projects** — `register-project [path]` makes a directory a project
  node; `project name` alone files a project with no directory behind
  it (a bucket for tasks and notes — nothing is created on disk), or
  cds into an existing one; `project --clone URL name` and `project
  --init name` build a bare-repo worktree hub under `project-root`.
  `project -e [name]` opens the project's markdown notes in `$EDITOR`
  (`--text` sets them without one); the console's overview shows them
  under the project's header. `projects` lists them, bare `project`
  describes the current one. Every project word is user-only: the model
  can file and read tasks under a project, but never create, enter, or
  edit one. `tree` and `trees` manage git worktrees.
- **Tasks** — `task some text` files a task under the current project,
  `tasks` lists open ones (`--ready` for unblocked, `--all` for every
  project), `task -c id` closes, `task -d id` deletes one
  outright. Tasks are graph nodes with `for` and
  `blocks` edges, so the model can read and file them too — and so can a
  Claude Code session, via `rf -e '(task "…")'`.
- **History** — `history [pattern]` searches every line ever typed, by
  directory, mode, or project. **Messages** — `messages [pattern]` searches
  chat transcripts, including Claude Code's when the hooks are installed.

`rf -e '(form)'` evaluates one form against the same database from
outside the shell — the surface scripts and other agents use. `rf
script.scm args` runs a file with no database or prelude.

## Backups

Everything durable — graphs, notes, projects, tasks, history, chat
transcripts — is the one SQLite file `~/.rf/main.db`. Back that file up
and you've backed up rf.

The database runs in WAL mode, so while any rf process is open, recent
writes live beside it in `main.db-wal`. Two safe ways to copy:

- **Online** (fine while rf is running) — let SQLite make the copy:

  ```sh
  sqlite3 ~/.rf/main.db "VACUUM INTO '/path/to/rf-backup.db'"
  ```

  This produces one consistent, self-contained file (and compacts it).

- **Cold copy** — with every rf shell, `rf console`, and Claude Code
  session exited, `cp ~/.rf/main.db …` is enough; if `main.db-wal` or
  `main.db-shm` exist, copy them alongside.

Skip `~/.rf/cache/` — it's derived data (quantized embeddings), rebuilt
on demand. Configuration deliberately lives outside the database: the
prelude `~/.rf.scm` and the credentials file `~/.rf.env` are the other
two files a full restore wants — and `~/.rf.env` holds API keys, so
store its backup accordingly.

To restore, stop rf and put the file back at `~/.rf/main.db` (removing
any stale `-wal`/`-shm` beside it). A database written by a newer rf
refuses to open under an older binary — the schema check fails closed —
so restore with a matching or newer rf.

## Processes

`sh "cmd"` runs a command and returns its output as a line stream (`:full`
for `{:stdout :stderr :exit}`, `{:timeout N}` to bound it, and a stream or
list of lines as one more argument feeds the command's stdin — the three
combine in any order). `fetch url` is the same shape for HTTP: a line
stream of the body, or `:full` for `{:status :body :headers}` where
`:body` is one string (`parse-json (lines body)` parses it). For anything
long: `job make test` returns a handle immediately; `job -s 1` polls,
`job -w 1` joins, `job -o 1` peeks at the captured output so far as
lines without blocking (`--stderr` for the other buffer; `job -o 1 |
tail -20` and `job -o 1 | grep FAIL` are real coreutils over the
snapshot), `job -k 1` stops. A job can be scheduled — `job --in
4h make backup` starts one later, `job --every day make backup` reruns
it until killed (spans: `30s`, `10m`, `4h`, `1d`, `1w`, or
`hour`/`day`/`week`) — with the schedule living and dying with
this shell process, like `&` survivors: for cron-durable schedules, use
cron. `jobs` lists them alongside Ctrl+Z-stopped and `&` background
children; `fg` resumes the most recently stopped one. `exec` replaces
the shell.

## Console

`rf console` serves a localhost dashboard: a fleet of rf sessions in the
browser (xterm.js), each a real PTY, with a rail showing what every
session is doing, the projects and open tasks from the graph as a landing
page (pick a task and a session opens in that project with `claude
'<task>'` typed in), and a force-layout view of the whole graph. Every
request is authenticated: the auto-opened URL signs its browser in with a
single-use nonce, and any other browser signs in at `/login` with the
token `rf token` prints. Signed-in browsers hold an expiring session (14
days idle, 30 days flat), and the token itself rotates weekly — hooks
and shell builtins read it fresh from `~/.rf/webtoken` per request, so
rotation only ever asks a browser to sign in again. `sudo make
install-service` runs it as a systemd unit on Linux. From inside a shell,
`sessions` lists the fleet, `session` opens one, `rename-session`
names the current one.

`session 'claude /review'` opens a session with that command line
typed into its fresh shell, named `review` — the same slug the
overview derives for a task row, with a leading `claude` or `agent`
dropped since every session runs one — unless `--name fix-review` says
otherwise; a plain shell has nothing to slug, so `session -n scratch`
names it. A session can also be scheduled: `session -n review 'claude
/review' --at 09:00 --every day` files a schedule the console fires —
`--in 4h` or `--at 09:00` sets the first run (the next 09:00, or an
absolute `2026-03-01 09:00`), `--every day` (or any `job`-style span)
repeats it, `--cancel` (with `--name`) drops it. The
schedule is a `schedule` node in the graph, not
a cron entry or a shell timer: it needs no console at filing time and
survives console restarts, and `sessions` (and the console's rail,
as a SCHEDULED row counting down) lists it with its next run. Each firing
spawns a fresh session — one by that name still open from the last run
keeps it, and the new run takes the next free number, `review-2` —
and a run is just a session that stays up in the fleet, not a job with
captured output. A console
that was down past a fire time fires it once on return. Filing from
outside the shell, cron or an agent, is `rf -e '(session "claude
/review" {:name "review" :at "09:00" :every "day"})'`.

Reaching the console from another machine: it binds 127.0.0.1 by
default, so the simplest routes are an SSH tunnel or a tailnet. For a
real hostname with HTTPS, put a TLS-terminating reverse proxy in front —
the console does no TLS of its own, by design (certificates, renewal,
and redirects are a solved problem one proxy over). With Caddy on the
same machine the whole config is:

    console.example.com {
        reverse_proxy 127.0.0.1:7433
    }

Nothing else is needed — websockets proxy through, redirects are
relative, and the session cookie turns on its Secure flag when the
proxy reports HTTPS (`X-Forwarded-Proto`). Sign in at `/login` with the
token from `rf token`. Only if the proxy runs elsewhere does the bind
need widening: `rf console --bind 0.0.0.0` serves every interface —
cleartext HTTP, so keep it behind the proxy or a private network.
Prefer 0.0.0.0 over a single external address: local hooks and shell
builtins dial 127.0.0.1, which a narrower bind would exclude.

## Claude Code

`rf hook --install` (or the setup wizard's last screen) writes hooks into
`~/.claude/settings.json`. Afterwards every Claude Code session on the
machine: syncs its transcript into `~/.rf/main.db` after each turn
(Claude Code deletes transcripts after ~30 days; this mirror outlives
them — `messages` searches it), sees the current project's open tasks as
session context, gets a `/task` skill for filing and closing tasks, and
reports its status to the console. Bare `rf hook` shows what's installed.
Nothing here needs the console running, and nothing leaves the machine.
