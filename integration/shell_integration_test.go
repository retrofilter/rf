package integration

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

func TestSchemeEval(t *testing.T) {
	t.Parallel()
	s := startShell(t, t.TempDir())
	s.sendLine("(+ 1 2)")
	s.expect(`3`)
	s.sendLine("(sort (list 3 1 2))")
	s.expect(`\(1 2 3\)`)
	s.sendLine("`(x ,(+ 1 1))")
	s.expect(`\(x 2\)`)
}

func TestCommandModeFallthrough(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	s := startShell(t, dir)
	// echo has no builtin equivalent -> /bin/sh
	s.sendLine("echo shell-was-here > out.txt")
	s.sendLine("cat out.txt")
	s.expect(`shell-was-here`)
	// a ! prefix always runs the system binary, even for builtin names
	s.clear()
	s.sendLine("!ls -la")
	s.expect(`total `)
}

func TestHistoryExpansion(t *testing.T) {
	t.Parallel()
	s := startShell(t, t.TempDir())

	// No prior command: an error, like bash — and the line never runs.
	s.sendLine("!!")
	s.expect(`event not found`)

	// Whole-line recall: the expansion echoes, then reruns.
	s.clear()
	s.sendLine("echo first-marker")
	s.expect(`first-marker`)
	s.sendLine("!!")
	s.expect(`echo first-marker`)
	s.expect(`first-marker`)

	s.clear()
	s.sendLine("env echo second-marker")
	s.expect(`second-marker`)
	s.sendLine("time !!")
	s.expect(`env echo second-marker`)
	s.expect(`second-marker`)

	s.clear()
	s.sendLine("!!")
	s.expect(`second-marker`)
}

func TestFallthroughHint(t *testing.T) {
	t.Parallel()
	s := startShell(t, t.TempDir())

	s.sendLine("true && similar query")
	s.expect(`hint: similar is a builtin`)
	s.expect(`parens`)

	// A name that is nobody's builtin fails plain — no hint.
	s.clear()
	s.sendLine("rf-no-such-zzz > out.txt")
	s.expect(`not found`)
	s.expectNot(`hint:`)
}

func TestSchemeRedirects(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	s := startShell(t, dir)

	s.sendLine("dir > listing.tsv")
	s.expect(`\(~\) \$ `)
	s.sendLine("dir | pick name > names.tsv")
	s.expect(`\(~\) \$ `)
	s.sendLine("dir | pick name >> names.tsv")
	s.expect(`\(~\) \$ `)
	s.sendLine("cat names.tsv")
	s.expect(`listing\.tsv\s*\n.*listing\.tsv`)

	// < on the first stage: the file's lines are the stage's input.
	s.clear()
	s.sendLine("take 1 < names.tsv")
	s.expect(`listing\.tsv`)

	s.clear()
	s.sendLine("dir 2>&1")
	s.expect(`dir is a builtin with one output`)
	s.clear()
	s.sendLine("dir > mid.tsv | take 1")
	s.expect(`starve the pipe`)

	s.clear()
	s.sendLine("export BIG=100000")
	s.sendLine("dir | where size > $BIG")
	s.expect(`where compares with -gt`)
	if _, err := os.Stat(filepath.Join(dir, "100000")); !os.IsNotExist(err) {
		t.Fatalf("where's > leaked to a file: stat 100000 = %v", err)
	}

	// $VAR under a Scheme head is a value; $(...) is a precise error.
	s.clear()
	s.sendLine("export N=1")
	s.sendLine("dir | take $N | length")
	s.expect(`(?m)^1\s*$`)
	s.clear()
	s.sendLine("dir $(pwd)")
	s.expect(`command substitution isn't run`)
}

func TestNativeCoreutils(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ".hidden"), []byte("x"), 0644); err != nil {
		t.Fatal(err)
	}
	s := startShell(t, dir)

	// ls -la is /bin/ls -la, not the builtin table
	s.sendLine("ls -la")
	s.expect(`total `)
	s.expect(`\.hidden`)
	s.expectNot(`name\s+type\s+size`)

	s.clear()
	s.sendLine("dir -a")
	s.expect(`name\s+type\s+size`)
	s.expect(`\.hidden`)
	s.expectNot(`total `)

	s.clear()
	s.sendLine("dir -R")
	s.expect(`Error`)
	s.expect(`prefix the line with !`)
}

func TestBuiltinPipeline(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	for name, content := range map[string]string{
		"alpha.md":  "hello",
		"beta.md":   "hello",
		"gamma.txt": "hello",
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0644); err != nil {
			t.Fatal(err)
		}
	}
	s := startShell(t, dir)

	// ls and grep are unix words: the whole pipeline runs in /bin/sh
	s.sendLine("ls | grep md")
	s.expect(`alpha.md`)
	s.expect(`beta.md`)
	s.expectNot(`gamma.txt`)
	s.expectNot(`name\s+type\s+size`)

	// dir heads a structured pipeline: _ placeholder and string indexes
	s.clear()
	s.sendLine("dir | sort-by name | get 0 | get name")
	s.expect(`alpha.md`)

	// pipeline results land in *1 like any command-mode result
	s.clear()
	s.sendLine("(string-length *1)")
	s.expect(`8`)

	// length as a stage word: the structured `| wc -l`
	s.clear()
	s.sendLine("dir | length")
	s.expect(`3`)

	// take as a stage word trims rows, structured
	s.clear()
	s.sendLine("dir | take 2 | length")
	s.expect(`2`)
	s.clear()
	s.sendLine("head -1 alpha.md")
	s.expect(`hello`)
	s.clear()
	s.sendLine("ls | head -2")
	s.expect(`alpha.md`)

	// write-file as a stage word: external cat streams into the builtin
	s.clear()
	s.sendLine("cat gamma.txt | write-file copied.txt")
	s.sendLine("cat copied.txt")
	s.expect(`hello`)

	// a pipeline with no Scheme word at all goes to /bin/sh whole
	s.clear()
	s.sendLine("echo piped-through | tr a-z A-Z")
	s.expect(`PIPED-THROUGH`)

	s.clear()
	s.sendLine("dir | tr a-z A-Z")
	s.expect(`ALPHA\.MD`)

	// the explicit serializer is the same crossing, spelled out
	s.clear()
	s.sendLine("dir | text | tr a-z A-Z")
	s.expect(`ALPHA\.MD`)
}

func TestChunkCommand(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	para := func(w string) string { return strings.Repeat(w+" words in this paragraph. ", 4) }
	doc := "# Title\n\n" + para("alpha") + "\n\n## Section\n\n" + para("beta") + "\n"
	if err := os.WriteFile(filepath.Join(dir, "doc.md"), []byte(doc), 0644); err != nil {
		t.Fatal(err)
	}
	s := startShell(t, dir)

	// piped: cat streams into chunk, chunk rows render as a table
	s.sendLine("cat doc.md | chunk -s 140")
	s.expect(`text`)
	s.expect(`alpha words`)

	s.clear()
	s.sendLine("chunk doc.md --size 140 | length")
	s.expect(`2`)

	// :format markdown starts the second chunk at the section heading
	s.clear()
	s.sendLine("chunk doc.md --format markdown -s 140 | get 1 | get text")
	s.expect(`## Section`)
}

func TestRowVerbsPipeline(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	for name, content := range map[string]string{
		"small.txt": "abc",
		"big.txt":   strings.Repeat("x", 100),
		"items.csv": "name,price\npen,2\nbook,15\n",
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0644); err != nil {
			t.Fatal(err)
		}
	}
	s := startShell(t, dir)

	s.sendLine("dir | where size -gt 50 | pick name")
	s.expect(`big.txt`)
	s.expectNot(`small.txt`)

	// = filters on strings; get digs the row out
	s.clear()
	s.sendLine("dir | where name = small.txt | get 0 | get size")
	s.expect(`3`)

	// sort-by -r (--desc) puts the largest first
	s.clear()
	s.sendLine("dir | sort-by size -r | get 0 | get name")
	s.expect(`big.txt`)

	// count-by renders {type count} rows
	s.clear()
	s.sendLine("dir | count-by type")
	s.expect(`file\s+3`)

	// from-csv is a table source: numeric cells compare numerically
	s.clear()
	s.sendLine("from-csv items.csv | where price -ge 10 | pick name")
	s.expect(`book`)
	s.expectNot(`pen`)

	// standalone where is intercepted with the pipe hint
	s.clear()
	s.sendLine("where size -gt 100000")
	s.expect(`pipe rows in`)
}

func TestExternalHeadPipeline(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	s := startShell(t, dir)

	s.sendLine(`printf 'one\ntwo\nthree\n' | take 2`)
	s.expect(`one`)
	s.expect(`two`)

	s.clear()
	s.sendLine(`printf 'BANANA\nAPPLE\n' | tr A-Z a-z | take 1`)
	s.expect(`banana`)
	s.expectNot(`apple`)

	s.clear()
	s.sendLine(`printf 'aaa\nbbb\n' | take 1 | tr a-z A-Z`)
	s.expect(`AAA`)
	s.expectNot(`BBB`)

	s.clear()
	s.sendLine(`printf 'KEEP\nDROP\n' | tr A-Z a-z | grep ke`)
	s.expect(`keep`)
	s.expectNot(`drop`)
	s.clear()
	s.sendLine(`printf 'stay\ngone\n' | grep -v gone | tr a-z A-Z`)
	s.expect(`STAY`)
	s.expectNot(`GONE`)
	s.expectNot(`is a builtin`)

	s.clear()
	s.sendLine(`printf 'shout\n' | string-upcase`)
	s.expect(`SHOUT`)
	s.clear()
	s.sendLine(`printf 'first\nsecond\n' | reverse | take 1`)
	s.expect(`second`)
}

func TestGitLogPipeline(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "HOME="+dir,
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t",
			"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t",
			"GIT_CONFIG_NOSYSTEM=1")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	git("init", "-q")
	if err := os.WriteFile(filepath.Join(dir, "auth.go"), []byte("package auth"), 0644); err != nil {
		t.Fatal(err)
	}
	git("add", ".")
	git("commit", "-q", "-m", "add auth module")

	s := startShell(t, dir)
	s.sendLine(`git log --oneline | take 1`)
	s.expect(`add auth module`)
}

func TestBuiltinsCommand(t *testing.T) {
	t.Parallel()
	s := startShell(t, t.TempDir())
	s.sendLine("builtins | where name = write-file")
	s.expect(`name\s+type`)
	s.expect(`write-file\s+function`)
	s.clear()
	s.sendLine("builtins | where name = pipe")
	s.expect(`pipe\s+special form`)
	s.clear()
	s.sendLine("builtins | where name = configure")
	s.expect(`configure\s+function`)
	// rows cross to unix grep as TSV implicitly — no serializer needed
	s.clear()
	s.sendLine("builtins | grep '^pipe'")
	s.expect(`special form`)
}

func TestHelpAndFlags(t *testing.T) {
	t.Parallel()
	// help pages through $PAGER; cat keeps the PTY transcript linear.
	s := startShell(t, t.TempDir(), "PAGER=cat")

	s.sendLine("dir -h")
	s.expect(`usage:  dir \[path\] \[flags\]`)
	s.expect(`scheme: \(dir \[path\] \[\{:all #t\}\]\)`)
	s.expect(`-a, --all`)
	s.expect(`-h, --help`)

	s.clear()
	s.sendLine("help history")
	s.expect(`history — search command history`)
	s.expect(`-d, --dir PATH`)
	s.expect(`-n, --limit N`)

	// help --list is the pages as rows, composable like any table
	s.clear()
	s.sendLine("help --list | where page = similar")
	s.expect(`rank items`)

	// instruction words only answer a lone -h; longer tails stay prose
	s.clear()
	s.sendLine("agent -h")
	s.expect(`agent — run a sub-agent chat turn`)

	// unknown flags point at help and the ! escape
	s.clear()
	s.sendLine("dir -Z")
	s.expect(`doesn't take -Z`)
	s.expect(`prefix the line with !`)
}

func TestEnvCommand(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "marker.txt"), []byte("x"), 0644); err != nil {
		t.Fatal(err)
	}
	s := startShell(t, dir, "RF_TEST_SECRET_TOKEN=hunter2", "RF_TEST_PLAIN=visible")
	s.sendLine("env | grep RF_TEST_PLAIN=")
	s.expect(`RF_TEST_PLAIN=visible`)
	s.expectNot(`name\s+value`)
	s.clear()
	s.sendLine("env RF_TEST_FROM_ENV=1 ls")
	s.expect(`marker.txt`)
	s.clear()
	s.sendLine(`(env "RF_TEST_PLAIN")`)
	s.expect("visible")
	s.clear()
	s.sendLine(`(env "RF_TEST_SECRET_TOKEN" :redact)`)
	s.expect(`\[redacted\]`)
}

func TestNativeDispatch(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("hello-from-file"), 0644); err != nil {
		t.Fatal(err)
	}
	s := startShell(t, dir)

	// ls is /bin/ls: plain names, no table
	s.sendLine("ls")
	s.expect(`notes.txt`)
	s.expectNot(`name\s+type\s+size`)

	// cat prints raw contents
	s.clear()
	s.sendLine("cat notes.txt")
	s.expect(`hello-from-file`)
	s.expectNot(`"hello-from-file"`)

	// cp / rm run natively; the Scheme spellings return structured values
	s.sendLine("cp notes.txt copy.txt")
	s.clear()
	s.sendLine(`(get "size" (stat "copy.txt"))`)
	s.expect(`15`)
	s.sendLine("rm copy.txt")
	s.clear()
	s.sendLine("ls copy.txt")
	s.expect(`(?i)no such file`)

	// native rm keeps its own guardrails: a directory without -r errors
	s.sendLine("mkdir sub")
	s.sendLine("echo x > sub/f")
	s.clear()
	s.sendLine("rm sub")
	s.expect(`(?i)is a directory`)
	if _, err := os.Stat(filepath.Join(dir, "sub")); err != nil {
		t.Fatal("rm should not have removed non-empty directory")
	}
}

func TestGlobExpansion(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	for name, content := range map[string]string{
		"a.go": "alpha\n", "b.go": "beta\n", "note.txt": "note\n", ".dot.go": "dotfile\n",
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0644); err != nil {
			t.Fatal(err)
		}
	}
	s := startShell(t, dir)

	s.sendLine("dir *.go")
	s.expect(`name\s+type\s+size`)
	s.expect(`a.go\s+file`)
	s.expect(`b.go\s+file`)
	s.expectNot(`dotfile`)
	s.expectNot(`note.txt`)

	s.clear()
	s.sendLine("ls *.go")
	s.expect(`a.go`)
	s.expectNot(`name\s+type\s+size`)
	s.clear()
	s.sendLine("grep alpha *.go")
	s.expect(`a.go:alpha`)

	// an external head's expansion streams into a Scheme stage
	s.clear()
	s.sendLine("cat *.go | take 1")
	s.expect(`alpha`)
	s.expectNot(`beta`)

	s.clear()
	s.sendLine(`dir "*.go"`)
	s.expect(`Error`)

	// the Scheme spelling of the same expansion (ls stays bound)
	s.clear()
	s.sendLine(`(length (ls (glob "*.go")))`)
	s.expect(`2`)
}

func TestShellBuiltinsFromScheme(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	s := startShell(t, dir)

	s.sendLine(`(write-file "a.txt" "hello")`)
	s.expect(`5`)
	s.clear()
	s.sendLine(`(get "size" (stat "a.txt"))`)
	s.expect(`5`)
	s.clear()
	// streaming sh prints stdout as it arrives, then errors on the exit
	s.sendLine(`(sh "echo out; exit 3")`)
	s.expect(`out`)
	s.expect(`Error: sh: exit 3`)
	s.clear()
	// :full keeps the eager {:stdout :stderr :exit} dictionary
	s.sendLine(`(sh "echo out; exit 3" :full)`)
	s.expect(`stdout\s+stderr\s+exit`)
	s.expect(`out\s+`)
	s.clear()
	s.sendLine(`(filter (lambda (e) (string=? (get "type" e) "file")) (ls))`)
	s.expect(`a.txt`)
}

func TestStreams(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("alpha line\nbeta line\nomega\n"), 0644); err != nil {
		t.Fatal(err)
	}
	s := startShell(t, dir)

	// grep in command mode is system grep, muscle memory intact
	s.sendLine(`grep "line" notes.txt`)
	s.expect(`alpha line`)
	s.expect(`beta line`)
	s.expectNot(`omega`)

	s.clear()
	s.sendLine(`(pipe (sh "yes") (take 2))`)
	s.expect(`y\s+y`)
	s.sendLine("(+ 20 22)")
	s.expect(`42`)

	s.clear()
	s.sendLine(`yes | take 2`)
	s.expect(`y\s+y`)
	s.sendLine("(+ 20 21)")
	s.expect(`41`)

	// the Scheme sh builtin streams its stdout
	s.clear()
	s.sendLine(`(sh "echo streamed-hi")`)
	s.expect(`streamed-hi`)

	s.clear()
	s.sendLine(`(grep "x" "no-such-file.txt")`)
	s.expect(`Error`)
	s.expect(`lines`)
}

func TestFetchCommand(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "alpha\nbeta\n")
	}))
	defer srv.Close()
	s := startShell(t, t.TempDir())

	// fetch is a command word streaming the body
	s.sendLine(`fetch "` + srv.URL + `"`)
	s.expect(`alpha\s+beta`)

	s.clear()
	s.sendLine(`fetch "` + srv.URL + `" | grep bet`)
	s.expect(`beta`)
	s.expectNot(`alpha\s+beta`)
}

func TestModeToggle(t *testing.T) {
	t.Parallel()
	s := startShell(t, t.TempDir())
	// cwd == HOME, so the lispy prompt shows "(~) $ "
	s.expect(`\(~\) \$ `)
	s.clear()
	s.send("\x1b[Z") // Shift+Tab -> agent mode
	s.expect(`\(~\) ⏺ `)
	s.clear()
	s.send("\x1b[Z") // back to command mode
	s.expect(`\(~\) \$ `)
	// Ctrl+Space (NUL) is the primary toggle
	s.clear()
	s.send("\x00")
	s.expect(`\(~\) ⏺ `)
	s.clear()
	s.send("\x00")
	s.expect(`\(~\) \$ `)
	// still in command mode: shell commands work
	s.sendLine("pwd")
	s.expect(`/`)
}

func TestPromptGitBranch(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	// Fake checkouts: .git/HEAD is all the prompt reads (no git binary)
	writeHead := func(repo, ref string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Join(repo, ".git"), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(repo, ".git", "HEAD"), []byte(ref), 0644); err != nil {
			t.Fatal(err)
		}
	}
	writeHead(filepath.Join(dir, "repo"), "ref: refs/heads/trunk\n")
	// Directly under ~/src → a project, so its branch stays hidden
	writeHead(filepath.Join(dir, "src", "proj"), "ref: refs/heads/hidden\n")
	s := startShell(t, dir)

	s.sendLine("cd repo")
	s.expect(`\(~/repo@trunk\) \$ `)

	// The project prompt's paren right after the path asserts no @branch
	s.clear()
	s.sendLine("cd ../src/proj")
	s.expect(`\(proj ~/src/proj\) \$ `)
}

func TestConfigure(t *testing.T) {
	t.Parallel()
	s := startShell(t, t.TempDir())

	s.sendLine("configure")
	s.expect(`model provider`)
	s.send("\x1b")
	s.expect(`created ~/.rf.scm`)
	s.expect(`agent-allow-working-dir :read`)

	s.clear()
	s.sendLine("configure")
	s.expect(`model provider`)
	s.send("\x1b[A")
	s.send("\r")
	s.expect(`api key \[not set\]: `)
	s.sendLine("sk-test-not-real")
	s.expect(`model`)
	s.send("\x1b[B\x1b[B\x1b[B\x1b[B") // down to "other"
	s.send("\r")
	s.expect(`model id \[claude-sonnet-4-6\]: `)
	s.sendLine("claude-opus-4-8")
	s.expect(`extended thinking`)
	s.send("\r")
	s.expect(`install the claude hooks`)
	s.send("\x1b[B\r") // no
	s.expect(`configured: provider=anthropic model=claude-opus-4-8`)
	s.expect(`:id "claude-opus-4-8"`)
	s.expect(`:api-key \(env "RF_ANTHROPIC_API_KEY"\)`)

	s.clear()
	s.sendLine("(configure)")
	s.expect(`model provider`)
	s.send("\r")
	s.expect(`api key \[sk-t\.\.\.real\]: `)
	s.sendLine("")
	s.expect(`model`)
	s.send("\r")
	s.expect(`model id \[claude-opus-4-8\]: `)
	s.sendLine("")
	s.expect(`extended thinking`)
	s.send("\r")
	s.expect(`install the claude hooks`)
	s.send("\x1b[B\r") // no
	s.expect(`configured: provider=anthropic model=claude-opus-4-8`)

	// back to ollama (down + enter), accepting the url and model defaults
	s.clear()
	s.sendLine("configure")
	s.expect(`model provider`)
	s.send("\x1b[B")
	s.send("\r")
	s.expect(`ollama url \[http://localhost:11434\]: `)
	s.sendLine("http://127.0.0.1:1") // dead port: never a live host server
	s.expect(`model \[granite4:3b\]: `)
	s.sendLine("")
	s.expect(`install the claude hooks`)
	s.send("\x1b[B\r") // no
	s.expect(`configured: provider=ollama model=granite4:3b`)
}

func TestConfigurePersistsViaPrelude(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	s := startShell(t, dir)
	s.sendLine("configure")
	s.expect(`model provider`)
	s.send("\x1b[A")
	s.send("\r")
	s.expect(`api key \[not set\]: `)
	s.sendLine("sk-test-not-real")
	s.expect(`model`)
	s.send("\x1b[B\x1b[B\x1b[B\x1b[B")
	s.send("\r")
	s.expect(`model id \[claude-sonnet-4-6\]: `)
	s.sendLine("claude-opus-4-8")
	s.expect(`extended thinking`)
	s.send("\r")
	s.expect(`install the claude hooks`)
	s.send("\x1b[B\r") // no
	s.expect(`configured: provider=anthropic model=claude-opus-4-8`)
	s.close()

	prelude, err := os.ReadFile(filepath.Join(dir, ".rf.scm"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"(define default-model", `:provider "anthropic"`, `:id "claude-opus-4-8"`, "(define agent-allow-working-dir :read)"} {
		if !strings.Contains(string(prelude), want) {
			t.Fatalf("prelude lacks %q:\n%s", want, prelude)
		}
	}

	again := startShell(t, dir)
	again.sendLine("configure")
	again.expect(`model provider`)
	again.send("\r")
	again.expect(`api key \[sk-t\.\.\.real\]: `)
	again.sendLine("")
	again.expect(`model`)
	again.send("\x1b")
	again.expect(`\$ `)
}

func TestProviderFromModelId(t *testing.T) {
	t.Parallel()
	s := startShell(t, t.TempDir(), "ANTHROPIC_API_KEY=sk-vendor-key-rf-never-reads")
	s.sendLine(`(define default-model "claude-sonnet-4-6")`)
	s.sendLine("(get :context-window (usage))")
	s.expect(`1000000`)
	s.clear()

	// Saved settings replace the binding: switch to ollama
	s.sendLine("configure")
	s.expect(`model provider`)
	s.send("\x1b[B")
	s.send("\r")
	s.expect(`ollama url \[http://localhost:11434\]: `)
	s.sendLine("http://127.0.0.1:1")
	s.expect(`model \[granite4:3b\]: `)
	s.sendLine("")
	s.expect(`install the claude hooks`)
	s.send("\x1b[B\r") // no
	s.expect(`configured: provider=ollama model=granite4:3b`)
}

func TestMigrationsRunOnStartup(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	s := startShell(t, dir)
	s.sendLine("configure")
	s.expect(`model provider`)
	s.send("\r")
	s.expect(`ollama url`)
	s.sendLine("http://127.0.0.1:1")
	s.expect(`model \[granite4:3b\]`)
	s.sendLine("")
	s.expect(`install the claude hooks`)
	s.send("\x1b[B\r") // no
	s.expect(`configured: provider=ollama`)
	s.expectNot(`no such table`)
	// the database lives in $HOME (the test dir), not the cwd
	if _, err := os.Stat(filepath.Join(dir, ".rf", "main.db")); err != nil {
		t.Fatal(".rf/main.db was not created in $HOME")
	}
	_ = s
}

func TestProjectTrees(t *testing.T) {
	t.Parallel()
	s := startShell(t, t.TempDir())

	s.sendLine("project --init demo")
	s.expect(`\(demo ~/src/demo/main\) \$ `)

	s.clear()
	s.sendLine("create-tree feat")
	s.expect(`\(demo ~/src/demo/feat\) \$ `)

	s.clear()
	s.sendLine("trees")
	s.expect(`name\s+branch`)
	s.expect(`feat\s+feat`)
	s.expect(`main\s+main`)

	// tree switches back to an existing worktree
	s.clear()
	s.sendLine("tree main")
	s.expect(`\(demo ~/src/demo/main\) \$ `)

	s.clear()
	s.sendLine("projects")
	s.expect(`name\s+path\s+trees\s+tasks`)
	s.expect(`demo\s+.*src/demo\s+2\s+0`)

	s.clear()
	s.sendLine("delete-tree feat")
	s.expect(`true`)
	s.clear()
	s.sendLine("trees")
	s.expect(`main\s+main`)
	s.expectNot(`feat`)
}

func TestHistoryLoggingAndSearch(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	s := startShell(t, dir)

	s.sendLine("mkdir subdir")
	s.expect(`subdir`)
	s.sendLine("cd subdir")
	s.sendLine("pwd")
	s.sendLine("cd ..")

	s.clear()
	s.sendLine("history")
	s.expect(`when\s+dir\s+mode\s+exit\s+line`)
	s.expect(`command\s+0\s+pwd`)

	// history | grep is native grep over the rows' implicit TSV
	s.clear()
	s.sendLine("history | grep pwd")
	s.expect(`command\s+0\s+pwd`)
	s.expectNot(`mkdir`)

	s.sendLine("cd subdir")
	s.clear()
	s.sendLine("history -d .")
	s.expect(`command\s+0\s+pwd`)
	s.expectNot(`mkdir`)

	// --limit types through the desugared dict
	s.clear()
	s.sendLine("history --limit 1 | length")
	s.expect(`1`)

	s.clear()
	s.sendLine(`(string-append "seen:" (number->string (length (history "zebra-marker"))))`)
	s.expect(`seen:0`)
	s.clear()
	s.sendLine(`(string-append "seen:" (number->string (length (history "zebra-marker"))))`)
	s.expect(`seen:1`)

	// Ctrl+R reverse search recalls a previous command
	s.clear()
	s.send("\x12mkd") // C-r, then incremental query
	s.expect(`mkdir subdir`)
	s.send("\r")

	s.sendLine(" echo secretly")
	s.expect(`secretly`)
	s.clear()
	s.sendLine(`(length (history (string-append "secre" "tly")))`)
	s.expect(`0`)

	// History persists across restarts (new shell, same HOME)
	s.close()
	s2 := startShell(t, dir)
	s2.sendLine("history pwd")
	s2.expect(`command\s+0\s+pwd`)
}

func TestTabCompletionCommandMode(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "unique-file.txt"), []byte("file-contents-here"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(dir, "subdir"), 0755); err != nil {
		t.Fatal(err)
	}
	s := startShell(t, dir)

	s.send("cat uni\t")
	s.expect(`unique-file\.txt`)
	s.send("\r")
	s.expect(`file-contents-here`)

	// directories complete with a trailing slash
	s.clear()
	s.send("ls sub\t")
	s.expect(`subdir/`)
	s.send("\r")
}

func TestTabCompletionCommandNames(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	// A private bin dir on PATH proves external executables complete too
	bin := filepath.Join(dir, "fakebin")
	if err := os.Mkdir(bin, 0755); err != nil {
		t.Fatal(err)
	}
	script := []byte("#!/bin/sh\necho stripes\n")
	if err := os.WriteFile(filepath.Join(bin, "zebracount"), script, 0755); err != nil {
		t.Fatal(err)
	}
	s := startShell(t, dir, "PATH="+bin+":/usr/bin:/bin")

	s.send("builtin\t")
	s.expect(`builtins`)
	s.send("\r")
	s.expect(`special form`)

	// A PATH executable completes and runs via the /bin/sh fallthrough
	s.clear()
	s.send("zebracou\t")
	s.expect(`zebracount`)
	s.send("\r")
	s.expect(`stripes`)
}

func TestTabCompletionSchemeMode(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "unique-file.txt"), []byte("file-contents-here"), 0644); err != nil {
		t.Fatal(err)
	}
	s := startShell(t, dir)

	s.send(`(cat "uni` + "\t")
	s.expect(`unique-file\.txt`)
	s.send("\")\r")
	s.expect(`file-contents-here`)
}

func TestTabCompletionProjectArg(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "src", "zebraproject"), 0755); err != nil {
		t.Fatal(err)
	}
	s := startShell(t, dir)

	// Completion is the registry: only a registered name completes.
	s.sendLine("register-project ~/src/zebraproject")
	s.clear()
	s.send("project zebrapro\t")
	s.expect(`zebraproject`)
	s.send("\r")
	s.expect(`src/zebraproject`)
}

func TestAutosuggest(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "sub"), 0755); err != nil {
		t.Fatal(err)
	}
	s := startShell(t, dir)

	// History: zz-home ran here, then zz-sub ran in sub/ more recently
	s.sendLine("echo zeta-home")
	s.expect(`zeta-home`)
	s.sendLine("cd sub")
	s.sendLine("echo zeta-sub")
	s.expect(`zeta-sub`)
	s.sendLine("cd ..")

	s.clear()
	s.send("echo zet")
	s.expect(`echo zeta-home`)

	// The ghost is not input: enter runs only what was typed so far
	s.send("\r")
	s.expect(`(?m)^zet`)
	s.expectNot(`(?m)^zeta-home`)

	// Typing again and accepting with right-arrow completes and runs it
	s.clear()
	s.send("echo zet")
	s.expect(`echo zeta-home`)
	s.send("\x1b[C") // → accepts the ghost into the buffer
	s.send("\r")
	s.expect(`(?m)^zeta-home`)
}

func TestSuggestKeybinding(t *testing.T) {
	t.Parallel()
	var body atomic.Value
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/messages" {
			http.NotFound(w, r)
			return
		}
		b, _ := io.ReadAll(r.Body)
		body.Store(string(b))
		// Suggest is a plain (non-streaming) completion: one JSON response
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"content":[{"type":"text","text":"printf 'zeta-%s\\n' ok"}],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":3}}`)
	}))
	t.Cleanup(srv.Close)

	dir := t.TempDir()
	modelPrelude(t, dir, srv.URL)
	s := startShell(t, dir)

	s.send("print zeta dash ok")
	s.expect(`print zeta dash ok`)
	s.waitReady()
	s.send("\x0b") // Ctrl+K — the buffer repaints as the proposal
	s.expect(`printf 'zeta-%s\\n' ok`)

	// The request carried the buffer and stayed tiny: no tools, no history
	sent, _ := body.Load().(string)
	if !strings.Contains(sent, "print zeta dash ok") {
		t.Errorf("suggest request missing the buffer: %s", sent)
	}
	if len(sent) > 4096 {
		t.Errorf("suggest request is %d bytes — should be tiny", len(sent))
	}
	if strings.Contains(sent, `"tools"`) {
		t.Errorf("suggest request must not carry tools: %s", sent)
	}

	// Nothing ran yet; enter runs the proposal (sh fallthrough)
	s.expectNot(`zeta-ok`)
	s.send("\r")
	s.expect(`zeta-ok`)
}

func TestClassifyPipeline(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/messages" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"content":[{"type":"text","text":"1. fruit\n2. tool\n3. fruit"}],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":3}}`)
	}))
	t.Cleanup(srv.Close)

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "things.txt"), []byte("apple\nhammer\npear\n"), 0644); err != nil {
		t.Fatal(err)
	}
	modelPrelude(t, dir, srv.URL)
	s := startShell(t, dir)

	s.sendLine(`cat things.txt | classify "fruit or tool?"`)
	s.expect(`hammer\s+tool`)

	s.clear()
	s.sendLine(`cat things.txt | classify "fruit or tool?" | count-by label`)
	s.expect(`2\s+fruit`)
}

func TestRememberRecall(t *testing.T) {
	t.Parallel()
	s := startShell(t, t.TempDir())

	// Bare command words; the default graph is created lazily
	s.sendLine("remember lunch with bob on tuesday")
	s.sendLine("recall bob")
	s.expect(`lunch with bob on tuesday`)

	// Quoted arguments are literals, not shell metacharacters
	s.clear()
	s.sendLine(`remember "something about cats"`)
	s.sendLine("recall cats")
	s.expect(`something about cats`)
	s.expectNot(`command not found`)

	// Scheme forms hit the same store
	s.clear()
	s.sendLine("(recall)")
	s.expect(`lunch with bob on tuesday`)
	s.sendLine("(graphs)")
	s.expect(`knowledge-base`)
}

func TestLastResultBindings(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	for _, name := range []string{"a.txt", "b.txt"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("x"), 0644); err != nil {
			t.Fatal(err)
		}
	}
	s := startShell(t, dir)

	// Scheme results shift *1 -> *2 -> *3
	s.sendLine("(+ 1 1)")
	s.sendLine("(+ 2 2)")
	s.clear()
	s.sendLine("(list *1 *2)")
	s.expect(`\(4 2\)`)

	// Command-mode builtins record too: the dir table stays touchable
	s.sendLine("dir")
	s.expect(`a.txt`)
	s.clear()
	s.sendLine("(length *1)")
	s.expect(`2`)
	s.clear()
	s.sendLine(`(get "name" (get 0 *2))`)
	s.expect(`a.txt`)

	s.clear()
	s.sendLine(`(print "side effect")`)
	s.sendLine(`(string-upcase *1)`)
	s.expect(`A.TXT`)
}

func TestPrelude(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	prelude := "(define default-graph \"personal\")\n" +
		"(define greet (lambda (n) (string-append \"hey \" n)))\n"
	if err := os.WriteFile(filepath.Join(dir, ".rf.scm"), []byte(prelude), 0644); err != nil {
		t.Fatal(err)
	}
	s := startShell(t, dir)

	// Prelude definitions are visible in the session
	s.sendLine(`(greet "john")`)
	s.expect(`hey john`)

	// default-graph redirection applies to remember/recall
	s.sendLine("remember feed the cat")
	s.clear()
	s.sendLine("recall cat")
	s.expect(`feed the cat`)
	s.sendLine("(graphs)")
	s.expect(`personal`)
	s.expectNot(`knowledge-base`)
}

func TestUserCommandFallback(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	prelude := "(define (shout s) (string-upcase s))\n" +
		"(define (excite s) (string-append s \"!\"))\n" +
		"(define (e f) (exec \"cat\" f))\n"
	if err := os.WriteFile(filepath.Join(dir, ".rf.scm"), []byte(prelude), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("alias-target-content"), 0644); err != nil {
		t.Fatal(err)
	}
	s := startShell(t, dir)

	// A defined function is a command word: words become string arguments
	s.sendLine("shout hello")
	s.expect(`HELLO`)

	s.clear()
	s.sendLine("e notes.txt")
	s.expect(`alias-target-content`)

	// User functions work as pipeline heads and stages (thread-last)
	s.clear()
	s.sendLine("shout hey | excite")
	s.expect(`HEY!`)

	// Undefined words still fall through to /bin/sh
	s.clear()
	s.sendLine("echo still-sh-here")
	s.expect(`still-sh-here`)

	// Evaluation errors are loud, never a silent /bin/sh fallthrough
	s.clear()
	s.sendLine("shout a b c")
	s.expect(`Error`)
}

func TestAliases(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	prelude := "(alias ll \"ls -la\")\n" +
		"(alias greet \"echo alias-expanded\")\n"
	if err := os.WriteFile(filepath.Join(dir, ".rf.scm"), []byte(prelude), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".hidden"), []byte("x"), 0644); err != nil {
		t.Fatal(err)
	}
	s := startShell(t, dir)

	s.sendLine("ll")
	s.expect(`total `)
	s.expect(`\.hidden`)
	s.expectNot(`name\s+type\s+size`)

	// An alias to a system command goes to /bin/sh; trailing words append
	s.clear()
	s.sendLine("greet trailing-words")
	s.expect(`alias-expanded trailing-words`)

	// aliases is a command word listing the table
	s.clear()
	s.sendLine("aliases")
	s.expect(`name\s+expansion`)
	s.expect(`ll\s+ls -la`)

	// aliases can be defined at the prompt and removed again
	s.sendLine(`(alias tell "echo told")`)
	s.sendLine("tell you")
	s.expect(`told you`)
	s.sendLine("unalias tell")
	s.clear()
	s.sendLine("tell you")
	s.expect(`not found`)
}

func TestInspectCommand(t *testing.T) {
	t.Parallel()
	s := startShell(t, t.TempDir())

	// pp-style source recovery: signature line, indented body
	s.sendLine("(define (square x) (* x x))")
	s.clear()
	s.sendLine("inspect square")
	s.expect(`\(define \(square x\)`)
	s.expect(`\(\* x x\)\)`)

	// builtins have no Scheme source; the error says so
	s.clear()
	s.sendLine("inspect ls")
	s.expect(`implemented in Go`)

	// Scheme spelling returns the string (echoed quoted)
	s.clear()
	s.sendLine("(inspect square)")
	s.expect(`define \(square x\)`)
}

func TestGrepDirectoryCommand(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "sub"), 0755); err != nil {
		t.Fatal(err)
	}
	files := map[string]string{
		"a.txt":      "alpha needle\nplain\n",
		"sub/b.txt":  "no\nbeta needle\n",
		".gitignore": "skipme.txt\n",
		"skipme.txt": "needle\n",
	}
	for rel, content := range files {
		if err := os.WriteFile(filepath.Join(dir, rel), []byte(content), 0644); err != nil {
			t.Fatal(err)
		}
	}
	s := startShell(t, dir)

	s.sendLine(`(grep "needle" ".")`)
	s.expect(`file\s+line\s+text`)
	s.expect(`a.txt\s+1\s+alpha needle`)
	s.expect(`sub/b.txt\s+2\s+beta needle`)
	s.expectNot(`skipme`)

	// and composes like any rows source
	s.clear()
	s.sendLine(`(length (grep "needle" "."))`)
	s.expect(`2`)

	s.clear()
	s.sendLine("grep needle .")
	s.expect(`(?i)is a directory`)
}

func TestBM25Command(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	files := map[string]string{
		"a.txt": "rotate auth tokens hourly\nunrelated filler line\n",
		"b.txt": "grocery list: milk eggs\n",
	}
	for rel, content := range files {
		if err := os.WriteFile(filepath.Join(dir, rel), []byte(content), 0644); err != nil {
			t.Fatal(err)
		}
	}
	s := startShell(t, dir)

	// bm25 over a directory ranks the tree's lines and renders a scored table
	s.sendLine("bm25 tokens .")
	s.expect(`file\s+line\s+text\s+score`)
	s.expect(`a.txt\s+1\s+rotate auth tokens hourly`)
	s.expectNot(`grocery`)

	// composes as a stage over the builtin grep's rows
	s.clear()
	s.sendLine(`(length (bm25 "tokens" (grep "auth" ".")))`)
	s.expect(`1`)
}
