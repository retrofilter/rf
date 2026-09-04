package integration

import (
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"
)

func (s *shell) firstChild() string {
	out, err := exec.Command("pgrep", "-P", strconv.Itoa(s.cmd.Process.Pid)).Output()
	if err != nil {
		return ""
	}
	f := strings.Fields(string(out))
	if len(f) == 0 {
		return ""
	}
	return f[0]
}

func childStopped(pid string, wait time.Duration) bool {
	deadline := time.Now().Add(wait)
	for time.Now().Before(deadline) {
		out, err := exec.Command("ps", "-o", "stat=", "-p", pid).Output()
		if err == nil && strings.HasPrefix(strings.TrimSpace(string(out)), "T") {
			return true
		}
		time.Sleep(20 * time.Millisecond)
	}
	return false
}

func (s *shell) waitForForegroundChild() string {
	s.t.Helper()
	deadline := time.Now().Add(expectTimeout)
	for time.Now().Before(deadline) {
		if child := s.firstChild(); child != "" {
			ps, err := exec.Command("ps", "-o", "pgid=,tpgid=", "-p", child).Output()
			if err == nil {
				if f := strings.Fields(string(ps)); len(f) == 2 && f[0] == f[1] {
					return child
				}
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	s.t.Fatalf("timed out waiting for a foreground child of the shell")
	return ""
}

func TestJobControlForeground(t *testing.T) {
	t.Parallel()
	s := startShell(t, t.TempDir())
	s.sendLine(`!sh -c 'if test "$(ps -o tpgid= -p $$)" -eq "$(ps -o pgid= -p $$)"; then r=OK; else r=BAD; fi; echo "FGRESULT-$r"'`)
	s.expect(`FGRESULT-OK`)
	s.expectNot(`FGRESULT-BAD`)
}

func TestJobControlSuspendResume(t *testing.T) {
	t.Parallel()
	s := startShell(t, t.TempDir())

	s.sendLine("!sleep 300")
	child := s.waitForForegroundChild()

	stopped := false
	for attempt := 0; attempt < 3 && !stopped; attempt++ {
		s.send("\x1a") // ^Z -> SIGTSTP to the foreground group (sleep)
		stopped = childStopped(child, 2*time.Second)
	}
	if !stopped {
		_ = exec.Command("kill", "-TSTP", "-"+child).Run()
		if childStopped(child, 2*time.Second) {
			t.Fatalf("^Z never stopped the child but a direct SIGTSTP did — the tty signal path is broken")
		}
		_ = exec.Command("kill", "-STOP", "-"+child).Run()
		if childStopped(child, 2*time.Second) {
			_ = exec.Command("kill", "-KILL", "-"+child).Run()
			t.Skipf("kernel discarded SIGTSTP to a valid foreground group " +
				"(macOS orphaned-pgrp accounting bug under concurrent go-test load); " +
				"SIGSTOP still worked — environment, not rf")
		}
		t.Fatalf("child ignored ^Z, SIGTSTP and SIGSTOP — unknown state")
	}
	s.expect(`stopped`)

	s.sendLine("jobs")
	s.expect(`1\s+stopped\s+sleep 300`)

	s.clear()
	s.sendLine("fg")
	s.expect(`(?m)^sleep 300`) // fg echoes the resumed job's line
	s.waitForForegroundChild() // fg must hand the terminal back before ^C
	s.send("\x03")             // ^C -> SIGINT to sleep's group, not rf's

	// Back at a working prompt, no jobs left.
	s.sendLine("(+ 20 22)")
	s.expect(`42`)
	s.sendLine("fg")
	s.expect(`no stopped jobs`)
}

func TestJobCommandWords(t *testing.T) {
	t.Parallel()
	s := startShell(t, t.TempDir())

	s.sendLine("job echo job-done-marker")
	s.expect(`command`) // the handle renders as a table

	s.sendLine("job -w 1")
	s.expect(`job-done-marker`)

	s.sendLine("jobs")
	s.expect(`1\s+done\s+0\s+echo job-done-marker`)

	s.sendLine("job --in 100ms echo -n scheduled-marker")
	s.expect(`command`)
	s.sendLine("job -w 2")
	s.expect(`scheduled-marker`)

	s.sendLine(`job printf '[%s]' what "time is" it`)
	s.expect(`command`)
	s.sendLine("job -w 3")
	s.expect(`\[what\]\[time is\]\[it\]`)

	s.sendLine("job -o 1")
	s.expect(`job-done-marker`)
	s.sendLine("job -o 1 | tail -n 1")
	s.expect(`job-done-marker`)
}
