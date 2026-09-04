package eval

import (
	"testing"

	"github.com/retrofilter/rf/core"
	"github.com/retrofilter/rf/models"
	"github.com/stretchr/testify/require"
)

func TestHistoryBuiltin(t *testing.T) {
	db := createTestDB(t)
	defer db.Close()
	ev := NewEvaluatorWithEnvironment(db, core.NewGraphStore(db))
	env := ev.globalEnv

	add := func(line, mode, cwd, project string, exit int) {
		t.Helper()
		e := &models.HistoryEntry{Line: line, Mode: mode, Cwd: cwd, Project: project, Session: "s1"}
		require.NoError(t, models.AddHistory(db, e))
		require.NoError(t, models.SetHistoryExit(db, e.ID, exit))
	}
	add("ls", "command", "/home/u", "", 0)
	add("git status", "command", "/home/u/src/foo/main", "foo", 0)
	add("make test", "command", "/home/u/src/foo/main", "foo", 2)
	add("(+ 1 2)", "scheme", "/home/u/src/foobar", "foobar", 0)
	add("summarize this repo", "agent", "/home/u/src/foo/main", "foo", 0)

	rows := func(expr string) []Value {
		t.Helper()
		v, err := evalExpr(expr, ev, env)
		require.NoError(t, err, expr)
		list, ok := v.([]Value)
		require.True(t, ok, "%s should return a list, got %v", expr, v)
		return list
	}
	lines := func(list []Value) []string {
		var out []string
		for _, r := range list {
			out = append(out, string(r.(Dictionary)["line"].(String)))
		}
		return out
	}

	// No filters: everything, oldest first
	require.Equal(t,
		[]string{"ls", "git status", "make test", "(+ 1 2)", "summarize this repo"},
		lines(rows(`(history)`)))

	// Pattern filter
	require.Equal(t, []string{"git status"}, lines(rows(`(history "git")`)))

	require.Equal(t,
		[]string{"git status", "make test", "summarize this repo"},
		lines(rows(`(history {:dir "/home/u/src/foo"})`)))

	// Mode, project, and limit options
	require.Equal(t, []string{"(+ 1 2)"}, lines(rows(`(history {:mode "scheme"})`)))
	require.Equal(t,
		[]string{"git status", "make test", "summarize this repo"},
		lines(rows(`(history {:project "foo"})`)))
	require.Equal(t,
		[]string{"(+ 1 2)", "summarize this repo"},
		lines(rows(`(history {:limit 2})`)))

	// Exit codes come through; unknown exits omit the key
	made := rows(`(history "make")`)
	require.Equal(t, Integer(2), made[0].(Dictionary)["exit"])

	// LIKE metacharacters are literal
	add("watch 100%", "command", "/home/u", "", 0)
	require.Equal(t, []string{"watch 100%"}, lines(rows(`(history "100%")`)))
	if list, err := evalExpr(`(history "%")`, ev, env); err != nil {
		t.Fatal(err)
	} else {
		require.Len(t, list.([]Value), 1)
	}

	// Unknown options error, naming the supported set
	_, err := evalExpr(`(history :everywhere)`, ev, env)
	require.ErrorContains(t, err, ":dir")
	_, err = evalExpr(`(history {:folder "x"})`, ev, env)
	require.ErrorContains(t, err, ":dir")

	self := &models.HistoryEntry{Line: "history git", Mode: "command", Cwd: "/home/u", Session: "s1"}
	require.NoError(t, models.AddHistory(db, self))
	ev.SetInFlightHistory(self.ID)
	require.Equal(t, []string{"git status"}, lines(rows(`(history "git")`)))
	// The next line overwrites the exclusion; the completed row matches.
	ev.SetInFlightHistory(0)
	require.Equal(t, []string{"git status", "history git"}, lines(rows(`(history "git")`)))
}

func TestHistoryDirContractsHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	db := createTestDB(t)
	defer db.Close()
	ev := NewEvaluatorWithEnvironment(db, core.NewGraphStore(db))
	env := ev.globalEnv

	for _, cwd := range []string{home, home + "/retrofilter", home + "x/other", "/tmp"} {
		e := &models.HistoryEntry{Line: "ls", Mode: "command", Cwd: cwd, Session: "s1"}
		require.NoError(t, models.AddHistory(db, e))
	}

	v, err := evalExpr(`(history)`, ev, env)
	require.NoError(t, err)
	var dirs []string
	for _, r := range v.([]Value) {
		dirs = append(dirs, string(r.(Dictionary)["dir"].(String)))
	}
	require.Equal(t, []string{"~", "~/retrofilter", home + "x/other", "/tmp"}, dirs)
}
