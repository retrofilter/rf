package integration

import (
	"os"
	"path/filepath"
	"testing"
)

func TestExportSticksAcrossLines(t *testing.T) {
	t.Parallel()
	s := startShell(t, t.TempDir())
	s.sendLine("export RF_STATE_TEST=alive")
	s.sendLine("echo got-$RF_STATE_TEST")
	s.expect(`got-alive`)
}

func TestCdCompoundMovesShell(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "build"), 0o755); err != nil {
		t.Fatal(err)
	}
	s := startShell(t, dir)
	s.sendLine("cd build && echo entered")
	s.expect(`entered`)
	s.clear()
	s.sendLine(`test "$(basename $(pwd))" = build && echo in-child-dir`)
	s.expect(`in-child-dir`)
	s.clear()
	s.sendLine(`cd - > /dev/null && test "$(pwd)" = "$HOME" && echo back-home`)
	s.expect(`back-home`)
}

func TestShellVarAndFunctionPersist(t *testing.T) {
	t.Parallel()
	s := startShell(t, t.TempDir())
	// A bare assignment is a shell var: visible next line, not exported.
	s.sendLine("RF_BARE=quiet")
	s.sendLine("echo var-is-$RF_BARE")
	s.expect(`var-is-quiet`)
	s.clear()
	s.sendLine("greet() { echo hello-$1; }")
	s.sendLine("greet world")
	s.expect(`hello-world`)
}

func TestSourcedScriptSticks(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	script := "export RF_FROM_SOURCE=yes\nrf_sourced_fn() { echo fn-runs; }\n"
	if err := os.WriteFile(filepath.Join(dir, "setup.sh"), []byte(script), 0o644); err != nil {
		t.Fatal(err)
	}
	s := startShell(t, dir)
	s.sendLine("source setup.sh")
	s.sendLine("echo src-$RF_FROM_SOURCE")
	s.expect(`src-yes`)
	s.clear()
	s.sendLine("rf_sourced_fn")
	s.expect(`fn-runs`)
}

func TestBackgroundJobNotice(t *testing.T) {
	t.Parallel()
	s := startShell(t, t.TempDir())
	s.sendLine("sleep 4 &")
	s.expect(`\[bg \d+\]  sleep 4`)
	s.sendLine("jobs")
	s.expect(`\d+\s+running\s+sleep 4`)
	// A backgrounded builtin's output survives the prompt repaint.
	s.clear()
	s.sendLine(`echo bg-echo-marker &`)
	s.expect(`bg-echo-marker`)
}
