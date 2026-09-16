package integration

import "testing"

func TestTaskRoundTrip(t *testing.T) {
	t.Parallel()
	s := startShell(t, t.TempDir())
	// Unquoted words join, like remember; the id comes back.
	s.sendLine("task fix the flaky resize test")
	s.sendLine("tasks")
	s.expect(`id\s+status\s+age\s+project\s+text`)
	s.expect(`1\s+open\s+\d+s\s+fix the flaky resize test`)
	s.clear()
	s.sendLine("task -c 1")
	s.sendLine("tasks --done")
	s.expect(`1\s+done\s+\d+s\s+fix the flaky resize test`)
}
