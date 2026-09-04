package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestTerminalTitle(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	titled := func(want string) bool {
		got := terminalTitle()
		return strings.HasPrefix(got, "\x1b]2;"+want+"\a") &&
			strings.Contains(got, "\x1b]7;file://")
	}
	if !titled("rf") {
		t.Errorf("outside project: %q, want rf title + cwd report", terminalTitle())
	}

	// A .bare directory marks a project hub; worktrees sit beside it.
	hub := filepath.Join(dir, "proj")
	if err := os.MkdirAll(filepath.Join(hub, ".bare"), 0o755); err != nil {
		t.Fatal(err)
	}
	tree := filepath.Join(hub, "main", "cmd")
	if err := os.MkdirAll(tree, 0o755); err != nil {
		t.Fatal(err)
	}

	t.Chdir(hub)
	if !titled("proj") {
		t.Errorf("at hub: %q, want proj title", terminalTitle())
	}
	t.Chdir(tree)
	if !titled("proj@main") {
		t.Errorf("in worktree subdir: %q, want proj@main title", terminalTitle())
	}

	// A legacy git checkout outside any project shows repo@branch.
	repo := filepath.Join(dir, "legacy")
	if err := os.MkdirAll(filepath.Join(repo, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, ".git", "HEAD"), []byte("ref: refs/heads/main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Chdir(repo)
	if !titled("legacy@main") {
		t.Errorf("in legacy repo: %q, want legacy@main title", terminalTitle())
	}
}

func TestSetTerminalTitleDedup(t *testing.T) {
	resetTerminalTitle()
	defer resetTerminalTitle()

	setTerminalTitle("\x1b]2;rf\a")
	if lastTitle != "\x1b]2;rf\a" {
		t.Fatalf("lastTitle = %q", lastTitle)
	}
	resetTerminalTitle()
	if lastTitle != "" {
		t.Fatalf("reset left lastTitle = %q", lastTitle)
	}
}
