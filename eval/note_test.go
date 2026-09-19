package eval

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestRefGrammar(t *testing.T) {
	for in, want := range map[string]Ref{
		"#42":                 {Key: "42"},
		"task:7":              {Kind: "task", Key: "7"},
		"note:Shopping List":  {Kind: "note", Key: "shopping-list"},
		"shopping-list":       {Kind: "note", Key: "shopping-list"},
		"project:rf":          {Kind: "project", Key: "rf"},
		" note: spaced-slug ": {Kind: "note", Key: "spaced-slug"},
	} {
		got, err := ParseRef(in)
		require.NoError(t, err, in)
		require.Equal(t, want, got, in)
	}
	for _, bad := range []string{"", "#x", "task:abc", "edge:3", "note:", "#"} {
		if _, err := ParseRef(bad); err == nil {
			t.Errorf("ParseRef(%q) should error", bad)
		}
	}
	require.Equal(t, "#42", Ref{Key: "42"}.String())
	require.Equal(t, "note:a", Ref{Kind: "note", Key: "a"}.String())

	refs := ExtractRefs("see [[task:3]] and [[shopping-list|the list]], again [[task:3]], plus [[#9]] and [[bad:ref]]")
	require.Equal(t, []Ref{{Kind: "task", Key: "3"}, {Kind: "note", Key: "shopping-list"}, {Key: "9"}}, refs)
}

func TestNoteSlug(t *testing.T) {
	require.Equal(t, "shopping-list", noteSlug("Shopping List"))
	require.Equal(t, "a-b", noteSlug("  a__b! "))
	require.Equal(t, "", noteSlug("!!!"))
}

func editorScript(t *testing.T, dir, body string) string {
	t.Helper()
	path := filepath.Join(dir, "ed.sh")
	require.NoError(t, os.WriteFile(path, []byte("#!/bin/sh\n"+body+"\n"), 0755))
	return path
}

func drainLines(t *testing.T, v Value) string {
	t.Helper()
	m, err := Materialize(v)
	require.NoError(t, err)
	return strings.TrimRight(string(m.(String)), "\n")
}

func TestNoteLifecycle(t *testing.T) {
	withTaskHome(t, func(home string, ev *Evaluator, env *Environment) {
		mustEval := func(expr string) Value {
			t.Helper()
			v, err := evalExpr(expr, ev, env)
			if err != nil {
				t.Fatalf("%s: %v", expr, err)
			}
			return v
		}
		noteNames := func(v Value) []string {
			t.Helper()
			var names []string
			for _, r := range v.([]Value) {
				names = append(names, string(r.(Dictionary)["name"].(String)))
			}
			return names
		}

		ev.SetSessionID("sess-1")
		dir := filepath.Join(home, "proj")
		require.NoError(t, os.MkdirAll(dir, 0755))
		mustEval(`(register-project "~/proj")`)
		require.NoError(t, os.Chdir(dir))

		// A bare body files under the cwd's project; the name slugifies.
		res := mustEval(`(note "Shopping List" {:text "# Groceries\n\n- eggs"})`).(Dictionary)
		require.Equal(t, String("shopping-list"), res["name"])
		require.Equal(t, String("note:shopping-list"), res["ref"])
		require.NotContains(t, res, "unresolved")
		id := res["id"]

		rows := mustEval(`(notes)`).([]Value)
		require.Len(t, rows, 1)
		row := rows[0].(Dictionary)
		require.Equal(t, id, row["id"])
		require.Equal(t, String("Groceries"), row["title"])
		require.NotContains(t, row, "project")

		// Showing a note is its document: front matter plus body, as lines.
		doc := drainLines(t, mustEval(`(note "shopping-list")`))
		require.Equal(t, "---\nname: shopping-list\nproject: proj\n---\n\n# Groceries\n\n- eggs", doc)

		// Session provenance rides on the node; the node resolves by reference.
		node := mustEval(`(node "note:shopping-list")`).(Dictionary)
		require.Equal(t, id, node["id"])
		require.Equal(t, String("note:shopping-list"), node["ref"])
		require.Equal(t, String("sess-1"), node["properties"].(Dictionary)["session"])

		// Front matter renames, re-files, and adds properties.
		res = mustEval(`(note "shopping-list" {:text "---\nname: groceries\nproject: \"\"\ntags: [errands, weekly]\n---\nbody"})`).(Dictionary)
		require.Equal(t, String("groceries"), res["name"])
		props := mustEval(`(node "note:groceries")`).(Dictionary)["properties"].(Dictionary)
		require.Contains(t, props, "tags")
		if _, err := evalExpr(`(node "note:shopping-list")`, ev, env); err == nil {
			t.Fatal("renamed note should not resolve under its old name")
		}
		doc = drainLines(t, mustEval(`(note "groceries")`))
		require.Equal(t, "---\nname: groceries\ntags:\n    - errands\n    - weekly\n---\n\nbody", doc)

		// Unfiled now, so hidden inside the project and visible everywhere.
		require.Empty(t, mustEval(`(notes)`))
		require.Equal(t, []string{"groceries"}, noteNames(mustEval(`(notes {:all #t})`)))
		require.NoError(t, os.Chdir(home))
		all := mustEval(`(notes)`).([]Value)
		require.Len(t, all, 1)
		require.Equal(t, String(""), all[0].(Dictionary)["project"])

		// --project re-files an existing note; a bare-body :text keeps it.
		mustEval(`(note "groceries" {:project "proj"})`)
		require.Equal(t, []string{"groceries"}, noteNames(mustEval(`(notes {:project "proj"})`)))
		mustEval(`(note "groceries" {:text "new body"})`)
		require.Equal(t, []string{"groceries"}, noteNames(mustEval(`(notes {:project "proj"})`)))
		props = mustEval(`(node "note:groceries")`).(Dictionary)["properties"].(Dictionary)
		require.Contains(t, props, "tags")
		require.Equal(t, String("new body"), props["text"])

		// Reserved keys and name clashes are refused.
		if _, err := evalExpr(`(note "other" {:text "---\ntext: x\n---\n"})`, ev, env); err == nil {
			t.Fatal("reserved front matter key should error")
		}
		mustEval(`(note "other" {:text "o"})`)
		if _, err := evalExpr(`(note "other" {:text "---\nname: groceries\n---\n"})`, ev, env); err == nil {
			t.Fatal("renaming onto an existing note should error")
		}

		// The assistant cannot open an editor, but may write :text.
		ev.SetCaller(CallerAssistant)
		if _, err := evalExpr(`(note "missing")`, ev, env); err == nil {
			t.Fatal("a missing note should error for the assistant rather than open an editor")
		}
		if _, err := evalExpr(`(note "groceries" {:edit #t})`, ev, env); err == nil {
			t.Fatal("note --edit should refuse the assistant")
		}
		mustEval(`(note "from-agent" {:text "hi"})`)
		ev.SetCaller(CallerUser)

		// For the user a missing note opens the editor and is created from it.
		t.Setenv("EDITOR", editorScript(t, home, `printf '\n# Milk\n' >> "$1"`))
		created := mustEval(`(note "Corner Shop")`).(Dictionary)
		require.Equal(t, String("corner-shop"), created["name"])
		require.Equal(t, "---\nname: corner-shop\n---\n\n# Milk", drainLines(t, mustEval(`(note "corner-shop")`)))
		mustEval(`(note "corner-shop" {:delete #t})`)

		// Delete is permanent.
		mustEval(`(note "groceries" {:delete #t})`)
		require.ElementsMatch(t, []string{"other", "from-agent"}, noteNames(mustEval(`(notes {:all #t})`)))
		if _, err := evalExpr(`(note "groceries" {:delete #t})`, ev, env); err == nil {
			t.Fatal("deleting a missing note should error")
		}
		require.ElementsMatch(t, []string{"from-agent", "other"}, NoteNames())
	})
}

func TestNoteLinks(t *testing.T) {
	withTaskHome(t, func(home string, ev *Evaluator, env *Environment) {
		mustEval := func(expr string) Value {
			t.Helper()
			v, err := evalExpr(expr, ev, env)
			if err != nil {
				t.Fatalf("%s: %v", expr, err)
			}
			return v
		}
		linksOf := func(ref string) []string {
			t.Helper()
			node := mustEval(`(node "` + ref + `")`).(Dictionary)
			var targets []string
			for _, e := range node["edges"].([]Value) {
				ed := e.(Dictionary)
				if ed["type"] == String("links") && ed["source"] == node["id"] {
					target := mustEval(`(node ` + PrintValue(ed["target"]) + `)`).(Dictionary)
					targets = append(targets, string(target["ref"].(String)))
				}
			}
			return targets
		}

		require.NoError(t, os.MkdirAll(filepath.Join(home, "proj"), 0755))
		mustEval(`(register-project "~/proj")`)
		taskID := mustEval(`(task "buy the groceries" {:project "proj"})`)
		taskRef := "task:" + PrintValue(taskID)

		// A link to a note that doesn't exist yet is reported, not an error.
		res := mustEval(`(note "plan" {:text "see [[` + taskRef + `]], [[project:proj]] and [[shopping-list]] twice: [[note:shopping-list]]"})`).(Dictionary)
		require.Equal(t, []Value{String("note:shopping-list")}, res["unresolved"])
		require.ElementsMatch(t, []string{taskRef, "project:proj"}, linksOf("note:plan"))

		// Creating the target resolves the dangling link retroactively.
		mustEval(`(note "shopping-list" {:text "[[plan]] links back; a self-link [[shopping-list]] is dropped"})`)
		require.ElementsMatch(t, []string{taskRef, "project:proj", "note:shopping-list"}, linksOf("note:plan"))
		require.Equal(t, []string{"note:plan"}, linksOf("note:shopping-list"))

		// Re-saving re-derives the edges: removed refs go, new ones come.
		mustEval(`(note "plan" {:text "only [[shopping-list]] now"})`)
		require.Equal(t, []string{"note:shopping-list"}, linksOf("note:plan"))

		// Task text carries refs too; the numeric spelling resolves as well.
		id := mustEval(`(task "check [[shopping-list]] and [[#` + PrintValue(taskID) + `]]" {:project "proj"})`)
		require.ElementsMatch(t, []string{"note:shopping-list", taskRef}, linksOf("task:"+PrintValue(id)))
		require.Equal(t, id, mustEval(`(node "#` + PrintValue(id) + `")`).(Dictionary)["id"])
		if _, err := evalExpr(`(node "task:99999")`, ev, env); err == nil {
			t.Fatal("task ref to a missing id should error")
		}
		if _, err := evalExpr(`(node "task:`+PrintValue(res["id"])+`")`, ev, env); err == nil {
			t.Fatal("task ref to a note should error")
		}
	})
}
