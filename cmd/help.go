package cmd

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strings"

	"github.com/retrofilter/rf/docs"
	"github.com/retrofilter/rf/eval"
	"golang.org/x/term"
)

var topicDocs = map[string]string{
	"modes":            "one prompt, two modes, and the keys that drive them",
	"command-mode":     "how command lines desugar to Scheme: flags, shadows, rsh",
	"pipelines":        "lazy pipelines, row verbs, rankers, and the unix crossing",
	"approval":         "what the model may do: y/N gates and the standing grants",
	"prelude":          "~/.rf.scm — configuration as Scheme, persist, the bindings",
	"memory-and-tasks": "notes, graphs, projects, and tasks in ~/.rf/main.db",
	"backups":          "copying ~/.rf/main.db (and the two config files) safely",
	"processes":        "background jobs, sh, timeouts, fg and ^Z",
	"console":          "the localhost session dashboard (rf console)",
	"claude-code":      "mirroring Claude Code transcripts, task context, hooks",
}

func registerHelpCommand(st *shellState) {
	eval.Register("help", "the rf manual: an overview page, topic pages, a man page per command",
		eval.CommandMeta{Command: true, MaxArgs: 1, Usage: "[page]",
			Options: []eval.Option{{Long: "list", Short: "l", Kind: eval.OptionBool,
				Doc: "the pages as rows instead (help --list | grep rank)"}}})
	st.chat.Env.Set("help", eval.BuiltinFunc(func(args []eval.Value, _ *eval.Environment) (eval.Value, error) {
		pos, opts, err := eval.ParseOptions("help", args)
		if err != nil {
			return nil, err
		}
		if list, _ := opts["list"].(bool); list {
			if len(pos) != 0 {
				return nil, errors.New("help --list takes no page name")
			}
			return manRows(), nil
		}
		page := ""
		if len(pos) == 1 {
			s, ok := pos[0].(eval.String)
			if !ok {
				return nil, errors.New("help expects a page name: (help \"chunk\")")
			}
			page = string(s)
		}
		if st.chat.Eval.RequireUser("help") != nil {
			text, err := manPage(page, false)
			if err != nil {
				return nil, err
			}
			return eval.StreamFromReader(strings.NewReader(text), nil), nil
		}
		text, err := manPage(page, true)
		if err != nil {
			return nil, err
		}
		return nil, pageText(text, func(cmd *exec.Cmd) error {
			_, err := st.runForeground(cmd, "help")
			return err
		})
	}))
}

func manPage(page string, color bool) (string, error) {
	page = strings.TrimSpace(page)
	if page == "" || strings.EqualFold(page, "rf") {
		return manOverview(color), nil
	}
	if _, ok := eval.LookupCommand(page); ok {
		return manCommand(page, color), nil
	}
	if heading, body, ok := guideTopic(page); ok {
		return manTopic(heading, body, color), nil
	}
	return "", fmt.Errorf("help: no page named %q — topics: %s; `help` lists everything, `help --list` as rows",
		page, strings.Join(topicSlugs(), ", "))
}

func manOverview(color bool) string {
	m := newMan(color)
	m.header("rf", 1, "RF Manual")
	m.section("name")
	m.para("**rf** — a programmable, persistent shell")
	m.section("description")
	m.markdown(guideIntro())
	m.section("topics")
	m.para("Longer-form pages, opened with **help** <topic>.")
	m.blank()
	for _, slug := range topicSlugs() {
		m.entry(slug+"(7)", topicDocs[slug])
	}
	m.section("commands")
	m.para("Every command word, each a page: **help** <command>. `<command> -h` prints the short usage.")
	m.blank()
	commands := eval.CommandWords()
	sort.Strings(commands)
	for _, name := range commands {
		m.entry(name+"(1)", eval.BuiltinDoc(name))
	}
	if stages := stageOnlyWords(); len(stages) > 0 {
		m.section("pipeline stages")
		m.para("Valid after a `|` but not as a line's first word.")
		m.blank()
		for _, name := range stages {
			m.entry(name+"(1)", eval.BuiltinDoc(name))
		}
	}
	m.section("see also")
	m.para("`help <topic>`, `help <command>`, `help --list` for the pages as rows, `(builtins)` for the whole Scheme library, HANDBOOK.md in the repository (the same registry, one file).")
	m.footer("rf", 1)
	return m.String()
}

func manCommand(name string, color bool) string {
	meta, _ := eval.LookupCommand(name)
	m := newMan(color)
	m.header(name, 1, "RF Manual")
	m.section("name")
	m.para("**" + name + "** — " + eval.BuiltinDoc(name))
	m.section("synopsis")
	usage, scheme, _ := eval.Synopsis(name)
	m.b.WriteString(manBodyIndent + m.bold(name) + strings.TrimPrefix(usage, name) + "\n")
	m.b.WriteString(manBodyIndent + scheme + "\n")
	if meta.Stage || meta.Globs {
		m.section("description")
		if meta.Stage {
			m.para("May appear after a `|` in a pipeline; the piped value threads in as the last argument.")
		}
		if meta.Globs {
			m.para("Unquoted glob patterns expand into matching paths (`" + name + " *.go`).")
		}
	}
	if len(meta.Options) > 0 {
		m.section("options")
		for _, o := range meta.Options {
			m.tagged(eval.FlagUsage(o), o.Doc)
		}
		m.tagged("-h, --help", "the short usage text")
	}
	m.section("see also")
	m.para("rf(1) — `help` for every page; `" + name + " -h` for the usage line.")
	m.footer(name, 1)
	return m.String()
}

func manTopic(heading, body string, color bool) string {
	slug := slugify(heading)
	m := newMan(color)
	m.header(slug, 7, "RF Manual")
	m.section("name")
	m.para("**" + slug + "** — " + topicDocs[slug])
	m.section("description")
	m.markdown(body)
	m.section("see also")
	m.para("rf(1) — `help` for the other topics and every command.")
	m.footer(slug, 7)
	return m.String()
}

func manRows() []eval.Value {
	rows := []eval.Value{}
	for _, slug := range topicSlugs() {
		rows = append(rows, eval.Dictionary{"page": eval.String(slug), "kind": eval.String("topic"), "doc": eval.String(topicDocs[slug])})
	}
	commands := eval.CommandWords()
	sort.Strings(commands)
	for _, name := range commands {
		rows = append(rows, eval.Dictionary{"page": eval.String(name), "kind": eval.String("command"), "doc": eval.String(eval.BuiltinDoc(name))})
	}
	for _, name := range stageOnlyWords() {
		rows = append(rows, eval.Dictionary{"page": eval.String(name), "kind": eval.String("stage"), "doc": eval.String(eval.BuiltinDoc(name))})
	}
	return rows
}

func stageOnlyWords() []string {
	isCommand := map[string]bool{}
	for _, name := range eval.CommandWords() {
		isCommand[name] = true
	}
	var out []string
	for _, name := range eval.StageWords() {
		if !isCommand[name] {
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return out
}

func guideIntro() string {
	body := docs.Guide
	if i := strings.Index(body, "\n## "); i >= 0 {
		body = body[:i]
	}
	if i := strings.Index(body, "\n"); i >= 0 {
		body = body[i:] // drop the "# rf guide" title line
	}
	return strings.TrimSpace(body)
}

func guideTopic(needle string) (heading, body string, ok bool) {
	want := slugify(needle)
	if want == "" {
		return "", "", false
	}
	lines := strings.Split(docs.Guide, "\n")
	for i, line := range lines {
		if !strings.HasPrefix(line, "## ") || !strings.Contains(slugify(line), want) {
			continue
		}
		end := len(lines)
		for j := i + 1; j < len(lines); j++ {
			if strings.HasPrefix(lines[j], "## ") {
				end = j
				break
			}
		}
		return strings.TrimPrefix(line, "## "), strings.TrimSpace(strings.Join(lines[i+1:end], "\n")), true
	}
	return "", "", false
}

func guideHeadings() []string {
	var out []string
	for _, line := range strings.Split(docs.Guide, "\n") {
		if strings.HasPrefix(line, "## ") {
			out = append(out, strings.TrimPrefix(line, "## "))
		}
	}
	return out
}

func topicSlugs() []string {
	var out []string
	for _, h := range guideHeadings() {
		out = append(out, slugify(h))
	}
	return out
}

func slugify(s string) string {
	s = strings.ToLower(strings.TrimPrefix(strings.TrimSpace(s), "## "))
	var b strings.Builder
	dash := true
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z' || r >= '0' && r <= '9':
			b.WriteRune(r)
			dash = false
		default:
			if !dash {
				b.WriteByte('-')
				dash = true
			}
		}
	}
	return strings.TrimSuffix(b.String(), "-")
}

func pageText(text string, run func(*exec.Cmd) error) error {
	if !term.IsTerminal(int(os.Stdout.Fd())) {
		fmt.Print(text)
		return nil
	}
	pager, pagerArgs := pagerCommand()
	if pager == "" {
		fmt.Print(text)
		return nil
	}
	f, err := os.CreateTemp("", "rf-help-*.txt")
	if err != nil {
		fmt.Print(text)
		return nil
	}
	defer os.Remove(f.Name())
	if _, err := f.WriteString(text); err != nil {
		f.Close()
		return err
	}
	f.Close()
	cmd := exec.Command(pager, append(pagerArgs, f.Name())...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	return run(cmd)
}

func pagerCommand() (string, []string) {
	if p := strings.Fields(os.Getenv("PAGER")); len(p) > 0 {
		if _, err := exec.LookPath(p[0]); err == nil {
			return p[0], p[1:]
		}
	}
	if _, err := exec.LookPath("less"); err == nil {
		return "less", []string{"-R"}
	}
	if _, err := exec.LookPath("more"); err == nil {
		return "more", nil
	}
	return "", nil
}
