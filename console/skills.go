package console

import (
	"os"
	"path/filepath"
)

const taskSkill = `---
name: task
description: File or complete a durable task in the shared rf task graph — local, cross-session, shared by every agent and the user's shell. Use when work should be tracked beyond this session (a follow-up discovered while coding, something the user wants done later) or when finishing a previously filed task.
---

# rf tasks

Tasks live in retrofilter's local graph (~/.rf/main.db), shared by every
agent session and the user's shell, and they survive this conversation.
Use them for anything worth picking up later. The in-session todo list is
for this conversation's checklist; rf tasks are the durable ones.

Invoked as /task with text: file that text as a task, then tell the user
the id.

File a task for the current project (prints the task's id):

    rf -e '(task "fix the flaky PTY resize test")'

Complete a task by id:

    rf -e '(task {:complete 42})'

Delete a task by id (permanent — for a task filed by mistake; completing
is the normal way to retire one):

    rf -e '(task {:delete 42})'

List open tasks here (all of them outside a project) / everywhere /
one project's / unblocked only:

    rf -e '(tasks)'
    rf -e '(tasks {:all})'
    rf -e '(tasks {:project "name"})'
    rf -e '(tasks {:ready})'

More: file under another project with (task "..." {:project "name"});
declare that the new task blocks task 17 with (task "..." {:blocks 17}).

Quoting: prefer a single-quoted shell string as above. If the text itself
contains an apostrophe, switch to a double-quoted shell string and escape
the inner double quotes: rf -e "(task \"don't forget the retry\")".

Habits: file follow-ups you notice but don't act on, complete tasks as
you finish the work they describe, and check (tasks) when starting work
in a project.

## Notes and references

Notes are longer markdown documents in the same graph, named by slug
and filed under a project like tasks. Read, list, and write them:

    rf -e '(note "shopping-list")'
    rf -e '(notes)'
    rf -e '(note "shopping-list" {:text "# Shopping list\n\n- eggs"})'

The :text may be a whole document with YAML front matter (name:,
project:, and any extra keys, which become node properties). Refer to
anything in the graph with one spelling — task:42, note:slug,
project:name, or #42 — and write [[task:42]] or [[note:slug]] inside a
note or task text to link it; rf -e '(node "task:42")' fetches any of
them with its edges (node is Scheme-only: it shadows the node binary).
`

// ClaudeSkillsDir is where rf installs Claude Code skills.
func ClaudeSkillsDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".claude", "skills"), nil
}

// InstallClaudeTaskSkill writes the /task skill, returning its path.
func InstallClaudeTaskSkill() (string, error) {
	dir, err := ClaudeSkillsDir()
	if err != nil {
		return "", err
	}
	path := filepath.Join(dir, "task", "SKILL.md")
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return "", err
	}
	return path, os.WriteFile(path, []byte(taskSkill), 0644)
}

// ClaudeTaskSkillInstalled reports whether the /task skill file is
// present (any content — user edits count as installed).
func ClaudeTaskSkillInstalled() bool {
	dir, err := ClaudeSkillsDir()
	if err != nil {
		return false
	}
	_, err = os.Stat(filepath.Join(dir, "task", "SKILL.md"))
	return err == nil
}
