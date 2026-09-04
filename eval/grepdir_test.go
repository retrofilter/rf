package eval

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeTree(t testing.TB, dir string, files map[string]string) {
	t.Helper()
	for rel, content := range files {
		path := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", rel, err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatalf("write %s: %v", rel, err)
		}
	}
}

func TestGrepDirectory(t *testing.T) {
	withTempDir(t, func(dir string, eval *Evaluator, env *Environment) {
		writeTree(t, dir, map[string]string{
			"a.txt":            "alpha match\nplain\n",
			"sub/b.txt":        "no\nbeta match\n",
			".gitignore":       "ignored.txt\nbuild/\n",
			"ignored.txt":      "match\n",
			"build/c.txt":      "match\n",
			".hidden.txt":      "match\n",
			".hiddendir/d.txt": "match\n",
			"bin.dat":          "match\x00binary\n",
			"sub/.gitignore":   "local.txt\n",
			"sub/local.txt":    "match\n",
			".git/config":      "match\n",
		})

		got, err := evalExpr(`(grep "match" ".")`, eval, env)
		if err != nil {
			t.Fatalf("grep dir: %v", err)
		}
		rows, ok := got.([]Value)
		if !ok {
			t.Fatalf("expected a list of rows, got %T", got)
		}
		var summary []string
		for _, r := range rows {
			d, ok := r.(Dictionary)
			if !ok {
				t.Fatalf("expected dictionary rows, got %T", r)
			}
			if d["_match"] != String("match") {
				t.Errorf("row missing _match mark: %v", d)
			}
			summary = append(summary, fmt.Sprintf("%s:%v:%s", d["file"], d["line"], d["text"]))
		}
		want := []string{
			"a.txt:1:alpha match",
			"sub/b.txt:2:beta match",
		}
		if strings.Join(summary, "|") != strings.Join(want, "|") {
			t.Errorf("got rows %v, want %v", summary, want)
		}
	})
}

func TestGrepDirectoryLargeFile(t *testing.T) {
	withTempDir(t, func(dir string, eval *Evaluator, env *Environment) {
		var sb strings.Builder
		for sb.Len() < grepBufSize+4096 {
			sb.WriteString("filler line of no particular interest\n")
		}
		sb.WriteString("the needle line\n")
		writeTree(t, dir, map[string]string{"big/log.txt": sb.String()})

		got, err := evalExpr(`(grep "needle" "big")`, eval, env)
		if err != nil {
			t.Fatalf("grep dir: %v", err)
		}
		rows, _ := got.([]Value)
		if len(rows) != 1 {
			t.Fatalf("expected 1 row, got %v", got)
		}
		d := rows[0].(Dictionary)
		if d["file"] != String("big/log.txt") || d["text"] != String("the needle line") {
			t.Errorf("unexpected row: %v", d)
		}
	})
}

func TestGrepDirectoryEarlyTake(t *testing.T) {
	withTempDir(t, func(dir string, eval *Evaluator, env *Environment) {
		files := map[string]string{}
		for i := 0; i < 50; i++ {
			files[fmt.Sprintf("f%02d.txt", i)] = "match here\n"
		}
		writeTree(t, dir, files)

		got, err := evalExpr(`(take 1 (grep "match" "."))`, eval, env)
		if err != nil {
			t.Fatalf("take over grep dir: %v", err)
		}
		rows, _ := got.([]Value)
		if len(rows) != 1 {
			t.Fatalf("expected 1 row, got %v", got)
		}
	})
}

func benchGrepDirTree(b *testing.B, dir string) int {
	files := map[string]string{
		".gitignore": "vendor/\n",
	}
	matches := 0
	var sb strings.Builder
	for f := 0; f < 200; f++ {
		sb.Reset()
		for l := 0; l < 100; l++ {
			if f%10 == 3 && l == 57 {
				sb.WriteString("worker: ERR_TIMEOUT after 1500ms retrying\n")
				matches++
			} else {
				fmt.Fprintf(&sb, "worker %d: request served path=/api/v1/items/%d status=200\n", l, f*100+l)
			}
		}
		files[fmt.Sprintf("pkg%d/sub%d/file%d.txt", f%8, f%3, f)] = sb.String()
	}
	for f := 0; f < 50; f++ {
		files[fmt.Sprintf("vendor/dep%d.txt", f)] = "ERR_TIMEOUT everywhere\n"
		files[fmt.Sprintf("pkg%d/blob%d.bin", f%8, f)] = "ERR_TIMEOUT\x00binary\n"
	}
	writeTree(b, dir, files)
	return matches
}

func benchmarkGrepDir(b *testing.B, workers int) {
	dir := b.TempDir()
	want := benchGrepDirTree(b, dir)
	m, err := compileGrepMatcher("ERR_TIMEOUT")
	if err != nil {
		b.Fatal(err)
	}
	prev := grepDirWorkers
	grepDirWorkers = workers
	defer func() { grepDirWorkers = prev }()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		s := dirGrepStream(m, "ERR_TIMEOUT", dir, grepCtxOpt{})
		n := 0
		for {
			_, ok, err := s.Next()
			if err != nil {
				b.Fatal(err)
			}
			if !ok {
				break
			}
			n++
		}
		if n != want {
			b.Fatalf("expected %d rows, got %d", want, n)
		}
	}
}

func BenchmarkGrepDir_1Worker(b *testing.B)  { benchmarkGrepDir(b, 1) }
func BenchmarkGrepDir_Parallel(b *testing.B) { benchmarkGrepDir(b, 0) }

func TestGrepContext(t *testing.T) {
	withTempDir(t, func(dir string, eval *Evaluator, env *Environment) {
		writeTree(t, dir, map[string]string{
			"f.txt": "one\ntwo\nhit a\nfour\nhit b\nsix\nseven\n",
		})

		summarize := func(v Value) []string {
			rows, ok := v.([]Value)
			if !ok {
				t.Fatalf("expected rows, got %T", v)
			}
			var out []string
			for _, r := range rows {
				d := r.(Dictionary)
				mark := ""
				if _, matched := d["_match"]; matched {
					mark = "*"
				}
				out = append(out, fmt.Sprintf("%v:%s%s", d["line"], d["text"], mark))
			}
			return out
		}

		got, err := evalExpr(`(grep "hit" "f.txt" {:before 1 :after 1})`, eval, env)
		if err != nil {
			t.Fatal(err)
		}
		want := []string{"2:two", "3:hit a*", "4:four", "5:hit b*", "6:six"}
		if strings.Join(summarize(got), "|") != strings.Join(want, "|") {
			t.Errorf("file context: got %v, want %v", summarize(got), want)
		}

		// :context sets both sides; the directory scan carries file names.
		got, err = evalExpr(`(grep "hit b" "." {:context 1})`, eval, env)
		if err != nil {
			t.Fatal(err)
		}
		rows := got.([]Value)
		var sum []string
		for _, r := range rows {
			d := r.(Dictionary)
			sum = append(sum, fmt.Sprintf("%s:%v:%s", d["file"], d["line"], d["text"]))
		}
		want = []string{"f.txt:4:four", "f.txt:5:hit b", "f.txt:6:six"}
		if strings.Join(sum, "|") != strings.Join(want, "|") {
			t.Errorf("dir context: got %v, want %v", sum, want)
		}

		// line-numbers alone: match rows only, numbered.
		got, err = evalExpr(`(grep "hit" "f.txt" {:line-numbers #t})`, eval, env)
		if err != nil {
			t.Fatal(err)
		}
		want = []string{"3:hit a*", "5:hit b*"}
		if strings.Join(summarize(got), "|") != strings.Join(want, "|") {
			t.Errorf("line-numbers: got %v, want %v", summarize(got), want)
		}

		// A stream input numbers the same way; a list of lines too.
		got, err = evalExpr(`(grep "hit" (cat "f.txt") {:after 1})`, eval, env)
		if err != nil {
			t.Fatal(err)
		}
		want = []string{"3:hit a*", "4:four", "5:hit b*", "6:six"}
		if strings.Join(summarize(got), "|") != strings.Join(want, "|") {
			t.Errorf("stream context: got %v, want %v", summarize(got), want)
		}
		got, err = evalExpr(`(grep "hit" (lines "one\ntwo\nhit a") {:before 1})`, eval, env)
		if err != nil {
			t.Fatal(err)
		}
		want = []string{"2:two", "3:hit a*"}
		if strings.Join(summarize(got), "|") != strings.Join(want, "|") {
			t.Errorf("list context: got %v, want %v", summarize(got), want)
		}

		// Context options need line-shaped input: dict rows refuse.
		if _, err := evalExpr(`(grep "x" (dir) {:context 1})`, eval, env); err == nil {
			t.Error("context over dict rows should error")
		}

		// Options untouched: plain grep still returns bare lines.
		got, err = evalExpr(`(grep "hit" "f.txt")`, eval, env)
		if err != nil {
			t.Fatal(err)
		}
		if s, ok := got.(String); !ok || string(s) != "hit a\nhit b\n" {
			t.Errorf("plain grep changed shape: %#v", got)
		}
	})
}

func TestGrepContextLargeFile(t *testing.T) {
	withTempDir(t, func(dir string, eval *Evaluator, env *Environment) {
		var sb strings.Builder
		for sb.Len() < grepBufSize+4096 {
			sb.WriteString("filler line of no particular interest\n")
		}
		sb.WriteString("before line\nthe needle line\nafter line\n")
		writeTree(t, dir, map[string]string{"big/log.txt": sb.String()})

		got, err := evalExpr(`(grep "needle" "." {:context 1})`, eval, env)
		if err != nil {
			t.Fatal(err)
		}
		rows := got.([]Value)
		if len(rows) != 3 {
			t.Fatalf("expected 3 rows, got %d: %v", len(rows), rows)
		}
		mid := rows[1].(Dictionary)
		if mid["text"] != String("the needle line") || mid["_match"] == nil {
			t.Errorf("middle row should be the marked match: %v", mid)
		}
		for _, i := range []int{0, 2} {
			if _, marked := rows[i].(Dictionary)["_match"]; marked {
				t.Errorf("context row %d must not carry _match", i)
			}
		}
	})
}
