package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/retrofilter/rf/core"
	"github.com/retrofilter/rf/eval"
)

func TestSessionStartContext(t *testing.T) {
	home := t.TempDir()
	if resolved, err := filepath.EvalSymlinks(home); err == nil {
		home = resolved
	}
	t.Setenv("HOME", home)
	t.Chdir(home)

	proj := filepath.Join(home, "proj")
	if err := os.MkdirAll(proj, 0755); err != nil {
		t.Fatal(err)
	}

	// Seed through the same builtins the shell uses.
	db, err := openMainDB()
	if err != nil {
		t.Fatal(err)
	}
	ev := eval.NewEvaluatorWithEnvironment(db, core.NewGraphStore(db))
	for _, expr := range []string{
		`(register-project "~/proj")`,
		`(task "fix the flaky test" {:project "proj"})`,
	} {
		forms, err := eval.ParseAll(expr)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := ev.EvalAll(forms, ev.GlobalEnv()); err != nil {
			t.Fatalf("%s: %v", expr, err)
		}
	}
	db.Close()

	var buf strings.Builder
	sessionStartContext(&buf, proj)
	out := buf.String()
	for _, want := range []string{
		`Open tasks for project "proj"`,
		"fix the flaky test",
		`rf -e '(task "text")'`,
		"{:complete ID}",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("session-start context missing %q:\n%s", want, out)
		}
	}

	// Outside any project: no tasks, but the cheatsheet still teaches.
	buf.Reset()
	sessionStartContext(&buf, home)
	out = buf.String()
	if !strings.Contains(out, "No open tasks") {
		t.Fatalf("expected empty listing outside the project:\n%s", out)
	}
	if !strings.Contains(out, `rf -e '(task "text")'`) {
		t.Fatalf("cheatsheet should always print:\n%s", out)
	}
}
