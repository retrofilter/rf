package rsh

import (
	"context"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

func jobSession(onStop func(int)) *Session {
	s := NewSession()
	s.Jobs = &JobControl{TTY: -1, OnStop: onStop}
	return s
}

func TestJobRunsNormally(t *testing.T) {
	s := jobSession(func(int) { t.Error("unexpected park") })
	code, out, errb := run(t, s, "echo hi | tr a-z A-Z")
	if code != 0 || strings.TrimSpace(out) != "HI" {
		t.Fatalf("exit %d out %q stderr %q", code, out, errb)
	}
	// External exit codes still propagate.
	if code, _, _ := run(t, s, "sh -c 'exit 3'"); code != 3 {
		t.Fatalf("exit code: %d", code)
	}
	// A pure-external pipeline shares one process group.
	if code, out, errb := run(t, s, "printf 'b\\na\\n' | sort | head -1"); code != 0 || strings.TrimSpace(out) != "a" {
		t.Fatalf("pipeline: %d %q stderr %q", code, out, errb)
	}
}

func TestJobStopParksAndAbandonsLine(t *testing.T) {
	var mu sync.Mutex
	parked := 0
	pgid := 0
	s := jobSession(func(pg int) {
		mu.Lock()
		parked++
		pgid = pg
		mu.Unlock()
	})

	var out, errb syncBuffer
	done := make(chan int, 1)
	go func() {
		done <- s.Run(context.Background(), "sleep 397 && echo after-resume",
			strings.NewReader(""), &out, &errb)
	}()

	deadline := time.Now().Add(5 * time.Second)
	var target int
	for time.Now().Before(deadline) && target == 0 {
		if pid, err := pgrepChild("sleep 397"); err == nil && pid != 0 {
			if syscall.Kill(-pid, syscall.SIGTSTP) == nil {
				target = pid
			}
		}
		if target == 0 {
			time.Sleep(20 * time.Millisecond)
		}
	}
	if target == 0 {
		t.Fatal("no stoppable sleep child appeared")
	}

	select {
	case code := <-done:
		if code != 148 {
			t.Fatalf("parked line exit = %d, want 148 (stderr %q)", code, errb.String())
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return after job stop")
	}
	mu.Lock()
	defer mu.Unlock()
	if parked != 1 {
		t.Fatalf("OnStop fired %d times, want 1", parked)
	}
	if pgid != target {
		t.Fatalf("parked pgid %d, want %d", pgid, target)
	}
	if strings.Contains(out.String(), "after-resume") {
		t.Fatal("rest of the line ran after the park")
	}
	if err := syscall.Kill(-pgid, syscall.SIGCONT); err != nil {
		t.Fatalf("SIGCONT: %v", err)
	}
	_ = syscall.Kill(-pgid, syscall.SIGKILL)
	reapUntilGone(pgid)
}

func TestJobCancelKillsGroup(t *testing.T) {
	s := jobSession(func(int) { t.Error("unexpected park") })
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(100 * time.Millisecond)
		cancel()
	}()
	var out, errb syncBuffer
	start := time.Now()
	code := s.Run(ctx, "sleep 30", strings.NewReader(""), &out, &errb)
	if elapsed := time.Since(start); elapsed > 10*time.Second {
		t.Fatalf("cancel took %v", elapsed)
	}
	if code == 0 {
		t.Fatalf("cancelled run reported success (stderr %q)", errb.String())
	}
}

func pgrepChild(pattern string) (int, error) {
	out, err := exec.Command("pgrep", "-P", strconv.Itoa(os.Getpid()), "-f", pattern).Output()
	if err != nil {
		return 0, nil
	}
	fields := strings.Fields(string(out))
	if len(fields) == 0 {
		return 0, nil
	}
	return strconv.Atoi(fields[0])
}

func reapUntilGone(pid int) {
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		var ws syscall.WaitStatus
		n, err := syscall.Wait4(pid, &ws, syscall.WNOHANG, nil)
		if err != nil || n == pid {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestBackgroundNoticeAndRegistry(t *testing.T) {
	var mu sync.Mutex
	var notices []BgJob
	s := jobSession(func(int) { t.Error("unexpected park") })
	s.Jobs.OnBackground = func(pid int, cmd string) {
		mu.Lock()
		notices = append(notices, BgJob{PID: pid, Cmd: cmd})
		mu.Unlock()
	}

	if code, _, _ := run(t, s, "true &"); code != 0 {
		t.Fatal("true & failed")
	}
	mu.Lock()
	if len(notices) != 0 {
		t.Fatalf("short-lived bg child earned a notice: %v", notices)
	}
	mu.Unlock()

	if code, _, errb := run(t, s, "sleep 398 & echo fg-part"); code != 0 {
		t.Fatalf("bg line failed: %s", errb)
	}
	mu.Lock()
	if len(notices) != 1 || !strings.Contains(notices[0].Cmd, "sleep 398") {
		mu.Unlock()
		t.Fatalf("notices: %v", notices)
	}
	pid := notices[0].PID
	mu.Unlock()
	jobs := s.Background()
	if len(jobs) != 1 || jobs[0].PID != pid {
		t.Fatalf("Background() = %v, want pid %d", jobs, pid)
	}
	_ = syscall.Kill(pid, syscall.SIGKILL)
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) && len(s.Background()) != 0 {
		time.Sleep(20 * time.Millisecond)
	}
	if left := s.Background(); len(left) != 0 {
		t.Fatalf("registry not pruned: %v", left)
	}
}

func TestBackgroundBuiltinOutputLandsBeforePrompt(t *testing.T) {
	s := jobSession(func(int) { t.Error("unexpected park") })
	_, out, _ := run(t, s, "echo bg-visible &")
	if !strings.Contains(out, "bg-visible") {
		t.Fatalf("bg builtin output missing at Run return: %q", out)
	}
}

func TestKillRoutesToPathBinary(t *testing.T) {
	var mu sync.Mutex
	var pid int
	s := jobSession(func(int) { t.Error("unexpected park") })
	s.Jobs.OnBackground = func(p int, _ string) {
		mu.Lock()
		pid = p
		mu.Unlock()
	}
	if code, _, errb := run(t, s, "sleep 399 &"); code != 0 {
		t.Fatalf("bg line: %s", errb)
	}
	mu.Lock()
	target := pid
	mu.Unlock()
	if target == 0 {
		t.Fatal("no bg notice")
	}
	if code, _, errb := run(t, s, "kill "+strconv.Itoa(target)); code != 0 {
		t.Fatalf("kill: exit %d, stderr %q", code, errb)
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) && len(s.Background()) != 0 {
		time.Sleep(20 * time.Millisecond)
	}
	if left := s.Background(); len(left) != 0 {
		t.Fatalf("job survived kill: %v", left)
	}
	// A user-defined kill function still wins over the rewrite.
	run(t, s, "kill() { echo shadowed-$1; }")
	if _, out, _ := run(t, s, "kill abc"); !strings.Contains(out, "shadowed-abc") {
		t.Fatalf("function shadow lost: %q", out)
	}
}

func TestAllBackgroundSkipsTerminal(t *testing.T) {
	cases := []struct {
		line string
		want bool
	}{
		{"sleep 100 &", true},
		{"sleep 1 & sleep 2 &", true},
		{"{ sleep 1; echo hi; } &", true},
		{"sleep 100 & vim", false},
		{"vim", false},
		{"echo hi | tr a-z A-Z", false},
		{"", false},
	}
	for _, tc := range cases {
		file, err := parse(tc.line, "")
		if err != nil {
			t.Fatalf("parse %q: %v", tc.line, err)
		}
		if got := allBackground(file); got != tc.want {
			t.Errorf("allBackground(%q) = %v, want %v", tc.line, got, tc.want)
		}
	}
}

func TestNotFoundRecorded(t *testing.T) {
	s := jobSession(func(int) { t.Error("unexpected park") })
	code, _, errb := run(t, s, "rf-no-such-command-xyz > /dev/null")
	if code != 127 {
		t.Fatalf("exit %d stderr %q", code, errb)
	}
	if got := s.NotFound(); len(got) != 1 || got[0] != "rf-no-such-command-xyz" {
		t.Fatalf("NotFound = %v", got)
	}
	// A later not-found mid-line records too, and successes never do.
	if code, _, _ := run(t, s, "true && rf-still-missing-abc; true"); code != 0 {
		t.Fatal("final exit should be 0")
	}
	if got := s.NotFound(); len(got) != 1 || got[0] != "rf-still-missing-abc" {
		t.Fatalf("NotFound = %v", got)
	}
	if _, _, _ = run(t, s, "true"); len(s.NotFound()) != 0 {
		t.Fatalf("NotFound not reset: %v", s.NotFound())
	}
}

func TestStartInGroupDeadLeader(t *testing.T) {
	truePath, err := exec.LookPath("true")
	if err != nil {
		t.Skip("no true binary")
	}
	leader := exec.Command(truePath)
	leader.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := leader.Start(); err != nil {
		t.Fatal(err)
	}
	_ = leader.Wait()
	deadPgid := leader.Process.Pid

	base := exec.Cmd{Path: truePath, Args: []string{"true"}}
	started, err := startInGroup(base, &syscall.SysProcAttr{Setpgid: true, Pgid: deadPgid}, true)
	if err != nil {
		t.Fatalf("fallback start failed: %v", err)
	}
	if err := started.Wait(); err != nil {
		t.Fatalf("wait: %v", err)
	}
	// Without fallback the dead-group join surfaces its error instead.
	if _, err := startInGroup(base, &syscall.SysProcAttr{Setpgid: true, Pgid: deadPgid}, false); err == nil {
		t.Fatal("expected join to a dead group to fail without fallback")
	}
}
