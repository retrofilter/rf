package console

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestStatusLayering(t *testing.T) {
	now := time.Now()
	r := NewRegistry(nil)
	r.now = func() time.Time { return now }

	created := now.Add(-time.Minute)
	var activity time.Time
	status := func() string { return statusOf(r.hooks["x"], created, activity, r.now()) }

	// No evidence at all → IDLE.
	require.Equal(t, StatusIdle, status())

	// Recent output → RUNNING.
	activity = now.Add(-time.Second)
	require.Equal(t, StatusRunning, status())

	// Hook evidence newer than output wins.
	r.ReportHook("x", "Notification")
	require.Equal(t, StatusBlocked, status())
	r.ReportHook("x", "Stop")
	require.Equal(t, StatusReview, status())

	activity = now.Add(3 * time.Second)
	now = now.Add(4 * time.Second)
	require.Equal(t, StatusRunning, status())

	// Unknown events keep the previous status.
	r2 := NewRegistry(nil)
	r2.ReportHook("y", "Stop")
	r2.ReportHook("y", "SomethingNew")
	require.Equal(t, StatusReview, r2.hooks["y"].Status)
}

func TestHarnessOf(t *testing.T) {
	require.Equal(t, "claude", harnessOf("claude"))
	require.Equal(t, "rf", harnessOf("rf"))
	require.Equal(t, "", harnessOf("zsh"))
	require.Equal(t, "", harnessOf(""))
}

func TestGitDiffStat(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	dir := t.TempDir()
	run := func(args ...string) {
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		out, err := cmd.CombinedOutput()
		require.NoError(t, err, string(out))
	}
	run("init", "-q")
	require.NoError(t, os.WriteFile(filepath.Join(dir, "a.txt"), []byte("one\ntwo\nthree\n"), 0644))
	run("add", "a.txt")
	run("commit", "-q", "-m", "init")

	added, deleted := gitDiffStat(dir)
	require.Zero(t, added)
	require.Zero(t, deleted)

	require.NoError(t, os.WriteFile(filepath.Join(dir, "a.txt"), []byte("one\nTWO\nthree\nfour\n"), 0644))
	added, deleted = gitDiffStat(dir)
	require.Equal(t, 2, added)
	require.Equal(t, 1, deleted)
}
