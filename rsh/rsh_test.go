package rsh

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func run(t *testing.T, s *Session, line string) (int, string, string) {
	t.Helper()
	var out, errb syncBuffer
	code := s.Run(context.Background(), line, strings.NewReader(""), &out, &errb)
	return code, out.String(), errb.String()
}

func chdirTemp(t *testing.T) string {
	t.Helper()
	orig, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(orig) })
	// macOS: TempDir is a /var symlink; Getwd reports the resolved path.
	resolved, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	return resolved
}

func TestExportPersists(t *testing.T) {
	t.Setenv("RSH_TEST_EXPORT", "") // registers cleanup
	os.Unsetenv("RSH_TEST_EXPORT")
	s := NewSession()
	if code, _, errb := run(t, s, "export RSH_TEST_EXPORT=sticks"); code != 0 {
		t.Fatalf("exit %d, stderr %q", code, errb)
	}
	if got := os.Getenv("RSH_TEST_EXPORT"); got != "sticks" {
		t.Fatalf("env not harvested: %q", got)
	}
	// And a later line's child sees it.
	_, out, _ := run(t, s, "printenv RSH_TEST_EXPORT")
	if strings.TrimSpace(out) != "sticks" {
		t.Fatalf("child env: %q", out)
	}
}

func TestBareAssignmentIsShellVarNotEnv(t *testing.T) {
	s := NewSession()
	run(t, s, "RSH_TEST_BARE=quiet")
	if got := os.Getenv("RSH_TEST_BARE"); got != "" {
		t.Fatalf("bare assignment leaked into env: %q", got)
	}
	// But it persists as a shell var across lines...
	_, out, _ := run(t, s, "echo $RSH_TEST_BARE")
	if strings.TrimSpace(out) != "quiet" {
		t.Fatalf("shell var lost: %q", out)
	}
	// ...and export later promotes it.
	t.Setenv("RSH_TEST_BARE", "")
	os.Unsetenv("RSH_TEST_BARE")
	run(t, s, "export RSH_TEST_BARE")
	if got := os.Getenv("RSH_TEST_BARE"); got != "quiet" {
		t.Fatalf("late export: %q", got)
	}
}

func TestUnsetHarvests(t *testing.T) {
	t.Setenv("RSH_TEST_UNSET", "here")
	s := NewSession()
	run(t, s, "unset RSH_TEST_UNSET")
	if _, found := os.LookupEnv("RSH_TEST_UNSET"); found {
		t.Fatal("unset not harvested")
	}
}

func TestPrefixAssignment(t *testing.T) {
	s := NewSession()
	_, out, _ := run(t, s, "RSH_TEST_PFX=once printenv RSH_TEST_PFX")
	if strings.TrimSpace(out) != "once" {
		t.Fatalf("prefix assignment: %q", out)
	}
	if got := os.Getenv("RSH_TEST_PFX"); got != "" {
		t.Fatalf("prefix assignment leaked: %q", got)
	}
}

func TestCdHarvests(t *testing.T) {
	dir := chdirTemp(t)
	sub := filepath.Join(dir, "build")
	if err := os.Mkdir(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	s := NewSession()
	if code, _, errb := run(t, s, "cd build && pwd"); code != 0 {
		t.Fatalf("exit %d, stderr %q", code, errb)
	}
	if cwd, _ := os.Getwd(); cwd != sub {
		t.Fatalf("cwd not harvested: %q want %q", cwd, sub)
	}
	// cd - returns via harvested OLDPWD.
	if code, _, errb := run(t, s, "cd -"); code != 0 {
		t.Fatalf("cd -: exit %d, stderr %q", code, errb)
	}
	if cwd, _ := os.Getwd(); cwd != dir {
		t.Fatalf("cd -: %q want %q", cwd, dir)
	}
}

func TestPipesRedirectsSubstitution(t *testing.T) {
	chdirTemp(t)
	s := NewSession()
	run(t, s, `printf 'b\na\n' | sort > out.txt`)
	data, err := os.ReadFile("out.txt")
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "a\nb\n" {
		t.Fatalf("pipeline+redirect: %q", data)
	}
	_, out, _ := run(t, s, `echo "got $(cat out.txt | wc -l | tr -d ' ') lines"`)
	if strings.TrimSpace(out) != "got 2 lines" {
		t.Fatalf("command substitution: %q", out)
	}
}

func TestFunctionPersists(t *testing.T) {
	s := NewSession()
	run(t, s, "greet() { echo hi $1; }")
	_, out, _ := run(t, s, "greet bob")
	if strings.TrimSpace(out) != "hi bob" {
		t.Fatalf("function did not persist: %q", out)
	}
}

func TestExitCodes(t *testing.T) {
	s := NewSession()
	if code, _, _ := run(t, s, "false"); code != 1 {
		t.Fatalf("false: %d", code)
	}
	if code, _, _ := run(t, s, "exit 7"); code != 7 {
		t.Fatalf("exit 7: %d", code)
	}
	if code, _, errb := run(t, s, "if then"); code != 2 || errb == "" {
		t.Fatalf("syntax error: %d %q", code, errb)
	}
	if code, _, _ := run(t, s, "definitely-not-a-command-xyz"); code != 127 {
		t.Fatalf("not found: %d", code)
	}
}

func TestSourceHarvests(t *testing.T) {
	dir := chdirTemp(t)
	script := filepath.Join(dir, "setup.sh")
	body := "export RSH_TEST_SRC=fromscript\nsetup_done() { echo done; }\n"
	if err := os.WriteFile(script, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("RSH_TEST_SRC", "")
	os.Unsetenv("RSH_TEST_SRC")
	s := NewSession()
	if code, _, errb := run(t, s, "source setup.sh"); code != 0 {
		t.Fatalf("source: exit %d, stderr %q", code, errb)
	}
	if got := os.Getenv("RSH_TEST_SRC"); got != "fromscript" {
		t.Fatalf("sourced export: %q", got)
	}
	_, out, _ := run(t, s, "setup_done")
	if strings.TrimSpace(out) != "done" {
		t.Fatalf("sourced function: %q", out)
	}
}

func TestVenvActivate(t *testing.T) {
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 not on PATH")
	}
	dir := chdirTemp(t)
	if out, err := exec.Command(python, "-m", "venv", "--without-pip", "venv").CombinedOutput(); err != nil {
		t.Skipf("venv creation failed: %v: %s", err, out)
	}
	t.Setenv("PATH", os.Getenv("PATH")) // restore after harvest mutates it
	t.Setenv("VIRTUAL_ENV", "")
	os.Unsetenv("VIRTUAL_ENV")

	s := NewSession()
	if code, _, errb := run(t, s, "source venv/bin/activate"); code != 0 {
		t.Fatalf("activate: exit %d, stderr %q", code, errb)
	}
	want := filepath.Join(dir, "venv")
	if got := os.Getenv("VIRTUAL_ENV"); got != want {
		t.Fatalf("VIRTUAL_ENV: %q want %q", got, want)
	}
	if !strings.HasPrefix(os.Getenv("PATH"), filepath.Join(want, "bin")) {
		t.Fatalf("PATH not prefixed: %q", os.Getenv("PATH"))
	}
	// A later line's child resolves python from the venv.
	_, out, _ := run(t, s, "command -v python3")
	if strings.TrimSpace(out) != filepath.Join(want, "bin", "python3") {
		t.Fatalf("python3 resolves to %q", out)
	}
	// deactivate is a function defined by the sourced script.
	if code, _, errb := run(t, s, "deactivate"); code != 0 {
		t.Fatalf("deactivate: exit %d, stderr %q", code, errb)
	}
	if got := os.Getenv("VIRTUAL_ENV"); got != "" {
		t.Fatalf("VIRTUAL_ENV survives deactivate: %q", got)
	}
	if strings.HasPrefix(os.Getenv("PATH"), filepath.Join(want, "bin")) {
		t.Fatal("PATH still prefixed after deactivate")
	}
}

func TestCancelKillsChildren(t *testing.T) {
	s := NewSession()
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(100 * time.Millisecond)
		cancel()
	}()
	var out, errb bytes.Buffer
	start := time.Now()
	code := s.Run(ctx, "sleep 30", strings.NewReader(""), &out, &errb)
	if elapsed := time.Since(start); elapsed > 10*time.Second {
		t.Fatalf("cancel took %v", elapsed)
	}
	if code == 0 {
		t.Fatalf("cancelled run reported success (stderr %q)", errb.String())
	}
}

func TestRunNoHarvest(t *testing.T) {
	t.Setenv("RSH_TEST_PLAIN", "")
	os.Unsetenv("RSH_TEST_PLAIN")
	var out, errb bytes.Buffer
	code, err := Run(context.Background(), "export RSH_TEST_PLAIN=leak; echo ran", strings.NewReader(""), &out, &errb)
	if err != nil || code != 0 {
		t.Fatalf("run: %d %v", code, err)
	}
	if got := os.Getenv("RSH_TEST_PLAIN"); got != "" {
		t.Fatalf("plain Run harvested env: %q", got)
	}
	if strings.TrimSpace(out.String()) != "ran" {
		t.Fatalf("stdout: %q", out.String())
	}
}

func TestFindings(t *testing.T) {
	s := NewSession()
	code, out, errb := run(t, s, "umask 077 && umask")
	t.Logf("umask: exit=%d stdout=%q stderr=%q", code, out, errb)

	start := time.Now()
	code, out, errb = run(t, s, "sleep 0.3 & echo bg-started")
	t.Logf("background: exit=%d stdout=%q stderr=%q elapsed=%v", code, out, errb, time.Since(start))
	if time.Since(start) > 250*time.Millisecond {
		t.Log("FINDING: interp waits for background jobs before returning")
	}
}
