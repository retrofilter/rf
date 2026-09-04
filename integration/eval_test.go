package integration

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func runRF(t *testing.T, dir string, extraEnv []string, args ...string) (string, string, error) {
	t.Helper()
	if resolved, err := filepath.EvalSymlinks(dir); err == nil {
		dir = resolved
	}
	cmd := exec.Command(rfBinary, args...)
	cmd.Dir = dir
	for _, kv := range os.Environ() {
		key, _, _ := strings.Cut(kv, "=")
		if isolatedEnv(key) || key == "CLAUDECODE" {
			continue
		}
		cmd.Env = append(cmd.Env, kv)
	}
	cmd.Env = append(cmd.Env, "HOME="+dir)
	cmd.Env = append(cmd.Env, extraEnv...)
	var stdout, stderr strings.Builder
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	return stdout.String(), stderr.String(), err
}

func TestEvalOneShot(t *testing.T) {
	home := t.TempDir()

	out, errOut, err := runRF(t, home, nil, "-e", `(task "write the launch post")`)
	if err != nil {
		t.Fatalf("task filing failed: %v\n%s", err, errOut)
	}
	id := strings.TrimSpace(out)
	if id == "" {
		t.Fatal("task should print its id")
	}

	out, _, err = runRF(t, home, nil, "-e", `(tasks)`)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "write the launch post") {
		t.Fatalf("tasks should list the filed task:\n%s", out)
	}

	if _, errOut, err = runRF(t, home, nil, "-e", `(task {:complete `+id+`})`); err != nil {
		t.Fatalf("task --complete failed: %v\n%s", err, errOut)
	}
	out, _, err = runRF(t, home, nil, "-e", `(tasks)`)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, "write the launch post") {
		t.Fatalf("completed task should leave the open listing:\n%s", out)
	}
}

func TestEvalAgentCallerGating(t *testing.T) {
	home := t.TempDir()
	agent := []string{"CLAUDECODE=1"}

	// Graph writes are deliberately ungated: /task must be frictionless.
	if _, errOut, err := runRF(t, home, agent, "-e", `(task "agent-filed")`); err != nil {
		t.Fatalf("agent task filing should be ungated: %v\n%s", err, errOut)
	}

	secret := filepath.Join(home, "secret.txt")
	if err := os.WriteFile(secret, []byte("hunter2"), 0600); err != nil {
		t.Fatal(err)
	}
	out, errOut, err := runRF(t, home, agent, "-e", `(cat "`+secret+`")`)
	if err == nil {
		t.Fatalf("gated read should fail closed for an agent caller:\n%s", out)
	}
	if strings.Contains(out, "hunter2") || !strings.Contains(errOut, "denied") {
		t.Fatalf("expected a denial, got stdout=%q stderr=%q", out, errOut)
	}

	// Without the marker the same read is user-typed code: ungated.
	out, errOut, err = runRF(t, home, nil, "-e", `(cat "`+secret+`")`)
	if err != nil {
		t.Fatalf("user read should be ungated: %v\n%s", err, errOut)
	}
	if !strings.Contains(out, "hunter2") {
		t.Fatalf("user read should print the file:\n%s", out)
	}
}

func TestEvalInteractivePrelude(t *testing.T) {
	home := t.TempDir()
	prelude := "(define (greet) \"hello from prelude\")\n" +
		"(define agent-allow-commands '(\"echo\"))\n"
	if err := os.WriteFile(filepath.Join(home, ".rf.scm"), []byte(prelude), 0644); err != nil {
		t.Fatal(err)
	}

	out, errOut, err := runRF(t, home, nil, "-ie", `(greet)`)
	if err != nil {
		t.Fatalf("-ie should see prelude definitions: %v\n%s", err, errOut)
	}
	if !strings.Contains(out, "hello from prelude") {
		t.Fatalf("expected the prelude function's value, got:\n%s", out)
	}

	if out, _, err = runRF(t, home, nil, "-e", `(greet)`); err == nil {
		t.Fatalf("plain -e must not load the prelude:\n%s", out)
	}

	agent := []string{"CLAUDECODE=1"}
	out, errOut, err = runRF(t, home, agent, "-ie", `(sh "echo hi")`)
	if err == nil {
		t.Fatalf("prelude allowlist must stay inert for agent callers:\n%s", out)
	}
	if !strings.Contains(errOut, "denied") {
		t.Fatalf("expected a denial, got stdout=%q stderr=%q", out, errOut)
	}
	out, errOut, err = runRF(t, home, agent, "-ie", `(greet)`)
	if err != nil {
		t.Fatalf("agent -ie should still see prelude definitions: %v\n%s", err, errOut)
	}
	if !strings.Contains(out, "hello from prelude") {
		t.Fatalf("expected the prelude function's value, got:\n%s", out)
	}

	// A broken prelude reports to stderr and never blocks the form.
	if err := os.WriteFile(filepath.Join(home, ".rf.scm"), []byte("(oops\n"), 0644); err != nil {
		t.Fatal(err)
	}
	out, errOut, err = runRF(t, home, nil, "-ie", `(+ 1 2)`)
	if err != nil {
		t.Fatalf("broken prelude must not block -ie: %v\n%s", err, errOut)
	}
	if !strings.Contains(out, "3") || !strings.Contains(errOut, ".rf.scm") {
		t.Fatalf("expected the result and a stderr report, got stdout=%q stderr=%q", out, errOut)
	}
}
