package eval

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/retrofilter/rf/core"
	"github.com/stretchr/testify/require"
)

func withTaskHome(t *testing.T, fn func(home string, ev *Evaluator, env *Environment)) {
	t.Helper()
	home := t.TempDir()
	if resolved, err := filepath.EvalSymlinks(home); err == nil {
		home = resolved
	}
	t.Setenv("HOME", home)
	t.Chdir(home)
	db := createTestDB(t)
	t.Cleanup(func() { db.Close() })
	ev := NewEvaluatorWithEnvironment(db, core.NewGraphStore(db))
	fn(home, ev, ev.globalEnv)
}

func TestRegisterProject(t *testing.T) {
	withTaskHome(t, func(home string, ev *Evaluator, env *Environment) {
		mustEval := func(expr string) Value {
			t.Helper()
			v, err := evalExpr(expr, ev, env)
			if err != nil {
				t.Fatalf("%s: %v", expr, err)
			}
			return v
		}

		dir := filepath.Join(home, "work", "thing")
		require.NoError(t, os.MkdirAll(filepath.Join(dir, "sub"), 0755))

		mustEval(`(register-project "~/work/thing")`)

		// Registry-first resolution, from the directory and below.
		name, hub, ok := FindProject(filepath.Join(dir, "sub"))
		require.True(t, ok)
		require.Equal(t, "thing", name)
		require.Equal(t, dir, hub)

		// Longest-prefix: a nested registration wins inside its subtree.
		nested := filepath.Join(dir, "sub", "tool")
		require.NoError(t, os.MkdirAll(nested, 0755))
		mustEval(`(register-project "~/work/thing/sub/tool" {:name "tool"})`)
		name, _, ok = FindProject(nested)
		require.True(t, ok)
		require.Equal(t, "tool", name)
		name, _, _ = FindProject(dir)
		require.Equal(t, "thing", name)

		// Duplicate names and paths error.
		other := filepath.Join(home, "elsewhere")
		require.NoError(t, os.MkdirAll(other, 0755))
		if _, err := evalExpr(`(register-project "~/elsewhere" {:name "thing"})`, ev, env); err == nil {
			t.Fatal("duplicate name should error")
		}
		if _, err := evalExpr(`(register-project "~/work/thing")`, ev, env); err == nil {
			t.Fatal("duplicate path should error")
		}

		names := ProjectNames()
		require.Equal(t, []string{"thing", "tool"}, names)
		require.NoError(t, os.MkdirAll(filepath.Join(home, "src", "legacy"), 0755))
		rows := mustEval(`(projects)`).([]Value)
		require.Len(t, rows, 2)
		require.Equal(t, String("thing"), rows[0].(Dictionary)["name"])
		require.Equal(t, String("tool"), rows[1].(Dictionary)["name"])

		// unregister-project drops it from resolution.
		mustEval(`(unregister-project "tool")`)
		name, _, ok = FindProject(nested)
		require.True(t, ok) // still inside "thing"
		require.Equal(t, "thing", name)
		if _, err := evalExpr(`(unregister-project "tool")`, ev, env); err == nil {
			t.Fatal("unregistering an unknown project should error")
		}
	})
}

func TestProjectWithoutDirectory(t *testing.T) {
	withTaskHome(t, func(home string, ev *Evaluator, env *Environment) {
		mustEval := func(expr string) Value {
			t.Helper()
			v, err := evalExpr(expr, ev, env)
			if err != nil {
				t.Fatalf("%s: %v", expr, err)
			}
			return v
		}

		// project NAME with nothing behind it files a pathless node: no dir, no git.
		id, ok := mustEval(`(project "misc")`).(Integer)
		require.True(t, ok, "creating a project should return its node id")
		require.NotZero(t, id)
		entries, _ := os.ReadDir(filepath.Join(home, "src"))
		require.Empty(t, entries, "a pathless project must create nothing under the project root")
		require.Equal(t, home, cwdOf(t), "a pathless project never changes directory")

		rows := mustEval(`(projects)`).([]Value)
		require.Len(t, rows, 1)
		require.Equal(t, String("misc"), rows[0].(Dictionary)["name"])
		require.Equal(t, String(""), rows[0].(Dictionary)["path"])

		// A pathless project never claims a directory.
		_, _, found := FindProject(home)
		require.False(t, found)

		// Naming it again describes rather than re-creates.
		dict := mustEval(`(project "misc")`).(Dictionary)
		require.Equal(t, String("misc"), dict["name"])
		require.Equal(t, String(""), dict["text"])

		// Tasks file under it by name.
		mustEval(`(task "sort the garage" {:project "misc"})`)
		tasks := mustEval(`(tasks {:project "misc"})`).([]Value)
		require.Len(t, tasks, 1)

		// --text sets the markdown; "" clears it.
		mustEval(`(project "misc" {:text "# Misc\n\nodds and ends"})`)
		dict = mustEval(`(project "misc")`).(Dictionary)
		require.Equal(t, String("# Misc\n\nodds and ends"), dict["text"])
		mustEval(`(project "misc" {:text ""})`)
		dict = mustEval(`(project "misc")`).(Dictionary)
		require.Equal(t, String(""), dict["text"])

		// --edit runs $EDITOR on a temp file and stores what it leaves behind.
		script := filepath.Join(home, "fake-editor.sh")
		require.NoError(t, os.WriteFile(script, []byte("#!/bin/sh\nfor f; do :; done\nprintf 'edited **notes**\\n' > \"$f\"\n"), 0755))
		t.Setenv("EDITOR", script+" -q")
		require.Equal(t, true, mustEval(`(project "misc" {:edit #t})`))
		dict = mustEval(`(project "misc")`).(Dictionary)
		require.Equal(t, String("edited **notes**"), dict["text"])

		// Unknown names for edit error instead of creating.
		if _, err := evalExpr(`(project "nope" {:edit #t})`, ev, env); err == nil {
			t.Fatal("editing an unknown project should error")
		}
		if _, err := evalExpr(`(project "misc" {:edit #t :text "x"})`, ev, env); err == nil {
			t.Fatal("--edit with --text should error as exclusive")
		}

		// Bare project inside a registered directory describes it.
		dir := filepath.Join(home, "work", "thing")
		require.NoError(t, os.MkdirAll(dir, 0755))
		mustEval(`(register-project "~/work/thing")`)
		t.Chdir(dir)
		dict = mustEval(`(project)`).(Dictionary)
		require.Equal(t, String("thing"), dict["name"])
		require.Equal(t, String(dir), dict["path"])
		mustEval(`(project {:text "the thing"})`)
		require.Equal(t, String("the thing"), mustEval(`(project)`).(Dictionary)["text"])
	})
}

func TestProjectWordsAreUserOnly(t *testing.T) {
	withTaskHome(t, func(home string, ev *Evaluator, env *Environment) {
		if _, err := evalExpr(`(project "misc")`, ev, env); err != nil {
			t.Fatal(err)
		}
		ev.SetCaller(CallerAssistant)
		defer ev.SetCaller(CallerUser)
		for _, form := range []string{
			`(project)`, `(project "misc")`, `(project "misc" {:text "x"})`, `(project "misc" {:edit #t})`,
			`(project "new" {:init #t})`, `(projects)`,
			`(register-project "~")`, `(unregister-project "misc")`,
		} {
			_, err := evalExpr(form, ev, env)
			require.Error(t, err, form)
			require.Contains(t, err.Error(), "only be run by the user", form)
		}
		// Reading and filing tasks stays open to the assistant.
		if _, err := evalExpr(`(task "still allowed" {:project "misc"})`, ev, env); err != nil {
			t.Fatal(err)
		}
		if _, err := evalExpr(`(tasks {:project "misc"})`, ev, env); err != nil {
			t.Fatal(err)
		}
	})
}

func cwdOf(t *testing.T) string {
	t.Helper()
	d, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func TestTaskLifecycle(t *testing.T) {
	withTaskHome(t, func(home string, ev *Evaluator, env *Environment) {
		mustEval := func(expr string) Value {
			t.Helper()
			v, err := evalExpr(expr, ev, env)
			if err != nil {
				t.Fatalf("%s: %v", expr, err)
			}
			return v
		}
		taskTexts := func(v Value) []string {
			t.Helper()
			var texts []string
			for _, r := range v.([]Value) {
				texts = append(texts, string(r.(Dictionary)["text"].(String)))
			}
			return texts
		}

		ev.SetSessionID("sess-1")
		dir := filepath.Join(home, "proj")
		require.NoError(t, os.MkdirAll(dir, 0755))
		mustEval(`(register-project "~/proj")`)
		require.NoError(t, os.Chdir(dir))

		id := mustEval(`(task "fix" "the" "flaky" "test")`)
		require.IsType(t, Integer(0), id)

		// The row shape: id, status, age, text (+ project only with :all).
		rows := mustEval(`(tasks)`).([]Value)
		require.Len(t, rows, 1)
		row := rows[0].(Dictionary)
		require.Equal(t, id, row["id"])
		require.Equal(t, String("open"), row["status"])
		require.Equal(t, String("fix the flaky test"), row["text"])
		require.NotContains(t, row, "project")
		require.Contains(t, row, "_age-days")

		// Session provenance rides on the node.
		node := mustEval(`(node ` + PrintValue(id) + `)`).(Dictionary)
		props := node["properties"].(Dictionary)
		require.Equal(t, String("sess-1"), props["session"])

		require.NoError(t, os.Chdir(home))
		mustEval(`(task "global" "chore")`)
		require.Equal(t, []string{"global chore"}, taskTexts(mustEval(`(tasks)`)))
		require.NoError(t, os.Chdir(dir))
		require.Equal(t, []string{"fix the flaky test"}, taskTexts(mustEval(`(tasks)`)))

		// :all sees both and gains the project column.
		all := mustEval(`(tasks {:all #t})`).([]Value)
		require.Len(t, all, 2)
		for _, r := range all {
			require.Contains(t, r.(Dictionary), "project")
		}

		// task -c closes; --done shows it with closed-at stamped.
		mustEval(`(task {:complete ` + PrintValue(id) + `})`)
		require.Empty(t, mustEval(`(tasks)`))
		closed := mustEval(`(tasks {:done #t})`).([]Value)
		require.Len(t, closed, 1)
		require.Equal(t, String("done"), closed[0].(Dictionary)["status"])
		if _, err := evalExpr(`(task {:complete 99999})`, ev, env); err == nil {
			t.Fatal("task -c on a missing id should error")
		}

		// delete-task removes the node outright — gone even from --done.
		mustEval(`(delete-task ` + PrintValue(id) + `)`)
		require.Empty(t, mustEval(`(tasks {:done #t})`))
		if _, err := evalExpr(`(delete-task `+PrintValue(id)+`)`, ev, env); err == nil {
			t.Fatal("delete-task on a deleted id should error")
		}
	})
}

func TestTaskComplete(t *testing.T) {
	withTaskHome(t, func(home string, ev *Evaluator, env *Environment) {
		mustEval := func(expr string) Value {
			t.Helper()
			v, err := evalExpr(expr, ev, env)
			if err != nil {
				t.Fatalf("%s: %v", expr, err)
			}
			return v
		}

		id := mustEval(`(task "ship the rf -e surface")`)

		require.Equal(t, true, mustEval(`(task {:complete `+PrintValue(id)+`})`))
		require.Empty(t, mustEval(`(tasks)`))
		closed := mustEval(`(tasks {:done #t})`).([]Value)
		require.Len(t, closed, 1)
		require.Equal(t, String("done"), closed[0].(Dictionary)["status"])

		// Command mode desugars task --complete N to the same dict.
		id2 := mustEval(`(task "second")`)
		require.Equal(t, true, mustEval(`(task {:complete `+PrintValue(id2)+`})`))

		// Text alongside --complete is a contradiction, not a filing.
		if _, err := evalExpr(`(task "text" {:complete 1})`, ev, env); err == nil {
			t.Fatal("task --complete with text should error")
		}
		// Missing ids and non-task nodes error like done.
		if _, err := evalExpr(`(task {:complete 99999})`, ev, env); err == nil {
			t.Fatal("task --complete on a missing id should error")
		}
		// Bare task with neither text nor :complete still errors.
		if _, err := evalExpr(`(task)`, ev, env); err == nil {
			t.Fatal("task with no text should error")
		}

		id3 := mustEval(`(task "third")`)
		require.Equal(t, true, mustEval(`(task {:delete `+PrintValue(id3)+`})`))
		require.Empty(t, mustEval(`(tasks)`))
		require.Len(t, mustEval(`(tasks {:done #t})`).([]Value), 2)
		if _, err := evalExpr(`(task {:delete `+PrintValue(id3)+`})`, ev, env); err == nil {
			t.Fatal("task --delete on a deleted id should error")
		}
		if _, err := evalExpr(`(task "text" {:delete 1})`, ev, env); err == nil {
			t.Fatal("task --delete with text should error")
		}
		if _, err := evalExpr(`(task {:delete 1 :complete 1})`, ev, env); err == nil {
			t.Fatal("task --delete with --complete should error")
		}
		nid := mustEval(`(remember "not a task")`)
		if _, err := evalExpr(`(task {:delete `+PrintValue(nid)+`})`, ev, env); err == nil {
			t.Fatal("task --delete on a non-task node should error")
		}
		require.Len(t, mustEval(`(recall "not a task")`).([]Value), 1)
	})
}

func TestTaskDependencies(t *testing.T) {
	withTaskHome(t, func(home string, ev *Evaluator, env *Environment) {
		mustEval := func(expr string) Value {
			t.Helper()
			v, err := evalExpr(expr, ev, env)
			if err != nil {
				t.Fatalf("%s: %v", expr, err)
			}
			return v
		}

		dir := filepath.Join(home, "proj")
		require.NoError(t, os.MkdirAll(dir, 0755))
		mustEval(`(register-project "~/proj")`)
		require.NoError(t, os.Chdir(dir))

		blocked := mustEval(`(task "ship" "it")`)
		blocker := mustEval(`(task "write" "tests" {:blocks ` + PrintValue(blocked) + `})`)

		// --ready: the blocked task is filtered while its blocker is open.
		ready := mustEval(`(tasks {:ready #t})`).([]Value)
		require.Len(t, ready, 1)
		require.Equal(t, blocker, ready[0].(Dictionary)["id"])

		mustEval(`(task {:complete ` + PrintValue(blocker) + `})`)
		ready = mustEval(`(tasks {:ready #t})`).([]Value)
		require.Len(t, ready, 1)
		require.Equal(t, blocked, ready[0].(Dictionary)["id"])

		// :blocks must point at a real task.
		if _, err := evalExpr(`(task "x" {:blocks 99999})`, ev, env); err == nil {
			t.Fatal("blocks on a missing id should error")
		}
	})
}

func TestTaskLazyRegistration(t *testing.T) {
	withTaskHome(t, func(home string, ev *Evaluator, env *Environment) {
		hub := filepath.Join(home, "src", "demo", "main")
		require.NoError(t, os.MkdirAll(hub, 0755))
		require.NoError(t, os.Chdir(hub))

		if _, err := evalExpr(`(task "first")`, ev, env); err != nil {
			t.Fatal(err)
		}
		rp, ok := registeredProjectByName("demo")
		require.True(t, ok, "filing a task should register the legacy project")
		require.Equal(t, filepath.Join(home, "src", "demo"), rp.Path)

		// A second task reuses the node — no duplicate registration.
		if _, err := evalExpr(`(task "second")`, ev, env); err != nil {
			t.Fatal(err)
		}
		rows, err := evalExpr(`(tasks)`, ev, env)
		require.NoError(t, err)
		require.Len(t, rows.([]Value), 2)
	})
}

func TestHumanAge(t *testing.T) {
	for _, tc := range []struct {
		d    time.Duration
		want string
	}{
		{30 * time.Second, "30s"},
		{90 * time.Second, "1m"},
		{2 * time.Hour, "2h"},
		{72 * time.Hour, "3d"},
	} {
		if got := humanAge(tc.d); got != tc.want {
			t.Errorf("humanAge(%v) = %q, want %q", tc.d, got, tc.want)
		}
	}
}
