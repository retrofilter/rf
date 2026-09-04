package cmd

import (
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync/atomic"

	"github.com/alecthomas/chroma/v2/quick"
	"github.com/charmbracelet/glamour"
	"github.com/charmbracelet/glamour/styles"
	"github.com/mattn/go-runewidth"
	"github.com/retrofilter/rf/eval"
	"github.com/retrofilter/rf/llm"
	"github.com/retrofilter/rf/models"
	"golang.org/x/term"
)

const (
	colorReset   = "\x1b[0m"
	colorCyan    = "\x1b[36m"
	colorGreen   = "\x1b[32m"
	colorMagenta = "\x1b[35m"
	colorBlue    = "\x1b[34m" // the splash wordmark
	colorRed     = "\x1b[31m"
	colorGray    = "\x1b[90m"

	bulletScheme = colorGreen + "⏺" + colorReset
	bulletText   = "⏺" // default foreground — white on dark themes
	bulletThink  = colorGray + "⏺" + colorReset
	elbow        = "  " + colorGray + "⎿" + colorReset + "  "
	elbowIndent  = "     "
)

var lightTerminal bool

func schemeHighlighter(line []rune) string {
	style := "xcode-dark"
	if lightTerminal {
		style = "xcode"
	}
	var highlighted strings.Builder
	err := quick.Highlight(&highlighted, string(line), "scheme", "terminal16m", style)
	if err != nil {
		return string(line)
	}
	return highlighted.String()
}

func smartChatHighlighter(line []rune) string {
	trimmed := strings.TrimLeft(string(line), " ")
	if isSchemeLine(trimmed) {
		return schemeHighlighter(line)
	}
	return string(line)
}

func renderMarkdown(content string) string {
	width := 80
	if w, _, err := term.GetSize(int(os.Stdout.Fd())); err == nil && w > 0 {
		width = min(w-4, 118)
	}
	style := styles.DarkStyleConfig
	if lightTerminal {
		style = styles.LightStyleConfig
	}
	margin := uint(0)
	style.Document.Margin = &margin
	if !lightTerminal {
		inlineCode := "86"
		style.Code.Color = &inlineCode
	}
	renderer, err := glamour.NewTermRenderer(
		glamour.WithStyles(style),
		glamour.WithWordWrap(width),
	)
	if err != nil {
		return content
	}
	out, err := renderer.Render(content)
	if err != nil {
		return content
	}
	return out
}

func bulletize(marker, body string) string {
	var sb strings.Builder
	for i, line := range strings.Split(strings.Trim(body, "\n"), "\n") {
		if i == 0 {
			sb.WriteString(marker + " " + line)
		} else {
			sb.WriteString("\n  " + line)
		}
	}
	return sb.String()
}

func resultBlock(result string) string {
	var sb strings.Builder
	for i, line := range strings.Split(strings.TrimRight(result, "\n"), "\n") {
		if i == 0 {
			sb.WriteString(elbow + line)
		} else {
			sb.WriteString("\n" + elbowIndent + line)
		}
	}
	return sb.String()
}

func clipLines(text string, n int) string {
	lines := strings.Split(strings.TrimRight(text, "\n"), "\n")
	if len(lines) <= n {
		return text
	}
	return strings.Join(lines[:n], "\n") + "\n" + colorGray + fmt.Sprintf("… +%d lines", len(lines)-n) + colorReset
}

type turnPrinter struct {
	raw    bool
	stream strings.Builder // raw streamed text currently on screen
	status bool            // working line currently shown
}

func (p *turnPrinter) print(s string) {
	if p.raw {
		s = strings.ReplaceAll(s, "\n", "\r\n")
	}
	fmt.Print(s)
}

func (p *turnPrinter) delta(kind llm.DeltaKind, d string) {
	p.hideStatus()
	if kind == llm.DeltaThinking {
		p.print(colorGray + d + colorReset)
	} else {
		p.print(d)
	}
	p.stream.WriteString(d)
}

func (p *turnPrinter) beginBlock() {
	p.hideStatus()
	p.eraseStream()
}

func (p *turnPrinter) eraseStream() {
	if p.stream.Len() == 0 {
		return
	}
	width := 80
	if w, _, err := term.GetSize(int(os.Stdout.Fd())); err == nil && w > 0 {
		width = w
	}
	rows := 0
	for _, line := range strings.Split(p.stream.String(), "\n") {
		rows += max(1, (runewidth.StringWidth(line)+width-1)/width)
	}
	fmt.Print("\r")
	if rows > 1 {
		fmt.Printf("\x1b[%dA", rows-1)
	}
	fmt.Print("\x1b[J")
	p.stream.Reset()
}

func (p *turnPrinter) showStatus() {
	if p.status || p.stream.Len() > 0 {
		return
	}
	p.print(colorGray + "working.. (ctrl+g to cancel)" + colorReset + "\n")
	p.status = true
}

func (p *turnPrinter) hideStatus() {
	if !p.status {
		return
	}
	fmt.Print("\x1b[1A\r\x1b[J")
	p.status = false
}

func printUsageFooter(chatInst *llm.Chat, printer *turnPrinter) {
	if line := chatInst.UsageLine(); line != "" {
		printer.print(colorGray + line + colorReset + "\n\n")
	}
}

func renderToolCall(msg *models.Message) []string {
	switch msg.FunctionName {
	case "read", "edit", "write":
		return renderFileToolCall(msg)
	}
	lines := []string{bulletScheme + " Scheme"}
	for _, line := range strings.Split(strings.TrimRight(schemeHighlighter([]rune(msg.Code)), "\n"), "\n") {
		lines = append(lines, "  "+line)
	}
	return lines
}

func renderFileToolCall(msg *models.Message) []string {
	var in struct {
		Path    string `json:"path"`
		Offset  int    `json:"offset"`
		Limit   int    `json:"limit"`
		Content string `json:"content"`
		Edits   []struct {
			OldText string `json:"oldText"`
			NewText string `json:"newText"`
		} `json:"edits"`
	}
	if err := json.Unmarshal([]byte(msg.Code), &in); err != nil || in.Path == "" {
		return []string{bulletScheme + " " + msg.FunctionName + " " + msg.Code}
	}
	title := strings.ToUpper(msg.FunctionName[:1]) + msg.FunctionName[1:]
	header := bulletScheme + " " + title + " " + in.Path
	lines := []string{header}
	switch msg.FunctionName {
	case "read":
		if in.Offset > 0 || in.Limit > 0 {
			lines[0] += colorGray + fmt.Sprintf(" (offset %d, limit %d)", max(in.Offset, 1), in.Limit) + colorReset
		}
	case "write":
		for _, line := range strings.Split(clipLines(in.Content, 8), "\n") {
			lines = append(lines, "  "+line)
		}
	case "edit":
		for i, e := range in.Edits {
			if i > 0 {
				lines = append(lines, "")
			}
			lines = appendDiffLines(lines, e.OldText, colorRed+"- ")
			lines = appendDiffLines(lines, e.NewText, colorGreen+"+ ")
		}
	}
	return lines
}

func appendDiffLines(lines []string, text, prefix string) []string {
	if text == "" {
		return lines
	}
	for _, l := range strings.Split(clipLines(strings.TrimRight(text, "\n"), 8), "\n") {
		lines = append(lines, "  "+prefix+l+colorReset)
	}
	return lines
}

func prompt(mode string) string {
	name, display, branch := promptParts()
	if name != "" {
		name = colorMagenta + name + colorReset + " "
	}
	if branch != "" {
		branch = colorMagenta + branch + colorReset
	}
	form := colorGray + "(" + colorReset + name + colorCyan + display + colorReset + branch + colorGray + ")" + colorReset
	if mode == modeAgent {
		return form + " " + colorCyan + "⏺" + colorReset + " "
	}
	return form + " " + colorGreen + "$" + colorReset + " "
}

func promptParts() (name, display, branch string) {
	cwd, err := os.Getwd()
	if err != nil {
		cwd = "?"
	}
	display = cwd
	if home, err := os.UserHomeDir(); err == nil && strings.HasPrefix(display, home) {
		display = "~" + strings.TrimPrefix(display, home)
	}
	if project, _, ok := eval.FindProject(cwd); ok {
		name = project
	} else if b, _, ok := gitBranch(cwd); ok {
		branch = "@" + b
	}
	return name, display, branch
}

func terminalTitle() string {
	title := "rf"
	cwd, err := os.Getwd()
	if err == nil {
		if project, hub, ok := eval.FindProject(cwd); ok {
			title = project
			if rel, err := filepath.Rel(hub, cwd); err == nil && rel != "." {
				title += "@" + strings.SplitN(rel, string(filepath.Separator), 2)[0]
			}
		} else if branch, root, ok := gitBranch(cwd); ok {
			title = filepath.Base(root) + "@" + branch
		}
	}
	out := "\x1b]2;" + title + "\a"
	if err == nil {
		host, _ := os.Hostname()
		u := url.URL{Scheme: "file", Host: host, Path: cwd}
		out += "\x1b]7;" + u.String() + "\a"
	}
	return out
}

var lastTitle string

func setTerminalTitle(title string) {
	if title == lastTitle {
		return
	}
	lastTitle = title
	os.Stdout.WriteString(title)
}

func resetTerminalTitle() { lastTitle = "" }

func gitBranch(cwd string) (branch, root string, ok bool) {
	for dir := cwd; ; dir = filepath.Dir(dir) {
		gitPath := filepath.Join(dir, ".git")
		info, err := os.Stat(gitPath)
		if err != nil {
			if dir == filepath.Dir(dir) {
				return "", "", false
			}
			continue
		}
		gitDir := gitPath
		if !info.IsDir() {
			data, err := os.ReadFile(gitPath)
			if err != nil {
				return "", "", false
			}
			gitDir = strings.TrimSpace(strings.TrimPrefix(string(data), "gitdir:"))
			if !filepath.IsAbs(gitDir) {
				gitDir = filepath.Join(dir, gitDir)
			}
		}
		head, err := os.ReadFile(filepath.Join(gitDir, "HEAD"))
		if err != nil {
			return "", "", false
		}
		ref := strings.TrimSpace(string(head))
		if branch, ok := strings.CutPrefix(ref, "ref: refs/heads/"); ok && branch != "" {
			return branch, dir, true
		}
		if len(ref) >= 7 {
			return ref[:7], dir, true // detached HEAD
		}
		return "", "", false
	}
}

func renderTable(v eval.Value) (string, bool) {
	width := 0
	if w, _, err := term.GetSize(int(os.Stdout.Fd())); err == nil && w > 0 {
		width = w
	}
	return eval.FormatTableColorWidth(v, width)
}

func drainStream(s *eval.Stream) (eval.Value, error) {
	var interrupted atomic.Bool
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt)
	defer signal.Stop(sigCh)
	done := make(chan struct{})
	defer close(done)
	go func() {
		select {
		case <-sigCh:
			interrupted.Store(true)
			s.Close() // unblocks a Next waiting on the producer
		case <-done:
		}
	}()

	if s.Lines() {
		var sb strings.Builder
		for {
			v, ok, err := s.Next()
			if err != nil {
				if interrupted.Load() {
					break
				}
				return eval.String(sb.String()), err
			}
			if !ok {
				break
			}
			text := eval.PrintValue(v)
			if str, isStr := v.(eval.String); isStr {
				text = string(str)
			}
			fmt.Println(text)
			sb.WriteString(text)
			sb.WriteByte('\n')
		}
		return eval.String(sb.String()), nil
	}

	var vals []eval.Value
	for {
		v, ok, err := s.Next()
		if err != nil {
			if interrupted.Load() {
				break
			}
			return vals, err
		}
		if !ok {
			break
		}
		vals = append(vals, v)
	}
	if len(vals) > 0 {
		if tbl, ok := renderTable(eval.Value(vals)); ok {
			fmt.Println(tbl)
		} else {
			fmt.Println(eval.PrintValue(eval.Value(vals)))
		}
	}
	return vals, nil
}

func printCommandResult(st *shellState, result eval.Value) int {
	if s, isStream := result.(*eval.Stream); isStream {
		materialized, err := drainStream(s)
		st.chat.Eval.RecordResult(materialized)
		if err != nil {
			fmt.Println(colorRed+"Error:"+colorReset, err)
			return 1
		}
		return 0
	}
	st.chat.Eval.RecordResult(result)
	switch v := result.(type) {
	case nil:
	case eval.String:
		if st.lastAgentReply != "" && string(v) == st.lastAgentReply {
			st.echoSuppressed = true
			break
		}
		// cat, pwd, which, ... print raw in command mode, not quoted
		fmt.Println(string(v))
	default:
		if tbl, ok := renderTable(result); ok {
			fmt.Println(tbl)
		} else {
			fmt.Println(eval.PrintValue(result))
		}
	}
	return 0
}

func printSplash() {
	lines := []string{
		colorBlue + `        __  ` + colorReset,
		colorBlue + `  _ __ / _| ` + colorReset + ` ` + colorBlue + `retrofilter` + colorReset + ` — a programmable, persistent shell`,
		colorBlue + ` | '__| |_  ` + colorReset + ` ` + colorGreen + `command` + colorReset + `: unix, structured pipes, scheme`,
		colorBlue + ` | |  |  _| ` + colorReset + ` ` + colorCyan + `agent` + colorReset + `: ctrl+space, switches to agent mode`,
		colorBlue + ` |_|  |_|   ` + colorReset + ` ` + colorCyan + `help` + colorReset + ` opens the manual - ` + colorCyan + `configure` + colorReset + ` sets the model`,
		"",
	}
	for _, line := range lines {
		fmt.Println(line)
	}
}
