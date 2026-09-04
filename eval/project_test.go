package eval

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseWorktrees(t *testing.T) {
	out := `worktree /home/u/src/foo/.bare
bare

worktree /home/u/src/foo/main
HEAD 5796c14e1168b2dfd3706689fda0ea7cf990012e
branch refs/heads/main

worktree /home/u/src/foo/wip
HEAD 5796c14e1168b2dfd3706689fda0ea7cf990012e
detached`

	trees := parseWorktrees(out)
	if len(trees) != 3 {
		t.Fatalf("expected 3 worktrees, got %d: %v", len(trees), trees)
	}
	if !trees[0].bare || trees[0].path != "/home/u/src/foo/.bare" {
		t.Errorf("bare entry wrong: %+v", trees[0])
	}
	if trees[1].branch != "main" || trees[1].head != "5796c14e1168b2dfd3706689fda0ea7cf990012e" {
		t.Errorf("main entry wrong: %+v", trees[1])
	}
	if trees[2].branch != "(detached)" {
		t.Errorf("detached entry wrong: %+v", trees[2])
	}
}

func withProjectHome(t *testing.T, fn func(home string, ev *Evaluator, env *Environment)) {
	t.Helper()
	home := t.TempDir()
	// Resolve symlinks (macOS /var -> /private/var) so os.Getwd matches HOME.
	if resolved, err := filepath.EvalSymlinks(home); err == nil {
		home = resolved
	}
	t.Setenv("HOME", home)
	t.Chdir(home)
	ev := NewEvaluator()
	fn(home, ev, ev.globalEnv)
}

func TestFindProject(t *testing.T) {
	withProjectHome(t, func(home string, ev *Evaluator, env *Environment) {
		deep := filepath.Join(home, "src", "foo", "main", "docs")
		if err := os.MkdirAll(deep, 0755); err != nil {
			t.Fatal(err)
		}
		name, hub, ok := FindProject(deep)
		if !ok || name != "foo" || hub != filepath.Join(home, "src", "foo") {
			t.Errorf("FindProject under ~/src: got %q %q %v", name, hub, ok)
		}

		// A .bare hub outside ~/src is a project too
		other := filepath.Join(home, "elsewhere", "bar")
		if err := os.MkdirAll(filepath.Join(other, ".bare"), 0755); err != nil {
			t.Fatal(err)
		}
		name, hub, ok = FindProject(filepath.Join(other, "main"))
		if !ok || name != "bar" || hub != other {
			t.Errorf("FindProject via .bare: got %q %q %v", name, hub, ok)
		}

		if _, _, ok := FindProject(home); ok {
			t.Error("FindProject should not match outside any project")
		}
	})
}

func TestProjectRootBinding(t *testing.T) {
	withProjectHome(t, func(home string, ev *Evaluator, env *Environment) {
		if got := ProjectRoot(); got != filepath.Join(home, "src") {
			t.Fatalf("default ProjectRoot: got %q", got)
		}

		if _, err := evalExpr(`(define project-root "~/code")`, ev, env); err != nil {
			t.Fatal(err)
		}
		if got := ProjectRoot(); got != filepath.Join(home, "code") {
			t.Errorf("overridden ProjectRoot: got %q", got)
		}
		deep := filepath.Join(home, "code", "foo", "main")
		if err := os.MkdirAll(deep, 0755); err != nil {
			t.Fatal(err)
		}
		name, hub, ok := FindProject(deep)
		if !ok || name != "foo" || hub != filepath.Join(home, "code", "foo") {
			t.Errorf("FindProject under project-root: got %q %q %v", name, hub, ok)
		}
		if _, _, ok := FindProject(filepath.Join(home, "src", "foo", "main")); ok {
			t.Error("~/src should not match while project-root points elsewhere")
		}

		// A bogus binding falls back rather than breaking the prompt.
		if _, err := evalExpr(`(define project-root 42)`, ev, env); err != nil {
			t.Fatal(err)
		}
		if got := ProjectRoot(); got != filepath.Join(home, "src") {
			t.Errorf("non-string project-root should fall back: got %q", got)
		}
	})
}

func TestProjectLifecycle(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	withProjectHome(t, func(home string, ev *Evaluator, env *Environment) {
		mustEval := func(expr string) Value {
			t.Helper()
			v, err := evalExpr(expr, ev, env)
			if err != nil {
				t.Fatalf("%s: %v", expr, err)
			}
			return v
		}
		cwd := func() string {
			t.Helper()
			d, err := os.Getwd()
			if err != nil {
				t.Fatal(err)
			}
			return d
		}

		mainPath := filepath.Join(home, "src", "demo", "main")
		if got := mustEval(`(create-project "demo")`); got != String(mainPath) {
			t.Fatalf("create-project returned %v, want %q", got, mainPath)
		}
		if cwd() != mainPath {
			t.Fatalf("create-project should cd into %s, cwd is %s", mainPath, cwd())
		}
		if _, err := os.Stat(filepath.Join(home, "src", "demo", ".bare")); err != nil {
			t.Fatal("bare repo missing:", err)
		}
		if _, err := evalExpr(`(create-project "demo")`, ev, env); err == nil {
			t.Fatal("re-creating an existing project should error")
		}

		featPath := filepath.Join(home, "src", "demo", "feat")
		mustEval(`(create-tree "feat")`)
		if cwd() != featPath {
			t.Fatalf("create-tree should cd into %s, cwd is %s", featPath, cwd())
		}

		trees := mustEval(`(trees)`).([]Value)
		if len(trees) != 2 {
			t.Fatalf("expected 2 trees, got %v", trees)
		}

		// tree switches to an existing worktree...
		mustEval(`(tree "main")`)
		if cwd() != mainPath {
			t.Fatalf("tree should cd into %s, cwd is %s", mainPath, cwd())
		}
		// ...and creates missing ones
		mustEval(`(tree "wip")`)
		if cwd() != filepath.Join(home, "src", "demo", "wip") {
			t.Fatalf("tree should create and cd, cwd is %s", cwd())
		}

		if projects := mustEval(`(projects)`).([]Value); len(projects) != 0 {
			t.Fatalf("projects without a store should list nothing, got %v", projects)
		}

		// delete-tree consults the approver and stops on denial
		ev.SetApprover(func(string) bool { return false })
		if _, err := evalExpr(`(delete-tree "wip")`, ev, env); err == nil {
			t.Fatal("delete-tree should be blocked by a denying approver")
		}
		ev.SetApprover(nil)

		if got := mustEval(`(delete-tree "wip")`); got != true {
			t.Fatalf("delete-tree returned %v", got)
		}
		if len(mustEval(`(trees)`).([]Value)) != 2 {
			t.Fatal("expected 2 trees after delete")
		}
		if _, err := evalExpr(`(delete-tree "wip")`, ev, env); err == nil {
			t.Fatal("deleting a missing tree should error")
		}
	})
}

func TestProjectNames(t *testing.T) {
	withProjectHome(t, func(home string, ev *Evaluator, env *Environment) {
		if err := os.MkdirAll(filepath.Join(home, "src", "foo"), 0755); err != nil {
			t.Fatal(err)
		}
		if got := ProjectNames(); len(got) != 0 {
			t.Fatalf("unregistered directories should complete nothing, got %v", got)
		}
	})
}

func TestTreeNames(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	withProjectHome(t, func(home string, ev *Evaluator, env *Environment) {
		if got := TreeNames(); got != nil {
			t.Fatalf("outside a repo should complete nothing, got %v", got)
		}
		repo := filepath.Join(home, "myrepo")
		for _, args := range [][]string{
			{"init", "-q", "-b", "main", repo},
			{"-C", repo, "-c", "user.email=t@t", "-c", "user.name=t", "commit", "--allow-empty", "-q", "-m", "init"},
		} {
			if out, err := exec.Command("git", args...).CombinedOutput(); err != nil {
				t.Fatalf("git %v: %v\n%s", args, err, out)
			}
		}
		t.Chdir(repo)
		if _, err := evalExpr(`(create-tree "feat")`, ev, ev.globalEnv); err != nil {
			t.Fatal(err)
		}
		names := TreeNames()
		want := map[string]bool{}
		for _, n := range names {
			want[n] = true
		}
		if !want["feat"] || !want["main"] || !want["myrepo"] {
			t.Fatalf("TreeNames = %v, want feat, main, and myrepo included", names)
		}
	})
}

func TestCreateTreeFromNormalClone(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	withProjectHome(t, func(home string, ev *Evaluator, env *Environment) {
		repo := filepath.Join(home, "myrepo")
		for _, args := range [][]string{
			{"init", "-q", "-b", "main", repo},
			{"-C", repo, "-c", "user.email=t@t", "-c", "user.name=t", "commit", "--allow-empty", "-q", "-m", "init"},
		} {
			if out, err := exec.Command("git", args...).CombinedOutput(); err != nil {
				t.Fatalf("git %v: %v\n%s", args, err, out)
			}
		}
		t.Chdir(repo)

		v, err := evalExpr(`(create-tree "feat")`, ev, ev.globalEnv)
		if err != nil {
			t.Fatal(err)
		}
		want := filepath.Join(repo, ".trees", "feat")
		if v != String(want) {
			t.Fatalf("create-tree returned %v, want %q", v, want)
		}
		exclude, err := os.ReadFile(filepath.Join(repo, ".git", "info", "exclude"))
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(exclude), ".trees/") {
			t.Fatalf("first tree should append .trees/ to info/exclude, got:\n%s", exclude)
		}
		// Idempotent: a second tree must not duplicate the line.
		if _, err := evalExpr(`(create-tree "feat2")`, ev, ev.globalEnv); err != nil {
			t.Fatal(err)
		}
		exclude, _ = os.ReadFile(filepath.Join(repo, ".git", "info", "exclude"))
		if strings.Count(string(exclude), ".trees/") != 1 {
			t.Fatalf("info/exclude should hold one .trees/ line, got:\n%s", exclude)
		}
		// git status must stay clean of the nested trees.
		out, err := exec.Command("git", "-C", repo, "status", "--porcelain").CombinedOutput()
		if err != nil {
			t.Fatal(err)
		}
		if strings.TrimSpace(string(out)) != "" {
			t.Fatalf("nested trees should be excluded from git status, got:\n%s", out)
		}
	})
}
