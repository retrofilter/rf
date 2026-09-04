package console

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestDirLabel(t *testing.T) {
	require.Equal(t, "~/src/rf", dirLabel("/Users/john/src/rf"))
	require.Equal(t, "~/src/rf", dirLabel("/home/john/src/rf"))
	require.Equal(t, "/opt/data", dirLabel("/opt/data"))
	require.Equal(t, "", dirLabel(""))
}

func TestDetailOf(t *testing.T) {
	require.Equal(t, "rf · ~/src", detailOf(Session{Title: "rf", Dir: "/Users/j/src"}))
	require.Equal(t, "rf", detailOf(Session{Title: "rf"}))
	require.Equal(t, "~/src", detailOf(Session{Dir: "/home/j/src"}))
	require.Equal(t, "fix the bug", detailOf(Session{Task: "fix the bug"}))
	require.Equal(t, "adopted session", detailOf(Session{}))
}

func TestFmtElapsed(t *testing.T) {
	require.Equal(t, "", fmtElapsed(0))
	require.Equal(t, "", fmtElapsed(-4))
	require.Equal(t, "45s", fmtElapsed(45))
	require.Equal(t, "3m 20s", fmtElapsed(200))
	require.Equal(t, "2h 5m", fmtElapsed(2*3600+5*60+30))
}

func TestFmtDiff(t *testing.T) {
	require.Equal(t, "no changes", fmtDiff(Session{}))
	require.Equal(t, "+12 −4", fmtDiff(Session{Added: 12, Deleted: 4}))
	require.Equal(t, "+0 −4", fmtDiff(Session{Deleted: 4}))
}

func TestFmtReset(t *testing.T) {
	now := time.Date(2026, 8, 13, 12, 0, 0, 0, time.UTC)
	require.Equal(t, "", fmtReset(time.Time{}, now))
	require.Equal(t, "", fmtReset(now.Add(-time.Hour), now))
	require.Equal(t, "14m", fmtReset(now.Add(14*time.Minute+30*time.Second), now))
	require.Equal(t, "2h 14m", fmtReset(now.Add(2*time.Hour+14*time.Minute), now))
	require.Equal(t, "3d 4h", fmtReset(now.Add(3*24*time.Hour+4*time.Hour), now))
}

func TestClampAndFillWidth(t *testing.T) {
	require.Equal(t, 0, clampPct(-5))
	require.Equal(t, 62, clampPct(61.7))
	require.Equal(t, 100, clampPct(150))
	require.Equal(t, "width: 0%", fillWidth(nil))
	require.Equal(t, "width: 62%", fillWidth(&UsageWindow{Utilization: 61.7}))
}

func TestHarnessLabel(t *testing.T) {
	sessions := []Session{
		{ID: "s1", Harness: "claude"},
		{ID: "s2", Harness: "rf"},
		{ID: "s3", Harness: ""},
	}
	label := func(active string) string {
		return viewModel{Sessions: sessions, Active: active, View: viewTerminal}.harnessLabel()
	}
	require.Equal(t, "Claude", label("s1"))
	require.Equal(t, "rf", label("s2"))
	require.Equal(t, "shell", label("s3"))
	require.Equal(t, "", label("gone"))
	require.Equal(t, "", label(""))
}

func TestPollEvents(t *testing.T) {
	fleet := []Session{{ID: "s1"}, {ID: "s2"}}

	// Empty fleet: land on the overview (and detach a stale selection).
	ev := pollEvents(viewModel{View: viewTerminal})
	require.Equal(t, map[string]any{"rf:showoverview": true}, ev)
	ev = pollEvents(viewModel{Active: "gone", View: viewTerminal})
	require.Equal(t, map[string]any{"rf:detached": true, "rf:showoverview": true}, ev)
	// Already on the overview, or mid-naming (⌘K): nothing to do.
	require.Empty(t, pollEvents(viewModel{View: viewOverview}))
	require.Empty(t, pollEvents(viewModel{View: viewNew}))

	ev = pollEvents(viewModel{Sessions: fleet, View: viewTerminal})
	require.Equal(t, map[string]any{"rf:select": map[string]string{"id": "s1"}}, ev)
	ev = pollEvents(viewModel{Sessions: fleet, Active: "gone", View: viewTerminal})
	require.Equal(t, map[string]any{"rf:select": map[string]string{"id": "s1"}}, ev)

	// A live selection is left alone.
	require.Empty(t, pollEvents(viewModel{Sessions: fleet, Active: "s2", View: viewTerminal}))

	// Naming view: a dead selection detaches, but nothing auto-selects.
	ev = pollEvents(viewModel{Sessions: fleet, Active: "gone", View: viewNew})
	require.Equal(t, map[string]any{"rf:detached": true}, ev)
	require.Empty(t, pollEvents(viewModel{Sessions: fleet, View: viewNew}))
}
