# Notes and references

The design record for `note`/`notes` (`eval/note.go`) and the reference
grammar (`eval/ref.go`). Tasks (`eval/task.go`) are the precedent: graph
nodes in the default graph, filed under a project by a `for` edge, read
and written by the user and the agent alike through one builtin.

## Notes

**A note is a named markdown document.** Where a task is one line with a
status, a note is the longer thing — a design, a reading list, a plan —
so its editing surface is `$EDITOR`, not the command line. Node type
`note`, properties `name` (the slug), `text` (the body), `session`
(provenance, like tasks), plus whatever front matter the user adds.

**Names are slugs.** `note "Shopping List"` makes
`shopping-list`. Slugs are stable, typeable, and unambiguous
inside `[[...]]`; a display title lives in the body's first heading
(what `notes` shows as `title`). Unique per graph.

**The document is the interface.** What the editor opens, what `note
NAME` prints, and what `--text` accepts are the same thing: a YAML front
matter block and a body.

    ---
    name: shopping-list
    project: rf
    tags: [errands, weekly]
    ---

    # Shopping list
    …

Front matter is authoritative on save: `name:` renames, `project:`
re-files (empty or absent unfiles — the key's presence decides, so a
bare-body `--text` on an existing note keeps its project and its extra
keys), any other scalar or list becomes a node property (`notes | where
tags …` works, and the graph view shows them). `type`, `text`, and
`session` are reserved. Printing regenerates the front matter from the
node, so an edit round-trips.

**Verbs mirror `project`, with one difference.** Bare `note NAME` shows
(as a line stream, so `> file.md` and pipelines work) — and, like
`task`, the first touch creates: a name with no note behind it opens
`$EDITOR` seeded with the front matter and the cwd's project, so
`note shopping-list` is the whole workflow. `-e` edits an existing one
in `$EDITOR`. Both are user-only, since the agent has no terminal; it
writes `--text` (a missing name errors with that hint). `-t` sets, `-p` re-files, `-d`
deletes. `notes` lists the way `tasks` does: scoped to the cwd's
project, `--all`/`-p` to widen, most recently edited first.

**No rendering in the shell.** A note prints as its markdown source. The
console renders markdown where it shows text; the terminal stays plain,
like `cat`.

## References

**One spelling names any node.** `task:42`, `note:slug`, `project:name`,
`#42` (any node by id). Typed kinds check the type — `task:5` errors if
node 5 is a note — and `#` is the escape for everything else. `node`
accepts a reference where it accepted an id, and every node row carries
its canonical `ref` (`RefString`), so results say how to name themselves.

**Links are text first, edges second.** Inside a note body or a task's
text, `[[ref]]` is a link (`[[ref|alias]]` too; a bare `[[slug]]` means a
note, the common case). Saving the text derives the node's outgoing
`links` edges from it — stale ones dropped, new ones added, the edge
carrying the ref it came from — so the text is the source of truth and
the graph view draws what the prose says. Dangling links are allowed:
a `[[future-note]]` stays text, is reported as `unresolved` in the save
result, and becomes an edge the moment that note is created (creation
and rename re-sync every note and task that mentions the slug).

**Why wikilinks, not ids in prose.** Prose written by people and models
should read as prose; `[[shopping-list]]` does, `#4321` does not. Ids remain
the universal fallback for nodes with no name.

Not done, deliberately: markdown rendering in the terminal, notes on the
console overview (the graph view shows them), backlink listing as a verb
(`neighbors note:x` is one hop away once it accepts refs), and re-writing
`[[old-slug]]` in other notes on rename (edges survive by id; the text
goes stale).
