package eval

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"
)

func withTempDir(t *testing.T, fn func(dir string, eval *Evaluator, env *Environment)) {
	t.Helper()
	orig, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	dir := t.TempDir()
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	defer os.Chdir(orig)

	// Resolve symlinks (macOS /var -> /private/var) so path comparisons work.
	dir, err = os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}

	eval := NewEvaluator()
	fn(dir, eval, eval.globalEnv)
}

func TestShellFileBuiltins(t *testing.T) {
	withTempDir(t, func(dir string, eval *Evaluator, env *Environment) {
		tests := []struct {
			expr      string
			expect    Value
			shouldErr bool
		}{
			{"(pwd)", String(dir), false},
			{"(write-file \"a.txt\" \"hello\")", Integer(5), false},
			// cat is a line stream; materialized text is newline-terminated
			{"(cat \"a.txt\")", String("hello\n"), false},
			{"(append-file \"a.txt\" \" world\")", Integer(6), false},
			{"(cat \"a.txt\")", String("hello world\n"), false},
			{"(exists? \"a.txt\")", true, false},
			{"(exists? \"missing.txt\")", false, false},
			{"(cp \"a.txt\" \"b.txt\")", String("b.txt"), false},
			{"(cat \"b.txt\")", String("hello world\n"), false},
			{"(mv \"b.txt\" \"c.txt\")", String("c.txt"), false},
			{"(exists? \"b.txt\")", false, false},
			{"(mkdir \"sub/nested\")", String("sub/nested"), false},
			{"(exists? \"sub/nested\")", true, false},
			{"(rm \"c.txt\")", true, false},
			{"(exists? \"c.txt\")", false, false},
			{"(rm \"sub\" :recursive)", true, false},
			{"(exists? \"sub\")", false, false},
			// the pre-option-convention positional boolean is gone
			{"(rm \"a.txt\" #t)", nil, true},
			// glob
			{"(glob \"*.txt\")", []Value{String("a.txt")}, false},
			// path helpers
			{"(basename \"/x/y/z.txt\")", String("z.txt"), false},
			{"(dirname \"/x/y/z.txt\")", String("/x/y"), false},
			{"(path-join \"a\" \"b\" \"c.txt\")", String("a/b/c.txt"), false},
			// errors
			{"(cat \"missing.txt\")", nil, true},
			{"(rm \"missing.txt\")", nil, true},
			{"(cp \"missing.txt\" \"x\")", nil, true},
			{"(pwd 1)", nil, true},
			{"(write-file \"x\")", nil, true},
		}
		for _, tc := range tests {
			got, err := evalExpr(tc.expr, eval, env)
			if tc.shouldErr {
				if err == nil {
					t.Errorf("expected error for %q, got %v", tc.expr, got)
				}
				continue
			}
			if err != nil {
				t.Errorf("unexpected error for %q: %v", tc.expr, err)
				continue
			}
			if !deepEqual(got, tc.expect) {
				t.Errorf("for %q, got %v, want %v", tc.expr, got, tc.expect)
			}
		}
	})
}

func TestGlobExpansionBuiltins(t *testing.T) {
	withTempDir(t, func(dir string, eval *Evaluator, env *Environment) {
		write := func(name, content string) {
			t.Helper()
			if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0644); err != nil {
				t.Fatal(err)
			}
		}
		write("a.go", "alpha\n")
		write("b.go", "beta\n")
		write("note.txt", "note\n")
		write(".hidden.go", "hidden\n")
		for _, d := range []string{"sub", "dest"} {
			if err := os.Mkdir(filepath.Join(dir, d), 0755); err != nil {
				t.Fatal(err)
			}
		}

		tests := []struct {
			expr      string
			expect    Value
			shouldErr bool
		}{
			{`(glob "*.go")`, []Value{String("a.go"), String("b.go")}, false},
			{`(glob ".*.go")`, []Value{String(".hidden.go")}, false},
			{`(glob "*.go" :all)`, []Value{String(".hidden.go"), String("a.go"), String("b.go")}, false},
			{`(length (ls (glob "*.go")))`, Integer(2), false},
			{`(get "name" (get 0 (ls (glob "*.go"))))`, String("a.go"), false},
			{`(get "type" (get 0 (ls (glob "su*"))))`, String("dir"), false},
			// stat: scalar row for a string, rows for an expansion
			{`(get "size" (stat "a.go"))`, Integer(6), false},
			{`(length (stat (glob "*.go")))`, Integer(2), false},
			// cat concatenates the files' lines in order, still lazily
			{`(cat (glob "*.go"))`, String("alpha\nbeta\n"), false},
			{`(take 1 (cat (glob "*.go")))`, String("alpha\n"), false},
			{`(cp (glob "*.go") "sub")`, []Value{String("sub/a.go"), String("sub/b.go")}, false},
			{`(cat "sub/b.go")`, String("beta\n"), false},
			{`(cp (glob "*.go") "note.txt")`, nil, true},
			{`(mv (glob "sub/*.go") "dest")`, []Value{String("dest/a.go"), String("dest/b.go")}, false},
			{`(exists? "sub/a.go")`, false, false},
			{`(cat "dest/a.go")`, String("alpha\n"), false},
			// rm over an expansion; an empty expansion is a no-op (nullglob)
			{`(rm (glob "dest/*.go"))`, true, false},
			{`(length (glob "dest/*.go"))`, Integer(0), false},
			{`(rm (glob "*.zig"))`, true, false},
			// non-string list elements error, pointing at the builtin
			{`(ls (list 1 2))`, nil, true},
		}
		for _, tc := range tests {
			got, err := evalExpr(tc.expr, eval, env)
			if tc.shouldErr {
				if err == nil {
					t.Errorf("expected error for %q, got %v", tc.expr, got)
				}
				continue
			}
			if err != nil {
				t.Errorf("unexpected error for %q: %v", tc.expr, err)
				continue
			}
			if !deepEqual(got, tc.expect) {
				t.Errorf("for %q, got %v, want %v", tc.expr, got, tc.expect)
			}
		}
	})
}

func TestShellLsAndInspection(t *testing.T) {
	withTempDir(t, func(dir string, eval *Evaluator, env *Environment) {
		if err := os.WriteFile(filepath.Join(dir, "f.txt"), []byte("12345"), 0644); err != nil {
			t.Fatal(err)
		}
		if err := os.Mkdir(filepath.Join(dir, "d"), 0755); err != nil {
			t.Fatal(err)
		}

		got, err := evalExpr("(ls)", eval, env)
		if err != nil {
			t.Fatalf("ls: %v", err)
		}
		entries, ok := got.([]Value)
		if !ok || len(entries) != 2 {
			t.Fatalf("ls: expected 2 entries, got %v", got)
		}
		for _, e := range entries {
			dict, ok := e.(Dictionary)
			if !ok {
				t.Fatalf("ls entry is not a dictionary: %v", e)
			}
			name := dict["name"].(String)
			switch string(name) {
			case "f.txt":
				if dict["type"] != String("file") || dict["size"] != Integer(5) {
					t.Errorf("f.txt entry wrong: %v", dict)
				}
			case "d":
				if dict["type"] != String("dir") {
					t.Errorf("d entry wrong: %v", dict)
				}
			default:
				t.Errorf("unexpected entry %s", name)
			}
		}

		// Hidden files are excluded by default; :all includes them
		if err := os.WriteFile(filepath.Join(dir, ".secret"), []byte("x"), 0644); err != nil {
			t.Fatal(err)
		}
		got, err = evalExpr("(ls)", eval, env)
		if err != nil {
			t.Fatalf("ls: %v", err)
		}
		if entries, ok := got.([]Value); !ok || len(entries) != 2 {
			t.Errorf("ls should hide dotfiles, got %v", got)
		}
		got, err = evalExpr("(ls :all)", eval, env)
		if err != nil {
			t.Fatalf("ls :all: %v", err)
		}
		if entries, ok := got.([]Value); !ok || len(entries) != 3 {
			t.Errorf("ls :all should include dotfiles, got %v", got)
		}
		if _, err := evalExpr("(ls :bogus)", eval, env); err == nil {
			t.Error("ls should reject unknown keyword options")
		}

		// ls on a file path lists that entry, like ls(1)
		got, err = evalExpr("(ls \"f.txt\")", eval, env)
		if err != nil {
			t.Fatalf("ls file: %v", err)
		}
		if entries, ok := got.([]Value); !ok || len(entries) != 1 {
			t.Fatalf("ls file: expected 1 entry, got %v", got)
		} else if entries[0].(Dictionary)["name"] != String("f.txt") {
			t.Errorf("ls file entry wrong: %v", entries[0])
		}

		// Inspect with get / keys / scheme composition
		checks := []struct {
			expr   string
			expect Value
		}{
			{"(get \"size\" (stat \"f.txt\"))", Integer(5)},
			{"(get :type (stat \"f.txt\"))", String("file")},
			{"(get 1 (list 10 20 30))", Integer(20)},
			{"(length (filter (lambda (e) (string=? (get \"type\" e) \"file\")) (ls)))", Integer(1)},
			{"(if (member \"size\" (keys (stat \"f.txt\"))) #t #f)", true},
		}
		for _, tc := range checks {
			got, err := evalExpr(tc.expr, eval, env)
			if err != nil {
				t.Errorf("unexpected error for %q: %v", tc.expr, err)
				continue
			}
			if !deepEqual(got, tc.expect) {
				t.Errorf("for %q, got %v, want %v", tc.expr, got, tc.expect)
			}
		}
	})
}

func TestShellGrep(t *testing.T) {
	withTempDir(t, func(dir string, eval *Evaluator, env *Environment) {
		files := map[string]string{
			"notes.txt": "alpha line\nbeta line\ngamma\n",
			"test_a.go": "x",
			"test_b.go": "y",
			"other.go":  "z",
		}
		for name, content := range files {
			if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0644); err != nil {
				t.Fatal(err)
			}
		}

		tests := []struct {
			expr      string
			expect    Value
			shouldErr bool
		}{
			// stream input (cat) filters lazily; materializes as text
			{`(grep "line" (cat "notes.txt"))`, String("alpha line\nbeta line\n"), false},
			{`(grep "nothing" (cat "notes.txt"))`, String(""), false},
			// a bare string names a file
			{`(grep "line" "notes.txt")`, String("alpha line\nbeta line\n"), false},
			// list of strings
			{`(grep "^test" (glob "*.go"))`, []Value{String("test_a.go"), String("test_b.go")}, false},
			// literal text goes through lines
			{`(grep "b" (lines "alpha\nbeta"))`, []Value{String("beta")}, false},
			// regexp syntax works
			{`(grep "gam+a" (cat "notes.txt"))`, String("gamma\n"), false},
			// errors
			{`(grep "[" (cat "notes.txt"))`, nil, true},
			{`(grep "x")`, nil, true},
			{`(grep 1 (ls))`, nil, true},
			{`(grep "x" 5)`, nil, true},
			{`(grep "x" "no-such-file.txt")`, nil, true},
		}
		for _, tc := range tests {
			got, err := evalExpr(tc.expr, eval, env)
			if tc.shouldErr {
				if err == nil {
					t.Errorf("expected error for %q, got %v", tc.expr, got)
				}
				continue
			}
			if err != nil {
				t.Errorf("unexpected error for %q: %v", tc.expr, err)
				continue
			}
			if !deepEqual(got, tc.expect) {
				t.Errorf("for %q, got %v, want %v", tc.expr, got, tc.expect)
			}
		}

		// list of dictionaries: a row is kept when any value matches
		got, err := evalExpr(`(grep "test" (ls))`, eval, env)
		if err != nil {
			t.Fatalf("grep over ls: %v", err)
		}
		entries, ok := got.([]Value)
		if !ok || len(entries) != 2 {
			t.Fatalf("grep over ls: expected 2 rows, got %v", got)
		}
		for _, e := range entries {
			dict := e.(Dictionary)
			name := string(dict["name"].(String))
			if !strings.HasPrefix(name, "test_") {
				t.Errorf("unexpected row %s", name)
			}
			// matched rows carry the pattern for render-time highlighting
			if dict["_match"] != String("test") {
				t.Errorf("row %s missing _match: %v", name, dict)
			}
		}
	})
}

func TestShellGlobRecursiveAndWc(t *testing.T) {
	withTempDir(t, func(dir string, eval *Evaluator, env *Environment) {
		files := map[string]string{
			"main.go":             "package main\n",
			"cmd/shell.go":        "package cmd\n// two\n",
			"cmd/sub/deep.go":     "package sub\n",
			"docs/readme.md":      "hi\n",
			".hidden/vendored.go": "package hidden\n",
		}
		for name, content := range files {
			path := filepath.Join(dir, name)
			if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte(content), 0644); err != nil {
				t.Fatal(err)
			}
		}

		expectGlob := func(expr string, want ...string) {
			t.Helper()
			got, err := evalExpr(expr, eval, env)
			if err != nil {
				t.Errorf("%s: %v", expr, err)
				return
			}
			list, ok := got.([]Value)
			if !ok {
				t.Errorf("%s: not a list: %v", expr, got)
				return
			}
			names := make([]string, len(list))
			for i, v := range list {
				names[i] = string(v.(String))
			}
			sort.Strings(names)
			sort.Strings(want)
			if strings.Join(names, ",") != strings.Join(want, ",") {
				t.Errorf("%s = %v, want %v", expr, names, want)
			}
		}

		expectGlob(`(glob "**/*.go")`, "main.go", "cmd/shell.go", "cmd/sub/deep.go")
		expectGlob(`(glob "**/*.go" :all)`, "main.go", "cmd/shell.go", "cmd/sub/deep.go", ".hidden/vendored.go")
		expectGlob(`(glob "cmd/**/*.go")`, "cmd/shell.go", "cmd/sub/deep.go")
		// single-star patterns keep filepath.Glob semantics
		expectGlob(`(glob "*.go")`, "main.go")
		if _, err := evalExpr(`(glob "**/*.go" :bogus)`, eval, env); err == nil {
			t.Error("glob should reject unknown keyword options")
		}

		// wc returns line/word/byte counts as a dictionary
		checks := []struct {
			expr   string
			expect Value
		}{
			{`(get :lines (wc "cmd/shell.go"))`, Integer(2)},
			{`(get :words (wc "cmd/shell.go"))`, Integer(4)},
			{`(get :bytes (wc "cmd/shell.go"))`, Integer(19)},
			// with escape decoding, string-split on "\n" counts lines
			{`(length (string-split "l1\nl2\nl3" "\n"))`, Integer(3)},
			{`(fold-left + 0 (map (lambda (f) (get :lines (wc f))) (glob "**/*.go")))`, Integer(4)},
		}
		for _, tc := range checks {
			got, err := evalExpr(tc.expr, eval, env)
			if err != nil {
				t.Errorf("unexpected error for %q: %v", tc.expr, err)
				continue
			}
			if !deepEqual(got, tc.expect) {
				t.Errorf("for %q, got %v, want %v", tc.expr, got, tc.expect)
			}
		}
		if _, err := evalExpr(`(wc "missing.go")`, eval, env); err == nil {
			t.Error("wc on a missing file should error")
		}
	})
}

func TestApprovalGate(t *testing.T) {
	withTempDir(t, func(dir string, eval *Evaluator, env *Environment) {
		mustWrite := func(name string) {
			t.Helper()
			if err := os.WriteFile(filepath.Join(dir, name), []byte("x"), 0644); err != nil {
				t.Fatal(err)
			}
		}
		mustWrite("a.txt")

		// Denying approver blocks rm and mv, and reports the action
		var asked []string
		eval.SetApprover(func(action string) bool {
			asked = append(asked, action)
			return false
		})
		if _, err := evalExpr(`(rm "a.txt")`, eval, env); err == nil || !strings.Contains(err.Error(), "denied") {
			t.Errorf("rm should be denied, got err=%v", err)
		}
		if _, err := evalExpr(`(mv "a.txt" "b.txt")`, eval, env); err == nil || !strings.Contains(err.Error(), "denied") {
			t.Errorf("mv should be denied, got err=%v", err)
		}
		if _, err := evalExpr(`(sh "rm a.txt")`, eval, env); err == nil || !strings.Contains(err.Error(), "denied") {
			t.Errorf("sh should be denied, got err=%v", err)
		}
		if got, _ := evalExpr(`(exists? "a.txt")`, eval, env); got != true {
			t.Error("a.txt should still exist after denied rm/mv/sh")
		}
		if len(asked) != 3 || !strings.Contains(asked[0], "rm") || !strings.Contains(asked[1], "mv") || !strings.Contains(asked[2], "sh") {
			t.Errorf("approver saw wrong actions: %v", asked)
		}

		// Approving lets them through; non-destructive builtins never ask
		eval.SetApprover(func(action string) bool { return true })
		if _, err := evalExpr(`(mv "a.txt" "b.txt")`, eval, env); err != nil {
			t.Errorf("approved mv failed: %v", err)
		}
		if _, err := evalExpr(`(rm "b.txt")`, eval, env); err != nil {
			t.Errorf("approved rm failed: %v", err)
		}

		// Removing the approver ungates
		eval.SetApprover(nil)
		mustWrite("c.txt")
		if _, err := evalExpr(`(rm "c.txt")`, eval, env); err != nil {
			t.Errorf("ungated rm failed: %v", err)
		}
	})
}

func TestShellShAndEnv(t *testing.T) {
	withTempDir(t, func(dir string, eval *Evaluator, env *Environment) {
		// :full keeps the eager dictionary with no exit check
		got, err := evalExpr("(sh \"echo hi; echo err >&2; exit 3\" :full)", eval, env)
		if err != nil {
			t.Fatalf("sh: %v", err)
		}
		dict, ok := got.(Dictionary)
		if !ok {
			t.Fatalf("sh :full did not return a dictionary: %v", got)
		}
		if dict["stdout"] != String("hi") || dict["stderr"] != String("err") || dict["exit"] != Integer(3) {
			t.Errorf("sh result wrong: %v", dict)
		}

		// the default is a line stream of stdout
		got, err = evalExpr("(sh \"echo one; echo two\")", eval, env)
		if err != nil {
			t.Fatalf("sh stream: %v", err)
		}
		if got != String("one\ntwo\n") {
			t.Errorf("sh stream: got %v", got)
		}

		// a non-zero exit surfaces at end-of-stream, stderr included
		_, err = evalExpr("(sh \"echo err >&2; exit 3\")", eval, env)
		if err == nil || !strings.Contains(err.Error(), "exit 3") || !strings.Contains(err.Error(), "err") {
			t.Errorf("sh stream exit error: got %v", err)
		}

		if _, err := evalExpr("(set-env \"RF_TEST_VAR\" \"42\")", eval, env); err != nil {
			t.Fatal(err)
		}
		got, err = evalExpr("(env \"RF_TEST_VAR\")", eval, env)
		if err != nil || got != String("42") {
			t.Errorf("env lookup: got %v err %v", got, err)
		}
		got, _ = evalExpr("(env \"RF_TEST_VAR_MISSING\")", eval, env)
		if got != false {
			t.Errorf("missing env should be #f, got %v", got)
		}

		// zero-arg env lists {:name :value} rows sorted by name
		got, err = evalExpr("(env)", eval, env)
		if err != nil {
			t.Fatal(err)
		}
		rows, ok := got.([]Value)
		if !ok || len(rows) == 0 {
			t.Fatalf("env rows: got %v", got)
		}
		prev := ""
		found := false
		for _, r := range rows {
			dict, isDict := r.(Dictionary)
			if !isDict {
				t.Fatalf("env row is not a dictionary: %v", r)
			}
			name, _ := dict["name"].(String)
			if string(name) < prev {
				t.Errorf("env rows unsorted: %q after %q", name, prev)
			}
			prev = string(name)
			if name == "RF_TEST_VAR" && dict["value"] == String("42") {
				found = true
			}
		}
		if !found {
			t.Error("env rows missing RF_TEST_VAR=42")
		}

		// :redact masks credential-looking names; plain names pass through
		if _, err := evalExpr("(set-env \"RF_TEST_API_KEY\" \"hunter2\")", eval, env); err != nil {
			t.Fatal(err)
		}
		envValue := func(rows Value, name string) Value {
			for _, r := range rows.([]Value) {
				if dict, ok := r.(Dictionary); ok && dict["name"] == String(name) {
					return dict["value"]
				}
			}
			return nil
		}
		got, err = evalExpr("(env :redact)", eval, env)
		if err != nil {
			t.Fatal(err)
		}
		if v := envValue(got, "RF_TEST_API_KEY"); v != String("[redacted]") {
			t.Errorf("redacted listing: RF_TEST_API_KEY = %v", v)
		}
		if v := envValue(got, "RF_TEST_VAR"); v != String("42") {
			t.Errorf("redacted listing: RF_TEST_VAR = %v", v)
		}
		got, err = evalExpr("(env \"RF_TEST_API_KEY\" :redact)", eval, env)
		if err != nil || got != String("[redacted]") {
			t.Errorf("redacted lookup: got %v err %v", got, err)
		}
		if _, err := evalExpr("(env :bogus)", eval, env); err == nil || !strings.Contains(err.Error(), ":redact") {
			t.Errorf("unknown env option should error: %v", err)
		}

		if _, err := evalExpr("(define (peek) (env \"RF_TEST_API_KEY\"))", eval, env); err != nil {
			t.Fatal(err)
		}
		eval.SetCaller(CallerAssistant)
		got, err = evalExpr("(env)", eval, env)
		if err != nil {
			t.Fatal(err)
		}
		if v := envValue(got, "RF_TEST_API_KEY"); v != String("[redacted]") {
			t.Errorf("assistant listing: RF_TEST_API_KEY = %v", v)
		}
		if v := envValue(got, "RF_TEST_VAR"); v != String("42") {
			t.Errorf("assistant listing: RF_TEST_VAR = %v", v)
		}
		got, err = evalExpr("(peek)", eval, env)
		if err != nil || got != String("[redacted]") {
			t.Errorf("assistant lookup via wrapper: got %v err %v", got, err)
		}
		eval.SetCaller(CallerUser)
		got, err = evalExpr("(peek)", eval, env)
		if err != nil || got != String("hunter2") {
			t.Errorf("user lookup: got %v err %v", got, err)
		}

		got, err = evalExpr("(which \"sh\")", eval, env)
		if err != nil {
			t.Fatal(err)
		}
		if s, ok := got.(String); !ok || !strings.HasSuffix(string(s), "/sh") {
			t.Errorf("which sh: got %v", got)
		}
	})
}

func TestPathBuiltins(t *testing.T) {
	withTempDir(t, func(dir string, eval *Evaluator, env *Environment) {
		home := filepath.Join(dir, "home")
		t.Setenv("HOME", home)
		t.Setenv("PATH", "/usr/bin:/bin")

		pathEntries := func(v Value) []string {
			rows, ok := v.([]Value)
			if !ok {
				t.Fatalf("expected {:path} rows, got %v", v)
			}
			entries := make([]string, len(rows))
			for i, r := range rows {
				dict, ok := r.(Dictionary)
				if !ok {
					t.Fatalf("row is not a dictionary: %v", r)
				}
				entries[i] = string(dict["path"].(String))
			}
			return entries
		}

		got, err := evalExpr("(paths)", eval, env)
		if err != nil {
			t.Fatal(err)
		}
		if entries := pathEntries(got); strings.Join(entries, ":") != "/usr/bin:/bin" {
			t.Errorf("paths: got %v", entries)
		}

		// add-path prepends in the given order, expanding ~
		got, err = evalExpr("(add-path \"~/scripts\" \"/opt/x/bin\")", eval, env)
		if err != nil {
			t.Fatal(err)
		}
		want := home + "/scripts:/opt/x/bin:/usr/bin:/bin"
		if entries := pathEntries(got); strings.Join(entries, ":") != want {
			t.Errorf("add-path: got %v want %v", entries, want)
		}
		if os.Getenv("PATH") != want {
			t.Errorf("add-path env: got %q want %q", os.Getenv("PATH"), want)
		}

		// idempotent: re-adding (even with a trailing slash) changes nothing
		if _, err := evalExpr("(add-path \"~/scripts\" \"/opt/x/bin/\")", eval, env); err != nil {
			t.Fatal(err)
		}
		if os.Getenv("PATH") != want {
			t.Errorf("add-path re-add: got %q want %q", os.Getenv("PATH"), want)
		}

		// a mixed call prepends only the missing entry
		if _, err := evalExpr("(add-path \"/usr/bin\" \"/new/bin\")", eval, env); err != nil {
			t.Fatal(err)
		}
		want = "/new/bin:" + want
		if os.Getenv("PATH") != want {
			t.Errorf("add-path mixed: got %q want %q", os.Getenv("PATH"), want)
		}

		// remove-path drops entries, matching through ~ and trailing slashes
		if _, err := evalExpr("(remove-path \"~/scripts/\" \"/new/bin\")", eval, env); err != nil {
			t.Fatal(err)
		}
		if got := os.Getenv("PATH"); got != "/opt/x/bin:/usr/bin:/bin" {
			t.Errorf("remove-path: got %q", got)
		}

		// arity and type errors
		if _, err := evalExpr("(add-path)", eval, env); err == nil {
			t.Error("add-path with no args should error")
		}
		if _, err := evalExpr("(add-path 42)", eval, env); err == nil {
			t.Error("add-path with a number should error")
		}
	})
}

func TestFormatTable(t *testing.T) {
	rows := []Value{
		Dictionary{"name": String("a.txt"), "type": String("file"), "size": Integer(5)},
		Dictionary{"name": String("subdir"), "type": String("dir"), "size": Integer(96)},
	}
	out, ok := FormatTable(rows)
	if !ok {
		t.Fatal("FormatTable rejected list of dicts")
	}
	lines := strings.Split(out, "\n")
	if len(lines) != 3 {
		t.Fatalf("expected header + 2 rows, got %d lines: %q", len(lines), out)
	}
	if !strings.HasPrefix(lines[0], "name") || !strings.Contains(lines[0], "type") || !strings.Contains(lines[0], "size") {
		t.Errorf("header wrong: %q", lines[0])
	}
	if !strings.Contains(lines[1], "a.txt") || !strings.Contains(lines[2], "subdir") {
		t.Errorf("rows wrong: %q", out)
	}

	marked := []Value{Dictionary{"name": String("data.db"), "type": String("file"), "_match": String("data")}}
	out, ok = FormatTable(marked)
	if !ok || strings.Contains(out, "_match") {
		t.Errorf("_match should not render as a column: %q", out)
	}
	colored, ok := FormatTableColor(marked)
	if !ok || !strings.Contains(colored, "\x1b[1;31mdata\x1b[0m") {
		t.Errorf("expected highlighted match in %q", colored)
	}

	wide := []Value{
		Dictionary{"text": String("pi — agent"), "score": Integer(1)},
		Dictionary{"text": String("plain ascii"), "score": Integer(2)},
	}
	out, ok = FormatTable(wide)
	if !ok {
		t.Fatal("FormatTable rejected list of dicts")
	}
	lines = strings.Split(out, "\n")
	if a, b := len([]rune(lines[1])), len([]rune(lines[2])); a != b {
		t.Errorf("rows misaligned: widths %d vs %d in %q", a, b, out)
	}

	// Non-tabular values are rejected
	if _, ok := FormatTable(String("hi")); ok {
		t.Error("FormatTable should reject strings")
	}
	if _, ok := FormatTable([]Value{Integer(1)}); ok {
		t.Error("FormatTable should reject lists of non-dicts")
	}

	// table builtin returns the same as a string
	eval := NewEvaluator()
	got, err := evalExpr("(table (list {:name \"x\" :size 1}))", eval, eval.globalEnv)
	if err != nil {
		t.Fatalf("table builtin: %v", err)
	}
	if s, ok := got.(String); !ok || !strings.Contains(string(s), "x") {
		t.Errorf("table builtin output: %v", got)
	}
}

func TestFormatTableMultilineCells(t *testing.T) {
	rows := []Value{
		Dictionary{"text": String("para one line.\n\npara two line.\n"), "start": Integer(0), "end": Integer(31)},
		Dictionary{"text": String("short"), "start": Integer(31), "end": Integer(36)},
	}
	out, ok := FormatTableWidth(rows, 80)
	if !ok {
		t.Fatal("FormatTableWidth rejected list of dicts")
	}
	lines := strings.Split(out, "\n")
	if len(lines) != 5 {
		t.Fatalf("expected 5 lines, got %d:\n%s", len(lines), out)
	}
	for i, line := range lines {
		if a, b := len([]rune(line)), len([]rune(lines[0])); a != b {
			t.Errorf("line %d misaligned: width %d vs header %d in:\n%s", i, a, b, out)
		}
	}
	if !strings.Contains(lines[1], "para one line.") || !strings.Contains(lines[3], "para two line.") {
		t.Errorf("paragraphs should land on their own continuation lines:\n%s", out)
	}
	if !strings.Contains(lines[1], "0") || strings.Contains(lines[3], "31") {
		t.Errorf("row values should not repeat on continuation lines:\n%s", out)
	}
}

func TestExecBuiltin(t *testing.T) {
	ev := NewEvaluator()
	env := ev.globalEnv

	// exit 0 -> nil (no result echo in the REPL)
	got, err := evalExpr(`(exec "true")`, ev, env)
	if err != nil || got != nil {
		t.Errorf("(exec \"true\") = %v, %v; want nil, nil", got, err)
	}

	// non-zero exit is an error naming the status
	if _, err := evalExpr(`(exec "false")`, ev, env); err == nil || !strings.Contains(err.Error(), "exited with status 1") {
		t.Errorf("(exec \"false\") error = %v, want exit-status error", err)
	}

	// argument validation
	if _, err := evalExpr(`(exec)`, ev, env); err == nil {
		t.Error("(exec) should error")
	}
	if _, err := evalExpr(`(exec (list 1))`, ev, env); err == nil || !strings.Contains(err.Error(), "string arguments") {
		t.Errorf("(exec '(1)) error = %v, want string-arguments error", err)
	}

	// user-only: the assistant caller is rejected, even without an approver
	ev.SetCaller(CallerAssistant)
	if _, err := evalExpr(`(exec "true")`, ev, env); err == nil || !strings.Contains(err.Error(), "only be run by the user") {
		t.Errorf("assistant (exec ...) error = %v, want RequireUser rejection", err)
	}
	ev.SetCaller(CallerUser)
}

func TestSleepBuiltin(t *testing.T) {
	ev := NewEvaluator()
	env := ev.globalEnv

	start := time.Now()
	got, err := evalExpr("(sleep 0.02)", ev, env)
	if err != nil || got != nil {
		t.Errorf("(sleep 0.02) = %v, %v; want nil, nil", got, err)
	}
	if elapsed := time.Since(start); elapsed < 20*time.Millisecond {
		t.Errorf("sleep returned after %v, want >= 20ms", elapsed)
	}

	for _, expr := range []string{"(sleep)", "(sleep -1)", "(sleep 301)", `(sleep "x")`} {
		if _, err := evalExpr(expr, ev, env); err == nil {
			t.Errorf("%s should error", expr)
		}
	}

	// approval-gated: a denied sleep never blocks
	ev.SetApprover(func(action string) bool { return false })
	start = time.Now()
	if _, err := evalExpr("(sleep 5)", ev, env); err == nil || !strings.Contains(err.Error(), "denied") {
		t.Errorf("gated sleep error = %v, want denial", err)
	}
	if time.Since(start) > time.Second {
		t.Error("denied sleep should return immediately")
	}
	ev.SetApprover(nil)
}

func TestCatRange(t *testing.T) {
	withTempDir(t, func(dir string, eval *Evaluator, env *Environment) {
		if _, err := evalExpr(`(write-file "f.txt" "l1\nl2\nl3\nl4\nl5")`, eval, env); err != nil {
			t.Fatal(err)
		}

		got, err := evalExpr(`(cat "f.txt" {:from 2 :to 4})`, eval, env)
		if err != nil {
			t.Fatal(err)
		}
		if s, ok := got.(String); !ok || string(s) != "l2\nl3\nl4\n" {
			t.Errorf("range: got %#v", got)
		}

		got, err = evalExpr(`(cat "f.txt" {:from 4})`, eval, env)
		if err != nil {
			t.Fatal(err)
		}
		if s, ok := got.(String); !ok || string(s) != "l4\nl5\n" {
			t.Errorf("open-ended from: got %#v", got)
		}

		got, err = evalExpr(`(cat "f.txt" {:from 2 :to 3 :line-numbers #t})`, eval, env)
		if err != nil {
			t.Fatal(err)
		}
		rows, ok := got.([]Value)
		if !ok || len(rows) != 2 {
			t.Fatalf("numbered range: got %#v", got)
		}
		first := rows[0].(Dictionary)
		if first["line"] != Integer(2) || first["text"] != String("l2") {
			t.Errorf("numbered row: %v", first)
		}

		if _, err := evalExpr(`(cat "f.txt" {:from 3 :to 2})`, eval, env); err == nil {
			t.Error("inverted range should error")
		}
		if _, err := evalExpr(`(cat "f.txt" {:from 0})`, eval, env); err == nil {
			t.Error(":from 0 should error (lines are 1-based)")
		}

		// Unoptioned cat unchanged.
		got, err = evalExpr(`(cat "f.txt")`, eval, env)
		if err != nil {
			t.Fatal(err)
		}
		if s, ok := got.(String); !ok || string(s) != "l1\nl2\nl3\nl4\nl5\n" {
			t.Errorf("plain cat changed: %#v", got)
		}
	})
}

func TestShTimeout(t *testing.T) {
	withTempDir(t, func(dir string, eval *Evaluator, env *Environment) {
		start := time.Now()
		_, err := evalExpr(`(sh "sleep 5" {:timeout 0.2})`, eval, env)
		if err == nil || !strings.Contains(err.Error(), "timed out") {
			t.Errorf("stream timeout: got %v", err)
		}
		if time.Since(start) > 4*time.Second {
			t.Error("timeout did not cut the run short")
		}

		_, err = evalExpr(`(sh "sleep 5" {:timeout 0.2 :full #t})`, eval, env)
		if err == nil || !strings.Contains(err.Error(), "timed out") {
			t.Errorf("full timeout: got %v", err)
		}

		got, err := evalExpr(`(sh "echo ok" {:timeout 5})`, eval, env)
		if err != nil {
			t.Fatal(err)
		}
		if s, ok := got.(String); !ok || string(s) != "ok\n" {
			t.Errorf("fast command under timeout: %#v", got)
		}

		if _, err := evalExpr(`(write-file "one.txt" "x")`, eval, env); err != nil {
			t.Fatal(err)
		}
		got, err = evalExpr(`(pipe (wc "one.txt") (sh "wc -l"))`, eval, env)
		if err != nil {
			t.Fatalf("dict stdin through sh: %v", err)
		}
		if s, ok := got.(String); !ok || strings.TrimSpace(string(s)) != "1" {
			t.Errorf("dict stdin: got %#v", got)
		}

		if _, err := evalExpr(`(sh "echo hi" {:timeout "soon"})`, eval, env); err == nil {
			t.Error("bad :timeout value should error")
		}
	})
}
