package integration

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestOnboardingWizardEscapeWritesPreludeOnce(t *testing.T) {
	dir := t.TempDir()
	s := launchShell(t, dir, "RF_SKIP_ONBOARDING=")
	s.expect(`model provider`)
	s.waitReady()
	s.send("\x1b")
	s.expect(`created ~/.rf.scm`)
	s.expect(`\$ `)
	s.sendLine("(+ 1 2)")
	s.expect(`3`)
	s.close()

	prelude, err := os.ReadFile(filepath.Join(dir, ".rf.scm"))
	if err != nil {
		t.Fatal(err)
	}
	if want := ";; start rf config"; !strings.Contains(string(prelude), want) {
		t.Fatalf("prelude lacks the wizard marker: %q", prelude)
	}

	again := startShell(t, dir, "RF_SKIP_ONBOARDING=")
	again.expectNot(`model provider`)
}

func TestOnboardingWizardChoicesApply(t *testing.T) {
	dir := t.TempDir()
	s := launchShell(t, dir, "RF_SKIP_ONBOARDING=")
	s.expect(`model provider`)
	s.waitReady()
	s.send("\x1b[B\x1b[B\r") // skip
	s.expect(`install the claude hooks`)
	s.waitReady()
	s.send("\x1b[B\r") // no
	s.expect(`agent-allow-working-dir :read`)
	s.expect(`\$ `)
	s.sendLine("(list agent-allow-working-dir (length agent-allow-commands))")
	s.expect(`\(read 3\)`)

	prelude, _ := os.ReadFile(filepath.Join(dir, ".rf.scm"))
	if !strings.Contains(string(prelude), `(define agent-allow-commands '("git status" "git diff" "git log"))`) {
		t.Fatalf("unexpected prelude: %q", prelude)
	}
	if _, err := os.Stat(filepath.Join(dir, ".claude", "settings.json")); err == nil {
		t.Fatal("hooks were installed despite 'no'")
	}
}

func TestHelpManual(t *testing.T) {
	s := startShell(t, t.TempDir(), "PAGER=cat")
	s.sendLine("help chunk")
	s.expect(`CHUNK\(1\)`)
	s.expect(`SYNOPSIS`)
	s.sendLine("help pipelines")
	s.expect(`PIPELINES\(7\)`)
	s.sendLine("help")
	s.expect(`RF\(1\)`)
	s.expect(`TOPICS`)
	s.sendLine("help --list | grep processes")
	s.expect(`background jobs`)
	s.sendLine("help nope")
	s.expect(`topics: modes`)
	s.sendLine("chunk -h")
	s.expect(`usage:  chunk`)
}
