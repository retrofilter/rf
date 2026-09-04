package llm

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jmoiron/sqlx"
	"github.com/retrofilter/rf/core"
	"github.com/retrofilter/rf/models"
	_ "modernc.org/sqlite"
)

func writeAgents(t *testing.T, dir, content string) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, agentsFileName)
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestAgentsContextEmpty(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	if got := agentsContext(t.TempDir()); got != "" {
		t.Errorf("expected empty context, got %q", got)
	}
}

func TestAgentsContextProjectWalk(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	hub := filepath.Join(home, "src", "foo")
	sub := filepath.Join(hub, "main", "sub")
	if err := os.MkdirAll(filepath.Join(hub, ".bare"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeAgents(t, filepath.Join(home, "src"), "above the hub")
	writeAgents(t, hub, "hub instructions")
	writeAgents(t, filepath.Join(hub, "main"), "tree instructions")
	writeAgents(t, sub, "sub instructions")

	got := agentsContext(sub)
	if strings.Contains(got, "above the hub") {
		t.Error("context includes AGENTS.md from above the project hub")
	}
	hubAt := strings.Index(got, "hub instructions")
	treeAt := strings.Index(got, "tree instructions")
	subAt := strings.Index(got, "sub instructions")
	if hubAt < 0 || treeAt < 0 || subAt < 0 {
		t.Fatalf("missing sections in context:\n%s", got)
	}
	if !(hubAt < treeAt && treeAt < subAt) {
		t.Errorf("expected outermost-first order (hub < tree < sub), got %d %d %d", hubAt, treeAt, subAt)
	}
}

func TestAgentsContextGitRoot(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	parent := t.TempDir()
	repo := filepath.Join(parent, "repo")
	inner := filepath.Join(repo, "pkg")
	if err := os.MkdirAll(filepath.Join(repo, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeAgents(t, parent, "outside the repo")
	writeAgents(t, repo, "repo instructions")
	writeAgents(t, inner, "pkg instructions")

	got := agentsContext(inner)
	if strings.Contains(got, "outside the repo") {
		t.Error("context includes AGENTS.md from above the git root")
	}
	if !strings.Contains(got, "repo instructions") || !strings.Contains(got, "pkg instructions") {
		t.Errorf("missing repo/pkg sections:\n%s", got)
	}
}

func TestAgentsContextNoBoundary(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	// Outside any project or git checkout only the directory itself counts.
	parent := t.TempDir()
	dir := filepath.Join(parent, "notes")
	writeAgents(t, parent, "parent instructions")
	writeAgents(t, dir, "local instructions")

	got := agentsContext(dir)
	if strings.Contains(got, "parent instructions") {
		t.Error("context walked above cwd with no project or git boundary")
	}
	if !strings.Contains(got, "local instructions") {
		t.Errorf("missing cwd section:\n%s", got)
	}
}

func TestAgentsContextGlobalFile(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	if err := os.WriteFile(filepath.Join(home, globalAgentsName), []byte("global instructions"), 0o644); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	writeAgents(t, dir, "local instructions")

	got := agentsContext(dir)
	globalAt := strings.Index(got, "global instructions")
	localAt := strings.Index(got, "local instructions")
	if globalAt < 0 || localAt < 0 {
		t.Fatalf("missing global/local sections:\n%s", got)
	}
	if globalAt > localAt {
		t.Error("global file should precede local AGENTS.md")
	}
	if !strings.Contains(got, "~/"+globalAgentsName+" (global)") {
		t.Errorf("global section not labeled:\n%s", got)
	}
}

func TestAgentsContextSkipsBlankAndTruncates(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	blank := t.TempDir()
	writeAgents(t, blank, "  \n\t\n")
	if got := agentsContext(blank); got != "" {
		t.Errorf("blank AGENTS.md should yield no context, got %q", got)
	}

	big := t.TempDir()
	writeAgents(t, big, strings.Repeat("x", agentsFileCap+100))
	got := agentsContext(big)
	if !strings.Contains(got, "[truncated]") {
		t.Error("oversized AGENTS.md should carry a truncation notice")
	}
	if len(got) > agentsFileCap+1024 {
		t.Errorf("context not capped: %d bytes", len(got))
	}
}

func TestMessageIncludesAgentsContext(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	dir := t.TempDir()
	writeAgents(t, dir, "always answer in haiku")
	t.Chdir(dir)

	db, err := sqlx.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	gs := core.NewGraphStore(db)

	fake := &fakeModel{responses: []*Response{textResponse("ok")}}
	chat := newChat(gs, Config{Provider: models.ProviderOllama, Model: "fake"}, fake)
	if _, err := chat.Message(context.Background(), "hi", nil, ""); err != nil {
		t.Fatal(err)
	}
	if len(fake.calls) == 0 {
		t.Fatal("model was never called")
	}
	if !strings.Contains(fake.calls[0].System, "always answer in haiku") {
		t.Error("system prompt missing AGENTS.md content")
	}
	if !strings.Contains(fake.calls[0].System, agentsHeader) {
		t.Error("system prompt missing AGENTS.md header")
	}
}
