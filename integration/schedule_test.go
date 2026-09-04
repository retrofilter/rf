package integration

import "testing"

func TestScheduledSessionFilesGraphNode(t *testing.T) {
	t.Parallel()
	s := startShell(t, t.TempDir())

	s.sendLine(`session -n review 'claude /review' --in 4h --every day`)
	s.expect(`every`)
	s.expect(`day`)
	s.expect(`next`)

	s.sendLine(`nodes -t schedule | pick name every command`)
	s.expect(`review`)
	s.expect(`claude /review`)

	// Re-filing re-times the one node rather than adding a second.
	s.sendLine(`session -n review 'claude /review' --at 23:59`)
	s.expect(`23:59`)
	s.sendLine(`(length (nodes {:type "schedule"}))`)
	s.expect(`(?m)^1\r?$`)

	// --in and --at contradict; junk spans and past times error.
	s.sendLine(`session -n x --in 1h --at 09:00`)
	s.expect(`give one`)
	s.sendLine(`session -n x --in soonish`)
	s.expect(`expects a span`)

	s.sendLine(`session -n review --cancel`)
	s.expect(`(?m)^true\r?$`)
	s.sendLine(`session -n review --cancel`)
	s.expect(`no schedule named`)

	s.sendLine(`session '(agent "what time is it")' --in 3s`)
	s.expect(`what-time-is-it`)
	s.sendLine(`session -n what-time-is-it --cancel`)
	s.expect(`(?m)^true\r?$`)
	s.sendLine(`session --in 3s`)
	s.expect(`expects a command line`)
	s.sendLine(`session --cancel`)
	s.expect(`needs the schedule's --name`)
}
