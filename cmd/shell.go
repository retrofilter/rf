package cmd

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/jmoiron/sqlx"
	"github.com/reeflective/readline"
	"github.com/reeflective/readline/inputrc"
	"github.com/retrofilter/rf/core"
	"github.com/retrofilter/rf/eval"
	"github.com/retrofilter/rf/llm"
	"github.com/retrofilter/rf/models"
	"github.com/retrofilter/rf/rsh"
	"github.com/spf13/cobra"
	"golang.org/x/sys/unix"
	"golang.org/x/term"
)

const (
	modeCommand = "command"
	modeAgent   = "agent"
)

const suggestTimeout = 30 * time.Second

type sqliteHistory struct {
	db      *sqlx.DB
	mode    *string // shell mode at write time (command/agent)
	session string
	lines   []string
	meta    []histMeta // parallel to lines: ranking data for autosuggestions
	lastID  int64
}

type histMeta struct{ dir, mode string }

func newSessionID() string {
	buf := make([]byte, 8)
	_, _ = rand.Read(buf)
	return hex.EncodeToString(buf)
}

func newSQLiteHistory(db *sqlx.DB, mode *string) *sqliteHistory {
	h := &sqliteHistory{db: db, mode: mode, session: newSessionID()}
	if seeds, err := models.RecentHistorySeeds(db, 10000); err == nil {
		h.lines = make([]string, len(seeds))
		h.meta = make([]histMeta, len(seeds))
		for i, s := range seeds {
			h.lines[i] = s.Line
			h.meta[i] = histMeta{dir: s.Cwd, mode: s.Mode}
		}
	}
	return h
}

func (h *sqliteHistory) Write(line string) (int, error) {
	if strings.TrimSpace(line) == "" {
		return len(h.lines), nil
	}
	mode := *h.mode
	if isSchemeLine(strings.TrimSpace(line)) {
		mode = "scheme"
	}
	cwd, err := os.Getwd()
	if err != nil {
		cwd = ""
	}
	h.lines = append(h.lines, line)
	h.meta = append(h.meta, histMeta{dir: cwd, mode: mode})
	h.lastID = 0
	if strings.HasPrefix(line, " ") {
		return len(h.lines), nil
	}

	project := ""
	if name, _, ok := eval.FindProject(cwd); ok {
		project = name
	}
	entry := models.HistoryEntry{Line: line, Mode: mode, Cwd: cwd, Project: project, Session: h.session}
	if err := models.AddHistory(h.db, &entry); err == nil {
		h.lastID = entry.ID
	}
	return len(h.lines), nil
}

func (h *sqliteHistory) setLastExit(code int) {
	if h.lastID == 0 {
		return
	}
	_ = models.SetHistoryExit(h.db, h.lastID, code)
	h.lastID = 0
}

func (h *sqliteHistory) GetLine(i int) (string, error) {
	if i < 0 || i >= len(h.lines) {
		return "", fmt.Errorf("history: index %d out of range", i)
	}
	return h.lines[i], nil
}

func (h *sqliteHistory) Len() int  { return len(h.lines) }
func (h *sqliteHistory) Dump() any { return h.lines }

const suggestMaxLen = 80

func (h *sqliteHistory) suggest(text, dir, mode string) string {
	if strings.TrimSpace(text) == "" {
		return ""
	}
	fallback := ""
	for i := len(h.lines) - 1; i >= 0; i-- {
		line := h.lines[i]
		if len(line) <= len(text) || len(line) > suggestMaxLen ||
			!strings.HasPrefix(line, text) || strings.ContainsRune(line, '\n') {
			continue
		}
		if m := h.meta[i].mode; m != mode && m != "scheme" {
			continue
		}
		if h.meta[i].dir == dir {
			return line
		}
		if fallback == "" {
			fallback = line
		}
	}
	return fallback
}

func expandHistoryBangs(line, prev string) (string, bool) {
	boundary := func(c byte) bool { return c == ' ' || c == '\t' || c == '|' }
	var out strings.Builder
	replaced := false
	var quote byte
	for i := 0; i < len(line); i++ {
		c := line[i]
		switch {
		case quote != 0:
			if c == quote {
				quote = 0
			}
		case c == '\'' || c == '"':
			quote = c
		case c == '!' && i+1 < len(line) && line[i+1] == '!' &&
			(i == 0 || boundary(line[i-1])) &&
			(i+2 == len(line) || boundary(line[i+2])):
			out.WriteString(prev)
			replaced = true
			i++
			continue
		}
		out.WriteByte(c)
	}
	return out.String(), replaced
}

func (h *sqliteHistory) previousLine(mode string) (string, bool) {
	for i := len(h.lines) - 2; i >= 0; i-- {
		if m := h.meta[i].mode; m != mode && m != "scheme" {
			continue
		}
		if _, hasBang := expandHistoryBangs(h.lines[i], ""); hasBang {
			continue
		}
		return h.lines[i], true
	}
	return "", false
}

func (h *sqliteHistory) expandHistory(line, mode string) (string, bool, error) {
	if _, has := expandHistoryBangs(line, ""); !has {
		return line, false, nil
	}
	prev, ok := h.previousLine(mode)
	if !ok {
		return "", false, errors.New("!!: event not found")
	}
	expanded, _ := expandHistoryBangs(line, prev)
	return expanded, true, nil
}

func (h *sqliteHistory) recordExpansion(expanded string) {
	mode := *h.mode
	if isSchemeLine(strings.TrimSpace(expanded)) {
		mode = "scheme"
	}
	if n := len(h.lines); n > 0 {
		h.lines[n-1] = expanded
		h.meta[n-1].mode = mode
	}
	if h.lastID != 0 {
		_ = models.UpdateHistoryLine(h.db, h.lastID, expanded, mode)
	}
}

type sessionRecorder struct {
	db      *sqlx.DB
	session string
	parent  string // spawning shell session for (agent ...) sub-sessions
	ensured bool
}

func (r *sessionRecorder) record(msgs ...*models.Message) {
	if !r.ensured {
		cwd, err := os.Getwd()
		if err != nil {
			cwd = ""
		}
		project := ""
		if name, _, ok := eval.FindProject(cwd); ok {
			project = name
		}
		if err := models.EnsureSession(r.db, r.session, cwd, project, r.parent); err != nil {
			return
		}
		r.ensured = true
	}
	for _, m := range msgs {
		_, _ = models.AppendSessionMessage(r.db, r.session, m)
	}
}

type turnControl struct {
	printer *turnPrinter

	mu       sync.Mutex
	ctx      context.Context    // non-nil while a turn is running
	cancel   context.CancelFunc // ditto
	approval chan bool          // non-nil while a tool approval is pending
}

func (c *turnControl) begin(ctx context.Context, cancel context.CancelFunc) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.ctx, c.cancel = ctx, cancel
}

func (c *turnControl) end() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.ctx, c.cancel, c.approval = nil, nil, nil
}

func (c *turnControl) activeContext() context.Context {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.ctx
}

func (c *turnControl) cancelTurn() {
	c.mu.Lock()
	cancel := c.cancel
	c.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

func (c *turnControl) approve(action string) bool {
	reply := make(chan bool, 1)
	c.mu.Lock()
	ctx := c.ctx
	c.approval = reply
	c.mu.Unlock()
	if ctx == nil {
		return false
	}
	c.printer.beginBlock()
	c.printer.print(colorRed + "approve?" + colorReset + " " + action + " [y/N] ")
	ok := false
	select {
	case ok = <-reply:
	case <-ctx.Done():
	}
	c.mu.Lock()
	c.approval = nil
	c.mu.Unlock()
	if ok {
		c.printer.print("y\n")
	} else {
		c.printer.print("n\n")
	}
	return ok
}

func (c *turnControl) handleApprovalKey(b byte) bool {
	c.mu.Lock()
	reply := c.approval
	if reply != nil {
		switch b {
		case 'y', 'Y':
			c.approval = nil
		case 'n', 'N', '\r', '\n':
			c.approval = nil
		}
	}
	c.mu.Unlock()
	if reply == nil {
		return false
	}
	switch b {
	case 'y', 'Y':
		reply <- true
	case 'n', 'N', '\r', '\n':
		reply <- false
	}
	return true
}

func watchTurnKeys(fd int, done <-chan struct{}, chat *llm.Chat, ctrl *turnControl) {
	var steer []byte
	buf := make([]byte, 64)
	for {
		select {
		case <-done:
			return
		default:
		}
		fds := []unix.PollFd{{Fd: int32(fd), Events: unix.POLLIN}}
		n, err := unix.Poll(fds, 50)
		if err != nil && !errors.Is(err, unix.EINTR) {
			return
		}
		select {
		case <-done:
			return
		default:
		}
		if n <= 0 || fds[0].Revents&unix.POLLIN == 0 {
			continue
		}
		nr, err := os.Stdin.Read(buf)
		if err != nil || nr == 0 {
			return
		}
		for _, b := range buf[:nr] {
			switch {
			case b == 0x07 || b == 0x03: // ctrl+g / ctrl-c
				eval.Interrupt()
				ctrl.cancelTurn()
			case ctrl.handleApprovalKey(b):
			case b == '\r' || b == '\n':
				if line := strings.TrimSpace(string(steer)); line != "" {
					chat.Steer(line)
				}
				steer = steer[:0]
			case b == 0x7f || b == 0x08: // backspace
				if len(steer) > 0 {
					steer = steer[:len(steer)-1]
				}
			case b >= 0x20:
				steer = append(steer, b)
			}
		}
	}
}

func runChat(chatInst *llm.Chat, ctrl *turnControl, printer *turnPrinter, rec *sessionRecorder, message string, history []*models.Message) ([]*models.Message, bool) {
	fd := int(os.Stdin.Fd())
	if oldState, err := term.MakeRaw(fd); err == nil {
		printer.raw = true
		defer func() {
			printer.raw = false
			_ = term.Restore(fd, oldState)
		}()
	}

	overflowRetried := false
	for {
		ctx, cancel := context.WithCancel(context.Background())
		ctrl.begin(ctx, cancel)
		done := make(chan struct{})
		go watchTurnKeys(fd, done, chatInst, ctrl)

		printer.showStatus()
		replies, err := chatInst.Message(ctx, message, history, "")
		close(done)
		ctrl.end()
		cancel()
		printer.hideStatus()
		// Canceled mid-stream: newline-terminate the partial raw text.
		if printer.stream.Len() > 0 {
			printer.print("\n")
			printer.stream.Reset()
		}

		if err != nil && llm.IsContextOverflow(err) && !overflowRetried && rec.ensured {
			overflowRetried = true
			printer.print(colorGray + "context overflow — compacting and retrying the turn.." + colorReset + "\n")
			if _, transcript, cerr := compactSession(chatInst, rec); cerr == nil {
				history = transcript
				continue
			} else {
				printer.print(colorGray + "auto-compact failed: " + cerr.Error() + colorReset + "\n")
			}
		}

		userMsg := &models.Message{
			Role:    models.MessageRoleUser,
			Type:    models.MessageTypeText,
			Content: message,
		}
		history = append(history, userMsg)
		history = append(history, replies...)
		rec.record(userMsg)
		rec.record(replies...)

		if err != nil {
			if errors.Is(err, context.Canceled) {
				printer.print(colorRed + "✗ interrupted" + colorReset + "\n\n")
			} else {
				printer.print(colorRed + "Error:" + colorReset + " " + err.Error() + "\n\n")
			}
			printUsageFooter(chatInst, printer)
			return history, true
		}
		leftovers := chatInst.TakeSteers()
		if len(leftovers) == 0 {
			printUsageFooter(chatInst, printer)
			return history, false
		}
		message = strings.Join(leftovers, "\n")
		printer.print(colorGray + "» " + message + colorReset + "\n")
	}
}

func attachChatHooks(chatInst *llm.Chat, ctrl *turnControl, printer *turnPrinter) {
	chatInst.OnDelta = printer.delta
	chatInst.OnEvent = func(event llm.Event, msg *models.Message) {
		printer.beginBlock()
		switch event {
		case llm.EventToolCall:
			for _, line := range renderToolCall(msg) {
				printer.print(line + "\n")
			}
		case llm.EventToolResult:
			if msg.ErrorMessage != "" {
				printer.print(resultBlock(colorRed+"Error: "+colorReset+msg.ErrorMessage) + "\n\n")
			} else {
				display := msg.Result
				if msg.FunctionName == "read" {
					display = clipLines(display, 5)
				}
				printer.print(resultBlock(display) + "\n\n")
			}
		case llm.EventThinking:
			printer.print(bulletize(bulletThink, colorGray+clipLines(strings.TrimSpace(msg.Content), 4)+colorReset) + "\n\n")
		case llm.EventText:
			if strings.TrimSpace(msg.Content) != "" {
				printer.print(bulletize(bulletText, renderMarkdown(msg.Content)) + "\n\n")
			}
		case llm.EventSteer:
			printer.print(colorGray + "» " + msg.Content + colorReset + "\n")
		case llm.EventRetry:
			printer.print(colorGray + msg.Content + colorReset + "\n")
		case llm.EventNotice:
			// Display-only, like retry: e.g. a prelude hook erroring.
			printer.print(colorGray + msg.Content + colorReset + "\n")
		}
		printer.showStatus()
	}
	chatInst.Approve = ctrl.approve
}

func expandHome(path string) string {
	if path == "~" || strings.HasPrefix(path, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, strings.TrimPrefix(path, "~"))
		}
	}
	return path
}

func (st *shellState) complete(line []rune, cursor int) readline.Completions {
	text := string(line[:cursor])
	if st.mode == modeCommand || strings.HasPrefix(strings.TrimLeft(text, " \t"), "!") {
		if comps, ok := completeCommandName(st.chat.Eval, st.chat.Env, text); ok {
			return comps
		}
	}
	if st.mode == modeCommand {
		if comps, ok := completeCommandFlag(text); ok {
			return comps
		}
		if comps, ok := completeCommandArg(text); ok {
			return comps
		}
	}
	return completeFiles(line, cursor)
}

func completeCommandFlag(text string) (readline.Completions, bool) {
	pairs, prefix, ok := commandFlagCandidates(text)
	if !ok {
		return readline.Completions{}, false
	}
	comps := readline.CompleteValuesDescribed(pairs...)
	comps.PREFIX = prefix
	return comps, true
}

func commandFlagCandidates(text string) ([]string, string, bool) {
	trimmed := strings.TrimLeft(text, " \t")
	if isSchemeLine(trimmed) || strings.HasPrefix(trimmed, "!") {
		return nil, "", false
	}
	start := strings.LastIndexAny(text, " \t")
	token := text[start+1:]
	if !strings.HasPrefix(token, "-") {
		return nil, "", false
	}
	stage := text[:start+1]
	piped := false
	if i := strings.LastIndex(stage, "|"); i >= 0 {
		stage = stage[i+1:]
		piped = true
	}
	words := strings.Fields(stage)
	if len(words) == 0 {
		return nil, "", false
	}
	if slices.Contains(words[1:], "--") {
		return nil, "", false // flags ended
	}
	meta, ok := eval.LookupCommand(words[0])
	if !ok || piped && !meta.Stage || !piped && !meta.Command {
		return nil, "", false
	}
	var pairs []string
	for _, o := range meta.Options {
		if long := "--" + o.Long; strings.HasPrefix(long, token) {
			pairs = append(pairs, long, o.Doc)
		}
		if short := "-" + o.Short; o.Short != "" && strings.HasPrefix(short, token) {
			pairs = append(pairs, short, o.Doc)
		}
	}
	if len(pairs) == 0 {
		return nil, "", false
	}
	return pairs, token, true
}

var commandArgCompleters = map[string]func() []string{
	"tree":        eval.TreeNames,
	"delete-tree": eval.TreeNames,
	"project":     eval.ProjectNames,
	"note":        eval.NoteNames,
}

func completeCommandArg(text string) (readline.Completions, bool) {
	values, prefix, ok := commandArgCandidates(text)
	if !ok {
		return readline.Completions{}, false
	}
	comps := readline.CompleteValues(values...)
	comps.PREFIX = prefix
	return comps, true
}

func commandArgCandidates(text string) ([]string, string, bool) {
	trimmed := strings.TrimLeft(text, " \t")
	if isSchemeLine(trimmed) || strings.HasPrefix(trimmed, "!") {
		return nil, "", false
	}
	start := strings.LastIndexAny(text, " \t")
	token := text[start+1:]
	source, ok := commandArgCompleters[strings.TrimSpace(text[:start+1])]
	if !ok || strings.ContainsAny(token, "/'\"") || strings.HasPrefix(token, "~") {
		return nil, "", false
	}
	var values []string
	for _, name := range source() {
		if strings.HasPrefix(name, token) {
			values = append(values, name)
		}
	}
	if len(values) == 0 {
		return nil, "", false
	}
	return values, token, true
}

var shellLoopWords = []string{"cd", "fg", "jobs", "exit", "quit"}

func completeCommandName(ev *eval.Evaluator, env *eval.Environment, text string) (readline.Completions, bool) {
	values, prefix, ok := commandNameCandidates(ev, env, text)
	if !ok {
		return readline.Completions{}, false
	}
	comps := readline.CompleteValues(values...)
	comps.PREFIX = prefix
	return comps, true
}

func commandNameCandidates(ev *eval.Evaluator, env *eval.Environment, text string) ([]string, string, bool) {
	trimmed := strings.TrimLeft(text, " \t")
	if isSchemeLine(trimmed) {
		return nil, "", false
	}
	start := strings.LastIndexAny(text, " \t")
	token := text[start+1:]
	before := strings.TrimSpace(text[:start+1])

	bang := strings.HasPrefix(trimmed, "!")
	if bang && before == "" {
		token = strings.TrimPrefix(token, "!")
	}
	stage := strings.HasSuffix(before, "|")
	if bang && before == "!" {
		before = "" // `! cmd`: still the head
	}
	if before != "" && !stage {
		return nil, "", false // an argument, not a command head
	}
	if token == "" || strings.ContainsAny(token, "/'\"") || strings.HasPrefix(token, "~") {
		return nil, "", false
	}

	seen := map[string]bool{}
	add := func(names ...string) {
		for _, name := range names {
			if strings.HasPrefix(name, token) {
				seen[name] = true
			}
		}
	}
	if !bang {
		add(eval.CommandWords()...)
		add(env.UserFunctionNames()...)
		if stage {
			add(eval.StageWords()...)
		} else {
			add(shellLoopWords...)
			add(ev.AliasNames()...)
		}
	}
	add(pathExecutables(token)...)
	if len(seen) == 0 {
		return nil, "", false // maybe a file after all
	}

	values := make([]string, 0, len(seen))
	for name := range seen {
		values = append(values, name)
	}
	sort.Strings(values)
	return values, token, true
}

func pathExecutables(prefix string) []string {
	var names []string
	for _, dir := range filepath.SplitList(os.Getenv("PATH")) {
		if dir == "" {
			dir = "."
		}
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, entry := range entries {
			name := entry.Name()
			if !strings.HasPrefix(name, prefix) || entry.IsDir() {
				continue
			}
			info, err := os.Stat(filepath.Join(dir, name))
			if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0111 == 0 {
				continue
			}
			names = append(names, name)
		}
	}
	return names
}

func completeFiles(line []rune, cursor int) readline.Completions {
	values, prefix := fileCandidates(string(line[:cursor]))
	comps := readline.CompleteValues(values...)
	if prefix != "" {
		comps.PREFIX = prefix
	}
	return comps
}

func fileCandidates(text string) (values []string, prefix string) {
	token := text[lastWordStart(text):]

	// Strip Scheme decoration so "(cat \"CL" completes like "CL"
	prefix = strings.TrimLeft(token, "(")
	var quote byte
	body := prefix
	if len(prefix) > 0 && (prefix[0] == '"' || prefix[0] == '\'') {
		quote = prefix[0]
		prefix = prefix[1:]
		body = prefix
	} else {
		body = unescapeWord(prefix)
	}

	dir, base := filepath.Split(body)
	searchDir := dir
	if searchDir == "" {
		searchDir = "."
	}
	entries, err := os.ReadDir(expandHome(searchDir))
	if err != nil {
		return nil, prefix
	}

	for _, entry := range entries {
		name := entry.Name()
		if !strings.HasPrefix(name, base) {
			continue
		}
		// Hide dotfiles unless the user typed a leading dot
		if !strings.HasPrefix(base, ".") && strings.HasPrefix(name, ".") {
			continue
		}
		candidate := dir + name
		if entry.IsDir() {
			candidate += "/"
		}
		switch quote {
		case 0:
			candidate = escapeWord(candidate)
		case '"':
			candidate = strings.NewReplacer(`\`, `\\`, `"`, `\"`, "$", `\$`, "`", "\\`").Replace(candidate)
		}
		values = append(values, candidate)
	}
	return values, prefix
}

func lastWordStart(text string) int {
	start := 0
	var quote byte
	for i := 0; i < len(text); i++ {
		c := text[i]
		switch {
		case quote == '\'':
			if c == '\'' {
				quote = 0
			}
		case c == '\\':
			i++
		case quote == '"':
			if c == '"' {
				quote = 0
			}
		case c == '"' || c == '\'':
			quote = c
		case c == ' ' || c == '\t':
			start = i + 1
		}
	}
	return start
}

func unescapeWord(word string) string {
	var b strings.Builder
	for i := 0; i < len(word); i++ {
		if word[i] == '\\' && i+1 < len(word) {
			i++
		}
		b.WriteByte(word[i])
	}
	return b.String()
}

func escapeWord(word string) string {
	var b strings.Builder
	for i := 0; i < len(word); i++ {
		if strings.IndexByte(" \t\n\\\"'$`&|;()<>*?[]", word[i]) >= 0 {
			b.WriteByte('\\')
		}
		b.WriteByte(word[i])
	}
	return b.String()
}

func isSchemeLine(line string) bool {
	return strings.HasPrefix(line, "(") || strings.HasPrefix(line, "`") || strings.HasPrefix(line, "'(")
}

func globForm(pattern string) eval.Value {
	return eval.Value([]eval.Value{eval.Symbol("glob"), eval.String(pattern)})
}

func commandCallArgs(head string, meta eval.CommandMeta, tokens []cmdToken, userFn bool) (args []eval.Value, nPos int, wantHelp bool, err error) {
	var opts eval.Dictionary
	noMoreFlags := false
	for i := 0; i < len(tokens); i++ {
		t := tokens[i]
		switch {
		case t.quoted:
			args = append(args, eval.String(t.text))
			nPos++
		case t.glob:
			args = append(args, globForm(t.text))
			nPos++
		case !userFn && !noMoreFlags && t.text == "--":
			noMoreFlags = true
		case !userFn && !noMoreFlags && slices.Contains(meta.Operators, t.text):
			args = append(args, eval.String(t.text))
			nPos++
		case !noMoreFlags && strings.HasPrefix(t.text, ":") && len(t.text) > 1:
			args = append(args, eval.Keyword(t.text[1:]))
		case t.text == "_":
			args = append(args, eval.Symbol("_"))
			nPos++
		case !userFn && !noMoreFlags && isFlagWord(t.text):
			if t.text == "-h" || t.text == "--help" {
				return nil, 0, true, nil
			}
			opt, inline, ok := lookupFlag(meta.Options, t.text)
			if !ok {
				return nil, 0, false, fmt.Errorf("%s is a builtin and doesn't take %s (%s -h lists its flags) — prefix the line with ! for /bin/sh", head, t.text, head)
			}
			var val eval.Value = true
			if opt.Kind != eval.OptionBool {
				raw, hasInline := inline, inline != ""
				if !hasInline {
					if i+1 >= len(tokens) {
						return nil, 0, false, fmt.Errorf("%s: --%s expects a value", head, opt.Long)
					}
					i++
					raw = tokens[i].text
				}
				if val, err = flagValue(head, opt, raw); err != nil {
					return nil, 0, false, err
				}
			} else if inline != "" {
				return nil, 0, false, fmt.Errorf("%s: --%s is a flag and takes no value", head, opt.Long)
			}
			if opts == nil {
				opts = eval.Dictionary{}
			}
			opts[opt.Long] = val
		default:
			args = append(args, eval.String(t.text))
			nPos++
		}
	}
	if opts != nil {
		args = append(args, opts)
	}
	return args, nPos, false, nil
}

func instructionFlags(head string, meta eval.CommandMeta, tokens []cmdToken) (opts eval.Dictionary, rest []cmdToken, wantHelp bool, err error) {
	i := 0
	for i < len(tokens) {
		t := tokens[i]
		if t.quoted || !isFlagWord(t.text) {
			break
		}
		if t.text == "--" {
			i++
			break
		}
		if t.text == "-h" || t.text == "--help" {
			return nil, nil, true, nil
		}
		opt, inline, ok := lookupFlag(meta.Options, t.text)
		if !ok {
			return nil, nil, false, fmt.Errorf("%s is a builtin and doesn't take %s (%s -h lists its flags) — prefix the line with ! for /bin/sh", head, t.text, head)
		}
		var val eval.Value = true
		if opt.Kind != eval.OptionBool {
			raw, hasInline := inline, inline != ""
			if !hasInline {
				if i+1 >= len(tokens) {
					return nil, nil, false, fmt.Errorf("%s: --%s expects a value", head, opt.Long)
				}
				i++
				raw = tokens[i].text
			}
			if val, err = flagValue(head, opt, raw); err != nil {
				return nil, nil, false, err
			}
		} else if inline != "" {
			return nil, nil, false, fmt.Errorf("%s: --%s is a flag and takes no value", head, opt.Long)
		}
		if opts == nil {
			opts = eval.Dictionary{}
		}
		opts[opt.Long] = val
		i++
	}
	return opts, tokens[i:], false, nil
}

func isFlagWord(s string) bool {
	return len(s) > 1 && s[0] == '-' && !(s[1] >= '0' && s[1] <= '9')
}

func lookupFlag(table []eval.Option, word string) (eval.Option, string, bool) {
	if long, ok := strings.CutPrefix(word, "--"); ok {
		long, inline, _ := strings.Cut(long, "=")
		for _, o := range table {
			if o.Long == long {
				return o, inline, true
			}
		}
		return eval.Option{}, "", false
	}
	short := strings.TrimPrefix(word, "-")
	for _, o := range table {
		if o.Short != "" && o.Short == short {
			return o, "", true
		}
	}
	return eval.Option{}, "", false
}

func flagValue(head string, opt eval.Option, raw string) (eval.Value, error) {
	switch opt.Kind {
	case eval.OptionInt:
		n, err := strconv.Atoi(raw)
		if err != nil {
			return nil, fmt.Errorf("%s: --%s expects a number, got %q", head, opt.Long, raw)
		}
		return eval.Integer(n), nil
	case eval.OptionNumber:
		f, err := strconv.ParseFloat(raw, 64)
		if err != nil {
			return nil, fmt.Errorf("%s: --%s expects a number, got %q", head, opt.Long, raw)
		}
		return eval.Number(f), nil
	default:
		return eval.String(raw), nil
	}
}

func wantsHelpOnly(tokens []cmdToken) bool {
	return len(tokens) == 1 && !tokens[0].quoted &&
		(tokens[0].text == "-h" || tokens[0].text == "--help")
}

func runHelp(st *shellState, name string) (bool, int) {
	text, ok := eval.CommandHelp(name)
	if !ok {
		fmt.Println(colorRed+"Error:"+colorReset, "no builtin named "+name)
		return true, 1
	}
	fmt.Println(text)
	return true, 0
}

func arityOK(meta eval.CommandMeta, n int) bool {
	return n >= meta.MinArgs && (meta.MaxArgs < 0 || n <= meta.MaxArgs)
}

func arityErrorHint(head string, meta eval.CommandMeta) string {
	count := fmt.Sprintf("%d to %d arguments", meta.MinArgs, meta.MaxArgs)
	if meta.MaxArgs < 0 {
		count = fmt.Sprintf("at least %d arguments", meta.MinArgs)
		if meta.MinArgs == 1 {
			count = "at least 1 argument"
		}
	} else if meta.MinArgs == meta.MaxArgs {
		count = fmt.Sprintf("%d arguments", meta.MinArgs)
		if meta.MinArgs == 1 {
			count = "1 argument"
		}
	}
	return fmt.Sprintf("%s is a builtin taking %s — use '!%s ...' for the system command", head, count, head)
}

func expandAliases(lookup func(string) (string, bool), line string) string {
	seen := map[string]bool{}
	for {
		if line == "" || line[0] == '\'' || line[0] == '"' {
			return line
		}
		word, rest := line, ""
		if end := strings.IndexAny(line, " \t"); end >= 0 {
			word, rest = line[:end], line[end:]
		}
		exp, ok := lookup(word)
		if !ok || seen[word] {
			return line
		}
		seen[word] = true
		line = exp + rest
	}
}

func isUserFunction(chatInst *llm.Chat, name string) bool {
	v, err := chatInst.Env.Lookup(name)
	return err == nil && eval.IsUserFunction(v)
}

func runCommand(st *shellState, line string) int {
	if strings.TrimSpace(line) == "" {
		return 0
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	defer close(done)
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt)
	defer signal.Stop(sig)
	go func() {
		select {
		case <-sig:
			cancel()
		case <-done:
		}
	}()
	st.runningLine = line
	code := st.rsh.Run(ctx, line, os.Stdin, os.Stdout, os.Stderr)
	if hint, ok := fallthroughHint(st, line); ok {
		fmt.Println(colorGray + "hint: " + hint + colorReset)
	}
	return code
}

func fallthroughHint(st *shellState, line string) (string, bool) {
	for _, name := range st.rsh.NotFound() {
		meta, isCmd := eval.LookupCommand(name)
		isCmd = isCmd && meta.Command
		if !isCmd && !isUserFunction(st.chat, name) {
			continue
		}
		noun := "a builtin"
		if !isCmd {
			noun = "a defined function"
		}
		return fmt.Sprintf("%s is %s — sh-only syntax on this line sent it to the system shell; spell the call in parens to keep it Scheme", name, noun), true
	}
	return "", false
}

func loadPrelude(chatInst *llm.Chat) {
	path, err := core.PreludePath()
	if err != nil {
		return
	}
	src, err := os.ReadFile(path)
	if err != nil {
		return
	}
	forms, err := eval.ParseAll(string(src))
	if err != nil {
		fmt.Println(colorRed+"Error parsing ~/.rf.scm:"+colorReset, err)
		return
	}
	for _, form := range forms {
		if _, err := chatInst.Eval.Eval(form, chatInst.Env); err != nil {
			fmt.Println(colorRed+"Error in ~/.rf.scm:"+colorReset, err)
			return
		}
	}
}

func loadPreludeBlock(chatInst *llm.Chat, path string) {
	src, err := os.ReadFile(path)
	if err != nil {
		return
	}
	text := string(src)
	for _, markers := range [][2]string{
		{configBlockBegin, configBlockEnd},
	} {
		begin := strings.Index(text, markers[0])
		end := strings.Index(text, markers[1])
		if begin < 0 || end < begin {
			continue
		}
		forms, err := eval.ParseAll(text[begin:end])
		if err != nil {
			fmt.Println(colorRed+"Error parsing ~/.rf.scm:"+colorReset, err)
			return
		}
		for _, form := range forms {
			if _, err := chatInst.Eval.Eval(form, chatInst.Env); err != nil {
				fmt.Println(colorRed+"Error in ~/.rf.scm:"+colorReset, err)
				return
			}
		}
	}
}

type shellState struct {
	db      *sqlx.DB
	gs      *core.GraphStore
	chat    *llm.Chat
	mode    string
	history []*models.Message
	rec     *sessionRecorder
	hist    *sqliteHistory
	ctrl    *turnControl
	printer *turnPrinter

	lastAgentReply string

	echoSuppressed bool

	jobs []job

	rsh         *rsh.Session
	runningLine string
}

func userOnly(st *shellState, name string, fn func() error) eval.BuiltinFunc {
	return func(args []eval.Value, env *eval.Environment) (eval.Value, error) {
		if err := st.chat.Eval.RequireUser(name); err != nil {
			return nil, err
		}
		if len(args) != 0 {
			return nil, fmt.Errorf("%s expects no arguments", name)
		}
		return nil, fn()
	}
}

func registerShellCommands(st *shellState) {
	eval.Register("configure", "interactive setup: provider, model, thinking, claude code hooks", eval.CommandMeta{Command: true,
		Options: []eval.Option{{Long: "allowed-commands", Short: "c", Kind: eval.OptionBool,
			Doc: "edit the standing command allowlist (the agent-allow-commands grant) instead"}}})
	eval.Register("resume", "pick a past session and continue it", eval.CommandMeta{Command: true})
	eval.Register("compact", "summarize older turns to shrink the chat context", eval.CommandMeta{Command: true})
	eval.Register("clear", "clear the screen and start a fresh session", eval.CommandMeta{Command: true})
	st.chat.Env.Set("configure", eval.BuiltinFunc(func(args []eval.Value, _ *eval.Environment) (eval.Value, error) {
		if err := st.chat.Eval.RequireUser("configure"); err != nil {
			return nil, err
		}
		pos, opts, err := eval.ParseOptions("configure", args)
		if err != nil {
			return nil, err
		}
		if len(pos) != 0 {
			return nil, errors.New("configure takes no arguments")
		}
		if allowed, _ := opts["allowed-commands"].(bool); allowed {
			configureAllowedCommands(st.chat)
		} else {
			runSetupWizard(st.chat, false)
		}
		if path, err := core.PreludePath(); err == nil {
			loadPreludeBlock(st.chat, path)
		}
		return nil, nil
	}))
	registerHelpCommand(st)
	st.chat.Env.Set("resume", userOnly(st, "resume", func() error {
		return runResume(st)
	}))
	st.chat.Env.Set("compact", userOnly(st, "compact", func() error {
		return runCompact(st)
	}))
	st.chat.Env.Set("clear", userOnly(st, "clear", func() error {
		return runClear(st)
	}))
	eval.Register("fg", "resume the most recently stopped job in the foreground (user-only)", eval.CommandMeta{Command: true})
	st.chat.Env.Set("fg", userOnly(st, "fg", func() error {
		if len(st.jobs) == 0 {
			return errors.New("fg: no stopped jobs")
		}
		st.fg()
		return nil
	}))
	eval.Register("jobs", "stopped, background, and job-builtin children as rows", eval.CommandMeta{Command: true})
	st.chat.Env.Set("jobs", eval.BuiltinFunc(func(args []eval.Value, _ *eval.Environment) (eval.Value, error) {
		if len(args) != 0 {
			return nil, errors.New("jobs expects no arguments")
		}
		rows := st.jobRows()
		if len(rows) == 0 {
			return nil, nil
		}
		return rows, nil
	}))
	registerAgentBuiltin(st)
	registerConsoleBuiltins(st.chat)
	st.chat.Eval.SetForegroundRunner(st.runForeground)
	st.chat.Eval.SetConfirmer(confirmPrompt)
}

func confirmPrompt(action string) bool {
	fmt.Print(colorRed + "confirm?" + colorReset + " " + action + " [y/N] ")
	reader := bufio.NewReader(os.Stdin)
	line, err := reader.ReadString('\n')
	if err != nil {
		fmt.Println()
		return false
	}
	answer := strings.ToLower(strings.TrimSpace(line))
	return answer == "y" || answer == "yes"
}

func runShell(cmd *cobra.Command, args []string) {
	dbExisted := false
	if dbPath, err := core.DBPath(); err == nil {
		_, statErr := os.Stat(dbPath)
		dbExisted = statErr == nil
	}
	db, err := openMainDB()
	if err != nil {
		fmt.Fprintln(os.Stderr, "rf:", err)
		os.Exit(1)
	}
	defer db.Close()

	gs := core.NewGraphStore(db)

	chatInst, err := llm.NewChat(gs)
	if err != nil {
		slog.Error("Failed to create chat instance", slog.Any("error", err))
		os.Exit(1)
	}

	printer := &turnPrinter{}
	st := &shellState{
		db:      db,
		gs:      gs,
		chat:    chatInst,
		mode:    modeCommand,
		printer: printer,
		ctrl:    &turnControl{printer: printer},
		rsh:     rsh.NewSession(),
	}
	jobTTY := -1
	if fd, isTTY := ttyFD(); isTTY {
		jobTTY = fd
	}
	st.rsh.Jobs = &rsh.JobControl{
		TTY: jobTTY,
		OnStop: func(pgid int) {
			st.parkJob(pgid, st.runningLine)
		},
		OnBackground: func(pid int, cmd string) {
			fmt.Printf("%s[bg %d]%s  %s\n", colorGray, pid, colorReset, cmd)
		},
	}

	lightTerminal = detectLightTerminal()

	if _, isTTY := ttyFD(); isTTY && firstRunPending(dbExisted) {
		runSetupWizard(st.chat, true)
	}

	rl := readline.NewShell()
	rl.Prompt.Primary(func() string {
		setTerminalTitle(terminalTitle())
		return prompt(st.mode)
	})
	rl.Completer = st.complete

	_ = rl.Config.Set("enable-bracketed-paste", true)

	rl.AcceptMultiline = func(line []rune) bool {
		trimmed := strings.TrimSpace(string(line))
		accept := !isSchemeLine(trimmed) || eval.SchemeComplete(trimmed)
		if accept {
			rl.ClearInlineSuggestion()
		}
		return accept
	}

	attachChatHooks(st.chat, st.ctrl, st.printer)

	hist := newSQLiteHistory(db, &st.mode)
	rl.History.Add("history", hist)
	st.hist = hist
	st.rec = &sessionRecorder{db: db, session: hist.session}
	st.chat.Eval.SetSessionID(hist.session)

	rl.SyntaxHighlighter = func(line []rune) string {
		if cwd, err := os.Getwd(); err == nil {
			if s := hist.suggest(string(line), cwd, st.mode); s != "" {
				rl.SetInlineSuggestion(s)
			} else {
				rl.ClearInlineSuggestion()
			}
		}
		return smartChatHighlighter(line)
	}

	registerShellCommands(st)
	loadPrelude(st.chat)

	rl.Keymap.Register(map[string]func(){
		"switch-mode": func() {
			if st.mode == modeCommand {
				st.mode = modeAgent
			} else {
				st.mode = modeCommand
			}
		},
		"suggest-command": func() {
			buffer := strings.TrimSpace(string(*rl.Line()))
			if buffer == "" {
				return
			}
			ctx, cancel := context.WithTimeout(context.Background(), suggestTimeout)
			defer cancel()
			cwd, _ := os.Getwd()
			suggestion, err := st.chat.Suggest(ctx, buffer, cwd)
			if err != nil || suggestion == "" {
				return
			}
			rl.Line().Set([]rune(suggestion)...)
			rl.Cursor().Set(rl.Line().Len())
			st.mode = modeCommand
		},
	})
	for _, km := range []string{"emacs", "vi", "vi-insert", "vi-command", "vi-move"} {
		if rl.Config.Binds[km] != nil {
			rl.Config.Binds[km]["\x1b[Z"] = inputrc.Bind{Action: "switch-mode"}
			rl.Config.Binds[km]["\x00"] = inputrc.Bind{Action: "switch-mode"}
			rl.Config.Binds[km]["\x0b"] = inputrc.Bind{Action: "suggest-command"}
		}
	}

	signal.Ignore(syscall.SIGTTOU)
	tstpCh := make(chan os.Signal, 1)
	signal.Notify(tstpCh, syscall.SIGTSTP)
	defer signal.Stop(tstpCh)
	go func() {
		for range tstpCh {
		}
	}()
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt)
	defer signal.Stop(sigCh)
	go func() {
		for range sigCh {
			eval.Interrupt()
		}
	}()

	printSplash()
	pad := false
	warnedJobs := false
	for {
		if pad && !st.echoSuppressed {
			fmt.Println()
		}
		pad = true
		st.echoSuppressed = false
		resetTerminalTitle()
		line, err := rl.Readline()
		if err != nil {
			if errors.Is(err, readline.ErrInterrupt) {
				// ^C abandons the line bash-style; only ctrl+d quits.
				continue
			}
			fmt.Println()
			if len(st.jobs) > 0 && !warnedJobs {
				warnedJobs = true
				fmt.Println("there are stopped jobs — fg resumes, exiting again hangs them up")
				continue
			}
			break
		}
		eval.ClearInterrupt()
		st.lastAgentReply = ""
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		st.chat.Eval.SetInFlightHistory(hist.lastID)
		if st.mode == modeCommand {
			expanded, used, err := hist.expandHistory(line, st.mode)
			if err != nil {
				fmt.Println(colorRed+"Error:"+colorReset, err)
				hist.setLastExit(1)
				continue
			}
			if used {
				fmt.Println(colorGray + expanded + colorReset)
				hist.recordExpansion(expanded)
				line = expanded
			}
		}
		if line == "(exit)" || line == "(quit)" || (st.mode == modeCommand && (line == "exit" || line == "quit")) {
			if len(st.jobs) > 0 && !warnedJobs {
				warnedJobs = true
				fmt.Println("there are stopped jobs — fg resumes, exiting again hangs them up")
				continue
			}
			break
		}
		if isSchemeLine(line) {
			exprs, err := eval.ParseAll(line)
			if err != nil {
				fmt.Println("Parse error:", err)
				hist.setLastExit(1)
				continue
			}
			result, err := st.chat.Eval.EvalAll(exprs, st.chat.Env)
			if err != nil {
				fmt.Println("Error:", err)
				hist.setLastExit(1)
				continue
			}
			if s, isStream := result.(*eval.Stream); isStream {
				materialized, streamErr := drainStream(s)
				st.chat.Eval.RecordResult(materialized)
				if streamErr != nil {
					fmt.Println("Error:", streamErr)
					hist.setLastExit(1)
					continue
				}
				hist.setLastExit(0)
				continue
			}
			hist.setLastExit(0)
			st.chat.Eval.RecordResult(result)
			if s, isStr := result.(eval.String); isStr && st.lastAgentReply != "" && string(s) == st.lastAgentReply {
				st.echoSuppressed = true
				continue
			}
			if result != nil {
				// Lists of dictionaries (ls, search-items, ...) render as tables
				if tbl, ok := renderTable(result); ok {
					fmt.Println(tbl)
				} else {
					fmt.Println(eval.PrintValue(result))
				}
			}
			continue
		}

		if strings.HasPrefix(line, "!") {
			hist.setLastExit(runCommand(st, strings.TrimSpace(line[1:])))
			continue
		}

		if st.mode == modeCommand {
			if strings.ContainsRune(line, '\n') {
				hist.setLastExit(runCommand(st, line))
				continue
			}
			line = expandAliases(st.chat.Eval.LookupAlias, line)
			if cl, ok := parseCommandLine(line, st.rsh.Environ()); ok {
				if handled, code := dispatchCommandLine(st, line, cl); handled {
					hist.setLastExit(code)
					continue
				}
			}
			hist.setLastExit(runCommand(st, line))
			continue
		}

		var failed bool
		st.history, failed = runChat(st.chat, st.ctrl, st.printer, st.rec, line, st.history)
		if failed {
			hist.setLastExit(1)
		} else {
			hist.setLastExit(0)
			maybeAutoCompact(st)
		}
		pad = false // the last chat block already printed its blank line
	}
	st.hangupJobs()
}
