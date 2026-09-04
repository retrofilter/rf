package integration

import (
	"testing"
	"time"
)

func TestCtrlCAtPromptKeepsShell(t *testing.T) {
	t.Parallel()
	s := startShell(t, t.TempDir())

	s.send("ls doomed-line")
	s.expect(`doomed-line`)
	s.clear()
	s.send("\x03")
	s.expect(`\$ `) // a fresh prompt: the line was abandoned, not the shell
	s.sendLine("(+ 41 1)")
	s.expect(`42`)
	s.expectNot(`doomed-line:`)
}

func TestCtrlCInterruptsSchemeEval(t *testing.T) {
	t.Parallel()
	s := startShell(t, t.TempDir())

	s.sendLine("(sleep 30)")
	time.Sleep(500 * time.Millisecond)
	s.clear()
	s.send("\x03")
	s.expect(`interrupted`)
	s.expect(`\$ `)
	s.sendLine("(+ 1 2)")
	s.expect(`3`)
}

func TestCtrlCKillsExternalCommandNotShell(t *testing.T) {
	t.Parallel()
	s := startShell(t, t.TempDir())

	s.sendLine("!sleep 30")
	time.Sleep(500 * time.Millisecond)
	s.clear()
	s.send("\x03") // SIGINT to the foreground group: sleep dies, rf survives
	s.expect(`\$ `)
	s.sendLine("(+ 20 3)")
	s.expect(`23`)
}
