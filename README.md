# retrofilter - a programmable, persistent shell

retrofilter is a shell replacement for the 2020s. It is based on the following core ideas:

- The shell is the right place for managing state. retrofilter is heavily persistent with
  an SQlite database providing task, history, and session management.
- It is easy to modify, and extend the shell. An *r7rs-small compatible*, sandboxed,
  **Scheme** implementation is used as an extension language allowing for granular controls.
- Structured **pipes** allow for composition and chaining of commands. You can even pipe the
  output of a command into an LLM or reranker.
- Agent agnostic. retrofilter provides its own agent harness particularly useful for
  customization and workflows but it also integrates well into Claude Code.
- retrofilter builds as a single binary and provides a **web console** so you
  can run and schedule sessions from a browser.

## Demo

Firstly, there is an sh compatibility layer so most common commands work:

```sh
(~) $ ls
agent       cmd         core        go.mod      integration logger      models      rsh
benchmarks  console     docs        go.sum      LICENSE     main.go     README.md
CLAUDE.md   contrib     eval        HANDBOOK.md llm         Makefile    rf

(~) $ cat HANDBOOK.md | grep "^###" | wc -l # count sections in handbook
      68

(~) $ NAME=rf && echo "Hi there, $NAME"
Hi there, rf
```

However, pipes can now return structured tables so you can do things like this:

```sh
dir ~/Downloads | where size -gt 10MB # find large files in downloads, dir is the tabular version of ls
history git -n 100 | count-by dir # search command history for "git" and count results by directory
```

Additionally retrofilter provides inbuilt LLM primitives:

```sh
uname -a | llm "what computer do I have" # ask an LLM in one call to identify your system
dir ~/Downloads | where size -gt 10MB | take 4 | llm-map "is this important to keep" # triage files to delete
tasks | rerank "most important" | take 1 # get top task to do next
```

You can even embed data on the fly, thanks to low-resource static embeddings via [go-potion](https://github.com/trengrj/go-potion).

```sh
(~) $ dir | hybrid "documentation" | take 2 # find the top 2 files about documentation
name         type  size  modified          mode        score
docs         dir   224B  2026-08-26 21:51  drwxr-xr-x  1
HANDBOOK.md  file  20K   2026-08-26 21:52  -rw-r--r--  0.984
```

For the extension language, retrofilter will detect Scheme code and evaluate it:

```sh
(~) $ (+ 1 2)
3

(~) $ (define (cube x) (* x x x))
cube

(~) $ (cube 42)
74088

(~) $ (pipe (dir) (similar "logging" {:limit 1}))  # pipeline code is just syntax sugar
name    type  size  modified          mode        score
logger  dir   96B   2026-07-31 22:01  drwxr-xr-x  0.159
```

If you don't know Scheme, retrofilter can generate it for you — just hit Ctrl+Space
to switch to `agent` mode and type in plain text:

```
(~) ⏺ write a function to get my ip address using ipecho.net
⏺ Scheme

  (define (my-ip)
    (string-trim (get :body (fetch "https://ipecho.net/plain" :full))))
  ⎿  my-ip

⏺ Scheme
  (my-ip)
approve? fetch "https://ipecho.net/plain" [y/N] y
  ⎿  "101.102.103.104"

⏺ Defined and working.  (my-ip)  fetches  https://ipecho.net/plain  and returns your public IP as a string —
  "101.102.103.104"  right now.

↑14k ↓189 $0.02
```

Functions defined by the agent can then be used in command mode (hit Ctrl+Space to go back):

```
(~) $ my-ip
"101.102.103.104"
```

You can even ask the agent to persist a function to your config (in `~/.rf.scm`) so that you can use it again.

```
(~) $ (persist my-ip)
⏺ Agent Persist the following Scheme definition into ~/.rf.scm (the rf prelude…
⏺ I will read the file first, then make the appropriate edit.

⏺ Read ~/.rf.scm
approve? read "~/.rf.scm" [y/N] y
  ⎿  ;; PATH — add-path is idempotent, so no containment guard needed
     … +20 lines

⏺ The file has no existing  my-ip  definition, so I'll append it at the end.

⏺ Edit ~/.rf.scm
approve? edit "~/.rf.scm" (1 edit) [y/N] y
  ⎿  edited ~/.rf.scm (1 replacement)
```

In this way you can build up capabilities over time and automate your workflows. For instance a `progress` function could be built as follows:

```scheme
(define (progress)
    (agent "Check git commits and summarize progress for the past week"))
```

You can then call it via `(progress)` in any mode or just as `progress` in command mode (as custom definitions are also exposed to the shell).

```sh
(~) $ progress
⏺ Agent Check git commits and summarize progress for last week
```

While you technically could do all your coding in `agent` mode it is expected most users will continue using their
existing preferred agent (if only to save money with their plan's credits).

Retrofilter helps in the following ways in particular if you are using Claude Code:

- `configure` optionally installs Claude Code hooks so that `messages` provides a fully searchable history of your conversations.
- Busy/waiting status and session and weekly credits monitoring for `rf console`.
- Sessions respawn automatically when `rf console` restarts, even across a reboot.
- Skills to create tasks and evaluate Scheme code in retrofilter.

## State and persistence

retrofilter persists all state in a single SQLite database, by default located at `~/.rf/main.db`. The `history` command
allows for searching through this history and `Ctrl+R` will search recent items.

`~/.rf.scm` allows for customization and registering new functions at startup. `~/.rf.env` can be used for environment
variables.

The `configure` command will set up the initial model provider and is triggered on first startup.

## Documentation

The manual lives in the shell: `help` opens RF(1) — a guide intro plus an index of every page — `help <command>`
is that command's man page, `help <topic>` a guide chapter (approval, pipes, tasks, …), and `help --list` gives
the pages as rows you can pipe. Every command also answers `-h` with short usage. The same registry renders as
[HANDBOOK.md](HANDBOOK.md) if you'd rather read one file.

## Installation

retrofilter builds a single static binary (pure Go, no cgo) called `rf`. With Go installed run:

```sh
go install github.com/retrofilter/rf@latest
rf              # first run opens the setup wizard (provider, model, what the agent may do unasked)
rf console      # starts the web console on localhost — access via the tokened URL it prints
```

Without Go, download the binary for your platform from the [releases page](https://github.com/retrofilter/rf/releases) (darwin/linux, amd64/arm64) and put `rf` on your PATH.

## Building from source

```sh
git clone https://github.com/retrofilter/rf && cd rf
make build      # builds ./rf
make install    # copies it into place (brew prefix on macOS; sudo needed for /usr/local/bin on Linux)
```
