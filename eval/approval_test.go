package eval

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReadBuiltinsGated(t *testing.T) {
	withTempDir(t, func(dir string, ev *Evaluator, env *Environment) {
		if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("alpha\nbeta\n"), 0644); err != nil {
			t.Fatal(err)
		}

		var asked []string
		ev.SetApprover(func(action string) bool {
			asked = append(asked, action)
			return true
		})
		if _, err := evalSeq(t, ev, `(cat "a.txt")`, `(ls ".")`, `(stat "a.txt")`, `(wc "a.txt")`, `(grep "alpha" "a.txt")`, `(glob "*.txt")`); err != nil {
			t.Fatalf("gated reads failed: %v", err)
		}
		want := []string{`cat "a.txt"`, `ls "."`, `stat "a.txt"`, `wc "a.txt"`, `grep "alpha" "a.txt"`, `glob "*.txt"`}
		if len(asked) != len(want) {
			t.Fatalf("approver saw %v, want %v", asked, want)
		}
		for i, w := range want {
			if asked[i] != w {
				t.Errorf("action %d: got %q, want %q", i, asked[i], w)
			}
		}

		// Denial blocks the read.
		ev.SetApprover(func(action string) bool { return false })
		if _, err := evalSeq(t, ev, `(cat "a.txt")`); err == nil || !strings.Contains(err.Error(), "denied") {
			t.Errorf("denied cat should error, got %v", err)
		}
		if _, err := evalSeq(t, ev, `(ls ".")`); err == nil || !strings.Contains(err.Error(), "denied") {
			t.Errorf("denied ls should error, got %v", err)
		}

		// No approver installed (user-typed code): never gated.
		ev.SetApprover(nil)
		if _, err := evalSeq(t, ev, `(cat "a.txt")`, `(ls ".")`); err != nil {
			t.Errorf("ungated reads failed: %v", err)
		}
	})
}

func TestWorkingDirBindingUserOnly(t *testing.T) {
	ev := NewEvaluator()

	ev.SetCaller(CallerAssistant)
	if _, err := evalSeq(t, ev, `(define agent-allow-working-dir :write)`); err == nil || !strings.Contains(err.Error(), "user") {
		t.Errorf("assistant define agent-allow-working-dir should error, got %v", err)
	}

	ev.SetCaller(CallerUser)
	if _, err := evalSeq(t, ev, `(define agent-allow-working-dir :read)`); err != nil {
		t.Fatalf("user define agent-allow-working-dir failed: %v", err)
	}
	ev.SetCaller(CallerAssistant)
	if _, err := evalSeq(t, ev, `(set! agent-allow-working-dir :write)`); err == nil {
		t.Error("assistant set! agent-allow-working-dir should error")
	}
	if _, err := evalSeq(t, ev, `(for-each (lambda (x) (set! agent-allow-working-dir :write)) '(1))`); err == nil {
		t.Error("assistant set! agent-allow-working-dir through for-each should error")
	}
	if v, err := ev.globalEnv.Lookup("agent-allow-working-dir"); err != nil || v != Keyword("read") {
		t.Errorf("agent-allow-working-dir should still be :read, got %v err=%v", v, err)
	}

	if _, err := evalSeq(t, ev,
		`(define-library (m) (export agent-allow-working-dir) (begin (define agent-allow-working-dir :write)))`,
		`(import (m))`); err == nil {
		if v, _ := ev.globalEnv.Lookup("agent-allow-working-dir"); v == Keyword("write") {
			t.Error("assistant import must not rebind agent-allow-working-dir")
		}
	}

	// The user remains free to change it.
	ev.SetCaller(CallerUser)
	if _, err := evalSeq(t, ev, `(set! agent-allow-working-dir :write)`); err != nil {
		t.Errorf("user set! agent-allow-working-dir failed: %v", err)
	}
}

func TestEnvMutatorsGated(t *testing.T) {
	withTempDir(t, func(dir string, ev *Evaluator, env *Environment) {
		defer os.Unsetenv("RF_TEST_GATED")

		var asked []string
		ev.SetApprover(func(action string) bool {
			asked = append(asked, action)
			return true
		})
		// add-path then remove-path so PATH round-trips unchanged.
		if _, err := evalSeq(t, ev, `(set-env "RF_TEST_GATED" "1")`, `(add-path ".")`, `(remove-path ".")`, `(cd ".")`); err != nil {
			t.Fatalf("approved mutators failed: %v", err)
		}
		if len(asked) != 4 {
			t.Fatalf("approver saw %v, want 4 prompts", asked)
		}

		// Denial blocks each mutation.
		ev.SetApprover(func(string) bool { return false })
		for _, expr := range []string{`(set-env "RF_TEST_GATED" "2")`, `(add-path "/no-such")`, `(remove-path "/no-such")`, `(cd "..")`} {
			if _, err := evalSeq(t, ev, expr); err == nil || !strings.Contains(err.Error(), "denied") {
				t.Errorf("%s should be denied, got %v", expr, err)
			}
		}
		if os.Getenv("RF_TEST_GATED") != "1" {
			t.Error("denied set-env still mutated the environment")
		}
		if cwd, _ := os.Getwd(); cwd != dir {
			t.Errorf("denied cd still moved: %s", cwd)
		}

		asked = nil
		ev.SetApprover(func(action string) bool {
			asked = append(asked, action)
			return true
		})
		ev.AllowDir(dir, false)
		if _, err := evalSeq(t, ev, `(cd "`+dir+`")`); err != nil {
			t.Fatal(err)
		}
		if len(asked) != 0 {
			t.Errorf("granted cd should not prompt, approver saw %v", asked)
		}
		if _, err := evalSeq(t, ev, `(set-env "RF_TEST_GATED" "3")`); err != nil {
			t.Fatal(err)
		}
		if len(asked) != 1 {
			t.Errorf("set-env must prompt despite the grant, approver saw %v", asked)
		}
	})
}

func TestCredentialPathsNeverGranted(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	if err := os.MkdirAll(filepath.Join(home, ".rf"), 0o755); err != nil {
		t.Fatal(err)
	}
	envFile := filepath.Join(home, ".rf.env")
	tokenFile := filepath.Join(home, ".rf", "webtoken")
	otherFile := filepath.Join(home, "notes.txt")
	for _, f := range []string{envFile, tokenFile, otherFile} {
		if err := os.WriteFile(f, []byte("x\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	ev := NewEvaluator()
	ev.AllowDir(home, true)
	if ev.PathAllowed(envFile, false) || ev.PathAllowed(envFile, true) {
		t.Error("grant must not cover ~/.rf.env")
	}
	if ev.PathAllowed(tokenFile, false) || ev.PathAllowed(tokenFile, true) {
		t.Error("grant must not cover ~/.rf/webtoken")
	}
	if !ev.PathAllowed(otherFile, false) {
		t.Error("grant should cover ordinary files under it")
	}

	var asked []string
	ev.SetApprover(func(action string) bool {
		asked = append(asked, action)
		return true
	})
	if _, err := evalSeq(t, ev, `(cat "`+otherFile+`")`); err != nil {
		t.Fatal(err)
	}
	if len(asked) != 0 {
		t.Fatalf("granted read should not prompt, approver saw %v", asked)
	}
	if _, err := evalSeq(t, ev, `(cat "~/.rf.env")`); err != nil {
		t.Fatal(err)
	}
	if len(asked) != 1 {
		t.Fatalf("credential read must prompt, approver saw %v", asked)
	}
}

func TestAllowDir(t *testing.T) {
	withTempDir(t, func(dir string, ev *Evaluator, env *Environment) {
		if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("hi\n"), 0644); err != nil {
			t.Fatal(err)
		}
		outside := t.TempDir()
		outFile := filepath.Join(outside, "out.txt")
		if err := os.WriteFile(outFile, []byte("far\n"), 0644); err != nil {
			t.Fatal(err)
		}

		var asked []string
		ev.SetApprover(func(action string) bool {
			asked = append(asked, action)
			return true
		})

		// Read grant: reads inside run free, writes and outside reads prompt.
		ev.AllowDir(dir, false)
		if _, err := evalSeq(t, ev, `(cat "a.txt")`, `(ls ".")`, `(stat "a.txt")`); err != nil {
			t.Fatalf("granted reads failed: %v", err)
		}
		if len(asked) != 0 {
			t.Fatalf("granted reads should not prompt, approver saw %v", asked)
		}
		if _, err := evalSeq(t, ev, `(cat "`+outFile+`")`); err != nil {
			t.Fatal(err)
		}
		if _, err := evalSeq(t, ev, `(write-file "b.txt" "x")`); err != nil {
			t.Fatal(err)
		}
		if len(asked) != 2 {
			t.Fatalf("outside read and inside write should prompt, approver saw %v", asked)
		}

		// PathAllowed mirrors the gate's view for callers outside eval.
		if !ev.PathAllowed(filepath.Join(dir, "a.txt"), false) {
			t.Error("PathAllowed should cover reads under the grant")
		}
		if ev.PathAllowed(filepath.Join(dir, "a.txt"), true) {
			t.Error("PathAllowed must not cover writes under a read grant")
		}
		if ev.PathAllowed(outFile, false) {
			t.Error("PathAllowed must not cover paths outside the grant")
		}

		// First grant wins: a second AllowDir must not move the boundary.
		ev.AllowDir(outside, true)
		if ev.PathAllowed(outFile, false) {
			t.Error("a second AllowDir must not extend the grant")
		}

		// Cleared and re-granted with write: writes inside run free too.
		ev.AllowDir("", false)
		ev.AllowDir(dir, true)
		asked = nil
		if _, err := evalSeq(t, ev, `(write-file "c.txt" "x")`, `(rm "c.txt")`, `(mkdir "sub")`); err != nil {
			t.Fatalf("granted writes failed: %v", err)
		}
		if len(asked) != 0 {
			t.Fatalf("granted writes should not prompt, approver saw %v", asked)
		}

		// A relative path escaping the grant still prompts.
		asked = nil
		if _, err := evalSeq(t, ev, `(cat "../`+filepath.Base(outside)+`/out.txt")`); err != nil {
			// The prompt fired (recorded) whether or not the read then worked.
			_ = err
		}
		if len(asked) != 1 {
			t.Fatalf("dot-dot escape should prompt, approver saw %v", asked)
		}
	})
}

func TestCommandAllowlist(t *testing.T) {
	withTempDir(t, func(dir string, ev *Evaluator, env *Environment) {
		if _, err := evalSeq(t, ev, `(define agent-allow-commands '("echo" "go test"))`); err != nil {
			t.Fatal(err)
		}

		var asked []string
		ev.SetApprover(func(action string) bool {
			asked = append(asked, action)
			return true
		})

		if _, err := evalSeq(t, ev, `(sh "echo hi there")`, `(job {:wait (job "echo bg")})`); err != nil {
			t.Fatalf("allowlisted commands failed: %v", err)
		}
		if len(asked) != 0 {
			t.Fatalf("allowlisted commands should not prompt, approver saw %v", asked)
		}

		if _, err := evalSeq(t, ev, `(sh "true")`); err != nil {
			t.Fatal(err)
		}
		if len(asked) != 1 {
			t.Fatalf("uncovered command must prompt, approver saw %v", asked)
		}

		asked = nil
		for _, expr := range []string{
			`(sh "echo hi; true")`,
			`(sh "echo $(true)")`,
			`(sh "echo hi | true")`,
			`(sh "echo hi > f.txt")`,
		} {
			if _, err := evalSeq(t, ev, expr); err != nil {
				t.Fatalf("%s: %v", expr, err)
			}
		}
		if len(asked) != 4 {
			t.Fatalf("metacharacter commands must prompt, approver saw %v", asked)
		}

		asked = nil
		if _, err := evalSeq(t, ev, `(let ((agent-allow-commands '("true"))) (sh "true"))`); err != nil {
			t.Fatal(err)
		}
		if len(asked) != 1 {
			t.Fatalf("let-shadowed allowlist must not grant, approver saw %v", asked)
		}

		// The binding is user-only to bind, like agent-allow-working-dir.
		ev.SetCaller(CallerAssistant)
		defer ev.SetCaller(CallerUser)
		if _, err := evalSeq(t, ev, `(set! agent-allow-commands '("rm -rf"))`); err == nil || !strings.Contains(err.Error(), "user") {
			t.Errorf("assistant set! agent-allow-commands should error, got %v", err)
		}
		if _, err := evalSeq(t, ev, `(define agent-allow-commands '("rm -rf"))`); err == nil {
			t.Error("assistant define agent-allow-commands should error")
		}
	})
}
