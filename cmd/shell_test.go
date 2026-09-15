package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/retrofilter/rf/eval"
	"github.com/stretchr/testify/require"
)

func TestSplitSimpleCommand(t *testing.T) {
	tests := []struct {
		desc   string
		line   string
		ok     bool
		tokens []cmdToken
	}{
		{"bare words", "remember lunch with bob",
			true, []cmdToken{{text: "remember", quoted: false, glob: false}, {text: "lunch", quoted: false, glob: false}, {text: "with", quoted: false, glob: false}, {text: "bob", quoted: false, glob: false}}},
		{"double-quoted argument", `remember "something about cats"`,
			true, []cmdToken{{text: "remember", quoted: false, glob: false}, {text: "something about cats", quoted: true, glob: false}}},
		{"single-quoted argument", `remember 'a $literal string'`,
			true, []cmdToken{{text: "remember", quoted: false, glob: false}, {text: "a $literal string", quoted: true, glob: false}}},
		{"metacharacters inside quotes are literal", `remember "rm -rf | dangerous > stuff"`,
			true, []cmdToken{{text: "remember", quoted: false, glob: false}, {text: "rm -rf | dangerous > stuff", quoted: true, glob: false}}},
		{"quote glued to a word", `cat "my file".txt`,
			true, []cmdToken{{text: "cat", quoted: false, glob: false}, {text: "my file.txt", quoted: true, glob: false}}},
		{"keyword option", "ls docs :all",
			true, []cmdToken{{text: "ls", quoted: false, glob: false}, {text: "docs", quoted: false, glob: false}, {text: ":all", quoted: false, glob: false}}},
		{"leading/trailing whitespace", "  pwd  ",
			true, []cmdToken{{text: "pwd", quoted: false, glob: false}}},

		// Unquoted glob metachars mark the token as a pattern
		{"glob", "cat *.txt",
			true, []cmdToken{{text: "cat", quoted: false, glob: false}, {text: "*.txt", quoted: false, glob: true}}},
		{"question-mark glob", "ls file?.go",
			true, []cmdToken{{text: "ls", quoted: false, glob: false}, {text: "file?.go", quoted: false, glob: true}}},
		{"char-class glob", "ls [ab].txt",
			true, []cmdToken{{text: "ls", quoted: false, glob: false}, {text: "[ab].txt", quoted: false, glob: true}}},
		{"quoted glob is literal", `ls "*.txt"`,
			true, []cmdToken{{text: "ls", quoted: false, glob: false}, {text: "*.txt", quoted: true, glob: false}}},

		{"backslash escape", `remember a\ b`,
			true, []cmdToken{{text: "remember", quoted: false, glob: false}, {text: "a b", quoted: true, glob: false}}},
		{"variable", "remember $HOME",
			true, []cmdToken{{text: "remember", quoted: false, glob: false}, {text: os.Getenv("HOME"), quoted: true, glob: false}}},
		{"expansion inside double quotes", `remember "$HOME"`,
			true, []cmdToken{{text: "remember", quoted: false, glob: false}, {text: os.Getenv("HOME"), quoted: true, glob: false}}},
		{"quoted glob stays literal after expansion", `remember "$HOME"*`,
			true, []cmdToken{{text: "remember", quoted: false, glob: false}, {text: os.Getenv("HOME") + "*", quoted: true, glob: false}}},

		{"pipe", "ls | grep foo", false, nil},
		{"redirect", "echo hi > out.txt", false, nil},
		{"brace expansion", "cat {a,b}.txt", false, nil},
		{"backtick", "echo `date`", false, nil},
		{"command substitution", "echo $(date)", false, nil},
		{"arithmetic", "echo $((1+2))", false, nil},
		{"unterminated double quote", `remember "oops`, false, nil},
		{"unterminated single quote", "remember don't", false, nil},
		{"assignment prefix", "FOO=1 remember x", false, nil},
		{"compound line", "remember x; remember y", false, nil},
	}

	for _, tt := range tests {
		t.Run(tt.desc, func(t *testing.T) {
			tokens, ok := splitSimpleCommand(tt.line)
			require.Equal(t, tt.ok, ok)
			if tt.ok {
				require.Equal(t, tt.tokens, withoutSpans(tokens))
				requireSpans(t, tt.line, tokens)
			}
		})
	}
}

func withoutSpans(tokens []cmdToken) []cmdToken {
	out := make([]cmdToken, len(tokens))
	for i, t := range tokens {
		t.start, t.end = 0, 0
		out[i] = t
	}
	return out
}

func requireSpans(t *testing.T, line string, tokens []cmdToken) {
	t.Helper()
	prev := 0
	for _, tok := range tokens {
		require.LessOrEqual(t, prev, tok.start)
		require.Less(t, tok.start, tok.end)
		span := line[tok.start:tok.end]
		if !strings.ContainsAny(span, "$\\") {
			require.Equal(t, tok.text, strings.NewReplacer("'", "", `"`, "").Replace(span), "span %q for token %q", span, tok.text)
		}
		prev = tok.end
	}
}

func TestParseCommandLine(t *testing.T) {
	eval.NewEvaluator()
	spans := func(line string, cl *cmdLine) []string {
		var out []string
		for _, s := range cl.stages {
			out = append(out, line[s.start:s.end])
		}
		return out
	}

	// Pipelines split at the shell's pipes, quotes respected.
	for line, want := range map[string][]string{
		"ls | grep md":                     {"ls", "grep md"},
		"ls|grep md|get 0":                 {"ls", "grep md", "get 0"},
		`remember "a | b"`:                 {`remember "a | b"`},
		`history "a | b" | grep x`:         {`history "a | b"`, "grep x"},
		"  pwd  ":                          {"pwd"},
		"make test 2>&1 | agent why":       {"make test 2>&1", "agent why"},
		"dir | where size -gt 10 | take 4": {"dir", "where size -gt 10", "take 4"},
	} {
		cl, ok := parseCommandLine(line, nil)
		require.True(t, ok, line)
		require.Equal(t, want, spans(line, cl), line)
	}

	// Compound lines, `|&`, background, negation, and parse errors are rsh's.
	for _, line := range []string{
		"ls || echo failed", "ls && make", "a; b", "sleep 1 &", "! true",
		"ls |& grep x", `ls | grep "oops`, "",
	} {
		_, ok := parseCommandLine(line, nil)
		require.False(t, ok, line)
	}

	// Redirects attach to their stage, with fd and operand read.
	cl, ok := parseCommandLine("make test 2>&1 | agent why", nil)
	require.True(t, ok)
	require.Equal(t, []cmdRedir{{op: ">&", fd: "2", target: "1", start: 10, end: 14}}, cl.stages[0].redirs)
	require.Empty(t, cl.stages[1].redirs)

	cl, ok = parseCommandLine(`history -n 5 >> "my out.tsv"`, nil)
	require.True(t, ok)
	require.Len(t, cl.stages[0].redirs, 1)
	require.Equal(t, ">>", cl.stages[0].redirs[0].op)
	require.Equal(t, "my out.tsv", cl.stages[0].redirs[0].target)
	require.Equal(t, []cmdToken{{text: "history"}, {text: "-n"}, {text: "5"}}, withoutSpans(cl.stages[0].tokens))

	cl, ok = parseCommandLine("for x in a b; do echo $x; done", nil)
	require.True(t, ok)
	require.Nil(t, cl.stages[0].tokens)
	_, ok = parseCommandLine("done 1", nil)
	require.False(t, ok)

	cl, ok = parseCommandLine("(cd x && ls) | take 2", nil)
	require.True(t, ok)
	require.Nil(t, cl.stages[0].tokens)
	require.Equal(t, []cmdToken{{text: "take"}, {text: "2"}}, withoutSpans(cl.stages[1].tokens))
	cl, ok = parseCommandLine("history -d $(pwd)", nil)
	require.True(t, ok)
	require.Equal(t, "command substitution", cl.stages[0].bail)

	line := `job --in 3s claude "what time is it?" > out.log`
	cl, ok = parseCommandLine(line, nil)
	require.True(t, ok)
	st := cl.stages[0]
	require.Equal(t, `claude "what time is it?" > out.log`, line[st.tokens[3].start:st.end])
}

func TestBuiltinCallArgs(t *testing.T) {
	eval.NewEvaluator() // registers the builtins' command metadata
	meta := func(head string) eval.CommandMeta {
		m, _ := eval.LookupCommand(head)
		return m
	}
	tests := []struct {
		desc   string
		head   string
		tokens []cmdToken
		args   []eval.Value
		nPos   int
		errs   string
	}{
		{"words become strings", "dir", []cmdToken{{text: "notes.txt", quoted: false, glob: false}},
			[]eval.Value{eval.String("notes.txt")}, 1, ""},
		{"keyword boolean sugar survives", "dir", []cmdToken{{text: "docs", quoted: false, glob: false}, {text: ":all", quoted: false, glob: false}},
			[]eval.Value{eval.String("docs"), eval.Keyword("all")}, 1, ""},
		{"quoted colon word stays a string", "remember", []cmdToken{{text: ":all", quoted: true, glob: false}},
			[]eval.Value{eval.String(":all")}, 1, ""},
		{"pipeline placeholder", "get", []cmdToken{{text: "_", quoted: false, glob: false}, {text: "0", quoted: false, glob: false}},
			[]eval.Value{eval.Symbol("_"), eval.String("0")}, 2, ""},
		{"glob token desugars to a (glob ...) form", "dir", []cmdToken{{text: "*.go", quoted: false, glob: true}},
			[]eval.Value{eval.Value([]eval.Value{eval.Symbol("glob"), eval.String("*.go")})}, 1, ""},
		{"quoted glob stays a literal string", "dir", []cmdToken{{text: "*.go", quoted: true, glob: false}},
			[]eval.Value{eval.String("*.go")}, 1, ""},
		{"short bool flag builds the dict", "dir", []cmdToken{{text: "-a", quoted: false, glob: false}},
			[]eval.Value{eval.Dictionary{"all": true}}, 0, ""},
		{"long bool flag", "dir", []cmdToken{{text: "--all", quoted: false, glob: false}},
			[]eval.Value{eval.Dictionary{"all": true}}, 0, ""},
		{"valued flags type and merge", "chunk", []cmdToken{{text: "notes.md", quoted: false, glob: false}, {text: "-s", quoted: false, glob: false}, {text: "500", quoted: false, glob: false}, {text: "--format", quoted: false, glob: false}, {text: "markdown", quoted: false, glob: false}},
			[]eval.Value{eval.String("notes.md"), eval.Dictionary{"size": eval.Integer(500), "format": eval.String("markdown")}}, 1, ""},
		{"--long=value spelling", "chunk", []cmdToken{{text: "--format=markdown", quoted: false, glob: false}},
			[]eval.Value{eval.Dictionary{"format": eval.String("markdown")}}, 0, ""},
		{"double dash ends flag parsing", "chunk", []cmdToken{{text: "--", quoted: false, glob: false}, {text: "-x", quoted: false, glob: false}},
			[]eval.Value{eval.String("-x")}, 1, ""},
		{"dash-digit word is not a flag", "where", []cmdToken{{text: "size", quoted: false, glob: false}, {text: "-gt", quoted: false, glob: false}, {text: "-5", quoted: false, glob: false}},
			[]eval.Value{eval.String("size"), eval.String("-gt"), eval.String("-5")}, 3, ""},
		{"declared operator words pass through", "where", []cmdToken{{text: "type"}, {text: "="}, {text: "file"}, {text: "and"}, {text: "size"}, {text: "-le"}, {text: "10MB"}},
			[]eval.Value{eval.String("type"), eval.String("="), eval.String("file"), eval.String("and"), eval.String("size"), eval.String("-le"), eval.String("10MB")}, 7, ""},
		{"unknown flag errors with ! hint", "dir", []cmdToken{{text: "-R", quoted: false, glob: false}}, nil, 0, "prefix the line with !"},
		{"flags on flagless builtins error", "where", []cmdToken{{text: "-n", quoted: false, glob: false}, {text: "f", quoted: false, glob: false}}, nil, 0, "prefix the line with !"},
		{"valued flag missing its value", "chunk", []cmdToken{{text: "--size", quoted: false, glob: false}}, nil, 0, "expects a value"},
		{"valued flag with a non-number", "chunk", []cmdToken{{text: "--size", quoted: false, glob: false}, {text: "x", quoted: false, glob: false}}, nil, 0, "expects a number"},
		{"bool flag rejects =value", "dir", []cmdToken{{text: "--all=1", quoted: false, glob: false}}, nil, 0, "takes no value"},
		{"quoted dash word is a literal", "remember", []cmdToken{{text: "-la", quoted: true, glob: false}},
			[]eval.Value{eval.String("-la")}, 1, ""},
	}
	for _, tt := range tests {
		t.Run(tt.desc, func(t *testing.T) {
			args, nPos, wantHelp, err := commandCallArgs(tt.head, meta(tt.head), tt.tokens, false)
			require.False(t, wantHelp)
			if tt.errs != "" {
				require.Error(t, err)
				require.Contains(t, err.Error(), tt.errs)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tt.args, args)
			require.Equal(t, tt.nPos, nPos)
		})
	}

	// -h and --help ask for help wherever they appear.
	for _, word := range []string{"-h", "--help"} {
		_, _, wantHelp, err := commandCallArgs("chunk", meta("chunk"), []cmdToken{{text: "notes.md", quoted: false, glob: false}, {text: word, quoted: false, glob: false}}, false)
		require.NoError(t, err)
		require.True(t, wantHelp)
	}
}

func TestExpandAliases(t *testing.T) {
	table := map[string]string{
		"ll":  "ls -la",
		"lg":  "ll", // chains resolve through the table
		"psc": "ps xawf -eo pid,user,cgroup,args",
		"ps":  "ps -x", // self-referential: expands once, no loop
	}
	lookup := func(name string) (string, bool) { exp, ok := table[name]; return exp, ok }

	tests := []struct{ in, out string }{
		{"ll docs", "ls -la docs"},
		{"lg", "ls -la"},
		{"ps aux", "ps -x aux"},
		// psc expands, then its new head ps expands once more (bash re-scans)
		{"psc extra", "ps -x xawf -eo pid,user,cgroup,args extra"},
		{"git status", "git status"}, // unknown words untouched
		{"'ll' docs", "'ll' docs"},   // quoted first word is literal
		{"", ""},
	}
	for _, tt := range tests {
		if got := expandAliases(lookup, tt.in); got != tt.out {
			t.Errorf("expandAliases(%q) = %q, want %q", tt.in, got, tt.out)
		}
	}
}

func TestUserCommandCallArgs(t *testing.T) {
	args, _, wantHelp, err := commandCallArgs("e", eval.CommandMeta{}, []cmdToken{{text: "-n", quoted: false, glob: false}, {text: "notes.txt", quoted: false, glob: false}, {text: ":all", quoted: false, glob: false}}, true)
	require.NoError(t, err)
	require.False(t, wantHelp)
	require.Equal(t, []eval.Value{eval.String("-n"), eval.String("notes.txt"), eval.Keyword("all")}, args)
}

func TestGitBranch(t *testing.T) {
	repo := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(repo, ".git"), 0o755))
	head := filepath.Join(repo, ".git", "HEAD")
	require.NoError(t, os.WriteFile(head, []byte("ref: refs/heads/feature/x\n"), 0o644))

	branch, root, ok := gitBranch(repo)
	require.True(t, ok)
	require.Equal(t, "feature/x", branch)
	require.Equal(t, repo, root)

	// Subdirectories walk up to the repository root
	sub := filepath.Join(repo, "a", "b")
	require.NoError(t, os.MkdirAll(sub, 0o755))
	branch, root, ok = gitBranch(sub)
	require.True(t, ok)
	require.Equal(t, "feature/x", branch)
	require.Equal(t, repo, root)

	// A worktree-style .git *file* points at the real git dir
	wt := t.TempDir()
	gitdir := filepath.Join(repo, ".git", "worktrees", "wt")
	require.NoError(t, os.MkdirAll(gitdir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(gitdir, "HEAD"), []byte("ref: refs/heads/wt-branch\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(wt, ".git"), []byte("gitdir: "+gitdir+"\n"), 0o644))
	branch, root, ok = gitBranch(wt)
	require.True(t, ok)
	require.Equal(t, "wt-branch", branch)
	require.Equal(t, wt, root)

	// Detached HEAD shows a short hash
	require.NoError(t, os.WriteFile(head, []byte("0123456789abcdef0123456789abcdef01234567\n"), 0o644))
	branch, _, ok = gitBranch(repo)
	require.True(t, ok)
	require.Equal(t, "0123456", branch)

	// Not a repository at all
	_, _, ok = gitBranch(t.TempDir())
	require.False(t, ok)
}

func TestCommandNameCandidates(t *testing.T) {
	bin := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(bin, "zetadeploy"), []byte("#!/bin/sh\n"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(bin, "zetadata.txt"), []byte("not a program"), 0o644))
	t.Setenv("PATH", bin)

	ev := eval.NewEvaluator()
	env := ev.GlobalEnv()
	exprs, err := eval.ParseAll(`(define (zeta-fn x) x) (alias zeta-al "ls -la")`)
	require.NoError(t, err)
	_, err = ev.EvalAll(exprs, env)
	require.NoError(t, err)

	completes := func(text string) []string {
		values, _, _ := commandNameCandidates(ev, env, text)
		return values
	}

	values, prefix, ok := commandNameCandidates(ev, env, "simila")
	require.True(t, ok)
	require.Equal(t, []string{"similar"}, values)
	require.Equal(t, "simila", prefix)
	_, prefix, _ = commandNameCandidates(ev, env, "!zeta")
	require.Equal(t, "zeta", prefix)
	require.Contains(t, completes("fe"), "fetch")
	require.Contains(t, completes("cd"), "cd") // shell-loop words too

	// User-defined functions, aliases, and PATH executables all complete
	require.Equal(t, []string{"zeta-al", "zeta-fn", "zetadeploy"}, completes("zeta"))

	// Pipeline stages offer stage words but not aliases
	require.Contains(t, completes("ls | ta"), "take")
	require.Contains(t, completes("ls | ta"), "table")
	require.NotContains(t, completes("cat f | zeta"), "zeta-al")

	// A ! line is sh whole: PATH executables only, at head and stages
	require.Equal(t, []string{"zetadeploy"}, completes("!zeta"))
	require.Equal(t, []string{"zetadeploy"}, completes("! zeta"))
	require.Equal(t, []string{"zetadeploy"}, completes("!ls | zeta"))

	// Non-command positions fall through to file completion (ok == false)
	for _, text := range []string{
		"cat simila",  // argument position
		"(simila",     // Scheme line
		"ls ",         // empty token
		"./simila",    // path-shaped
		`"simila`,     // quoted head is literal
		"nosuchcmdxy", // no match at all — maybe a file
	} {
		_, _, ok := commandNameCandidates(ev, env, text)
		require.False(t, ok, "expected no command completion for %q", text)
	}
}

func TestCommandFlagCandidates(t *testing.T) {
	eval.NewEvaluator() // registers the builtins' option tables

	flags := func(text string) []string {
		pairs, _, _ := commandFlagCandidates(text)
		var values []string
		for i := 0; i < len(pairs); i += 2 {
			values = append(values, pairs[i])
		}
		return values
	}

	pairs, prefix, ok := commandFlagCandidates("history --l")
	require.True(t, ok)
	require.Equal(t, []string{"--limit"}, []string{pairs[0]})
	require.NotEmpty(t, pairs[1]) // the doc line
	require.Equal(t, "--l", prefix)
	require.Equal(t, []string{"--dir", "--mode", "--project", "--limit"}, flags("history --"))

	// A bare `-` offers both spellings; flags follow positionals too
	require.Contains(t, flags("history -"), "-n")
	require.Contains(t, flags("history -"), "--limit")
	require.Contains(t, flags("history make -"), "--limit")

	require.Contains(t, flags("history | sort-by --d"), "--desc")
	_, _, ok = commandFlagCandidates("foo | history --l")
	require.False(t, ok, "history is not a stage word")

	// Everything else falls through to file completion (ok == false)
	for _, text := range []string{
		"history ",        // not a flag token
		"history -- --l",  // -- ended flags
		"where --",        // head with no declared options
		"ls --",           // shadowed name: system ls, flags unknowable
		"git log --",      // external command
		"(history --l",    // Scheme line
		"!history --l",    // sh-whole line
		"history --zippy", // no match at all — maybe a file
	} {
		_, _, ok := commandFlagCandidates(text)
		require.False(t, ok, "expected no flag completion for %q", text)
	}
}

func TestHistorySuggest(t *testing.T) {
	h := &sqliteHistory{
		lines: []string{
			"echo from-home",
			"(define x 1)",
			"why is the build red",
			"echo from-work",
			"echo done | take 2",
		},
		meta: []histMeta{
			{dir: "/home", mode: "command"},
			{dir: "/home", mode: "scheme"},
			{dir: "/home", mode: "agent"},
			{dir: "/work", mode: "command"},
			{dir: "/work", mode: "command"},
		},
	}

	require.Equal(t, "echo from-home", h.suggest("echo f", "/home", modeCommand))
	require.Equal(t, "echo from-work", h.suggest("echo f", "/work", modeCommand))
	require.Equal(t, "echo done | take 2", h.suggest("echo d", "/elsewhere", modeCommand))

	require.Equal(t, "", h.suggest("why", "/home", modeCommand))
	require.Equal(t, "why is the build red", h.suggest("why", "/home", modeAgent))
	require.Equal(t, "", h.suggest("echo f", "/home", modeAgent))
	require.Equal(t, "(define x 1)", h.suggest("(def", "/home", modeCommand))
	require.Equal(t, "(define x 1)", h.suggest("(def", "/home", modeAgent))

	require.Equal(t, "", h.suggest("", "/home", modeCommand))
	require.Equal(t, "", h.suggest("   ", "/home", modeCommand))
	require.Equal(t, "", h.suggest("echo from-home", "/home", modeCommand))
	h.lines = append(h.lines, "echo multi\necho line")
	h.meta = append(h.meta, histMeta{dir: "/home", mode: "command"})
	require.Equal(t, "echo from-home", h.suggest("echo", "/home", modeCommand))

	long := "claude 'investigate the rare rsh -race test flake seen during the F9-F16 fix session'"
	require.Greater(t, len(long), suggestMaxLen)
	atCap := "claude " + strings.Repeat("x", suggestMaxLen-len("claude "))
	h.lines = append(h.lines, long, atCap)
	h.meta = append(h.meta, histMeta{dir: "/home", mode: "command"}, histMeta{dir: "/home", mode: "command"})
	require.Equal(t, atCap, h.suggest("clau", "/home", modeCommand))
}

func TestCommandArgCandidates(t *testing.T) {
	orig := commandArgCompleters
	commandArgCompleters = map[string]func() []string{
		"tree": func() []string { return []string{"fix-prompt", "main"} },
	}
	t.Cleanup(func() { commandArgCompleters = orig })

	values, prefix, ok := commandArgCandidates("tree fi")
	require.True(t, ok)
	require.Equal(t, []string{"fix-prompt"}, values)
	require.Equal(t, "fi", prefix)
	values, _, ok = commandArgCandidates("tree ")
	require.True(t, ok)
	require.Equal(t, []string{"fix-prompt", "main"}, values)

	// Everything else falls through to file completion (ok == false)
	for _, text := range []string{
		"tree",         // head position, not an argument
		"tree main fi", // second argument — these commands take one
		"ls fi",        // head without a completion source
		"tree fi/x",    // path-shaped token
		"tree ~fi",     // home-shaped token
		`tree "fi`,     // quoted token is literal
		"(tree fi",     // Scheme line
		"!tree fi",     // sh-whole line
		"tree zz",      // no match — maybe a file
	} {
		_, _, ok := commandArgCandidates(text)
		require.False(t, ok, "expected no arg completion for %q", text)
	}
}

func TestExpandHistoryBangs(t *testing.T) {
	tests := []struct {
		desc     string
		line     string
		want     string
		replaced bool
	}{
		{"whole line", "!!", "make test", true},
		{"sudo prefix", "sudo !!", "sudo make test", true},
		{"pipeline tail", "!! | grep ok", "make test | grep ok", true},
		{"pipe-glued", "!!|grep ok", "make test|grep ok", true},
		{"escape composition", "!sudo !!", "!sudo make test", true},
		{"two occurrences", "!! && !!", "make test && make test", true},
		{"glued to a word stays literal", "echo a!!b", "echo a!!b", false},
		{"inside parens stays literal", "(f !!)", "(f !!)", false},
		{"single-quoted stays literal", "echo '!!'", "echo '!!'", false},
		{"double-quoted stays literal", `echo "!!"`, `echo "!!"`, false},
		{"no bangs", "make test", "make test", false},
		{"lone bang", "! true", "! true", false},
	}
	for _, tc := range tests {
		got, replaced := expandHistoryBangs(tc.line, "make test")
		require.Equal(t, tc.want, got, tc.desc)
		require.Equal(t, tc.replaced, replaced, tc.desc)
	}
}

func TestExpandHistory(t *testing.T) {
	h := &sqliteHistory{
		lines: []string{
			"why is the build red",
			"(+ 1 2)",
			"make test",
			"!!", // the just-accepted line — never its own event
		},
		meta: []histMeta{
			{mode: "agent"},
			{mode: "scheme"},
			{mode: "command"},
			{mode: "command"},
		},
	}
	line, used, err := h.expandHistory("sudo !!", modeCommand)
	require.NoError(t, err)
	require.True(t, used)
	require.Equal(t, "sudo make test", line)

	// A line with no !! passes through untouched.
	line, used, err = h.expandHistory("make build", modeCommand)
	require.NoError(t, err)
	require.False(t, used)
	require.Equal(t, "make build", line)

	h2 := &sqliteHistory{
		lines: []string{"why is the build red", "(+ 1 2)", "!!"},
		meta:  []histMeta{{mode: "agent"}, {mode: "scheme"}, {mode: "command"}},
	}
	line, used, err = h2.expandHistory("!!", modeCommand)
	require.NoError(t, err)
	require.True(t, used)
	require.Equal(t, "(+ 1 2)", line)

	// No eligible event: an error, like bash.
	h3 := &sqliteHistory{
		lines: []string{"why is the build red", "!!"},
		meta:  []histMeta{{mode: "agent"}, {mode: "command"}},
	}
	_, _, err = h3.expandHistory("!!", modeCommand)
	require.Error(t, err)
}

func TestFileCandidatesQuoting(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"Application Support", "plain", "it's $here"} {
		require.NoError(t, os.Mkdir(filepath.Join(dir, name), 0o755))
	}
	t.Chdir(dir)

	values, prefix := fileCandidates("cd Applic")
	require.Equal(t, []string{`Application\ Support/`}, values)
	require.Equal(t, "Applic", prefix)

	// A word carrying its own escapes is one word, and the prefix is as typed
	values, prefix = fileCandidates(`cd Application\ Sup`)
	require.Equal(t, []string{`Application\ Support/`}, values)
	require.Equal(t, `Application\ Sup`, prefix)

	values, prefix = fileCandidates("cd it")
	require.Equal(t, []string{`it\'s\ \$here/`}, values)
	require.Equal(t, "it", prefix)

	// Inside quotes the name goes in raw, and the quote is not part of the prefix
	values, prefix = fileCandidates(`cd "Application Sup`)
	require.Equal(t, []string{`Application Support/`}, values)
	require.Equal(t, "Application Sup", prefix)
	values, prefix = fileCandidates(`cd 'it`)
	require.Equal(t, []string{`it's $here/`}, values)
	require.Equal(t, "it", prefix)
	values, _ = fileCandidates(`cd "it`)
	require.Equal(t, []string{`it's \$here/`}, values)

	// Scheme decoration strips as before
	values, prefix = fileCandidates(`(cat "pl`)
	require.Equal(t, []string{"plain/"}, values)
	require.Equal(t, "pl", prefix)

	// The last word starts after an escaped or quoted blank, not at it
	values, prefix = fileCandidates(`cp Application\ Support pl`)
	require.Equal(t, []string{"plain/"}, values)
	require.Equal(t, "pl", prefix)
	values, _ = fileCandidates(`cp "Application Support" pl`)
	require.Equal(t, []string{"plain/"}, values)

	// Empty word lists everything, unquoted spellings escaped
	values, prefix = fileCandidates("ls ")
	require.Equal(t, []string{`Application\ Support/`, `it\'s\ \$here/`, "plain/"}, values)
	require.Equal(t, "", prefix)
}
