package cmd

import (
	"fmt"
	"os"
	"slices"
	"strings"

	"github.com/retrofilter/rf/eval"
	"mvdan.cc/sh/v3/expand"
	"mvdan.cc/sh/v3/syntax"
)

type cmdToken struct {
	text   string
	quoted bool
	glob   bool // unquoted glob metachars (*?[]) — a pattern, not a literal
	start  int
	end    int
}

type cmdRedir struct {
	op     string // the operator as spelled: > >> < 2> >& &> << <<< …
	fd     string // the N of N>, empty when absent
	target string // the operand, expanded (a heredoc's delimiter)
	bail   string // an unsupported expansion in the operand
	start  int
	end    int
}

type cmdStage struct {
	start  int
	end    int
	tokens []cmdToken
	bail   string
	redirs []cmdRedir
}

type cmdLine struct {
	stages []cmdStage
}

func parseCommandLine(line string, env expand.Environ) (*cmdLine, bool) {
	f, err := syntax.NewParser(syntax.Variant(syntax.LangBash)).Parse(strings.NewReader(line), "")
	if err != nil || len(f.Stmts) != 1 {
		return nil, false
	}
	var leaves []*syntax.Stmt
	if !flattenPipe(f.Stmts[0], &leaves) {
		return nil, false
	}
	if env == nil {
		env = expand.ListEnviron(os.Environ()...)
	}
	cl := &cmdLine{}
	for _, st := range leaves {
		cl.stages = append(cl.stages, leafStage(st, env))
	}
	return cl, true
}

func flattenPipe(st *syntax.Stmt, out *[]*syntax.Stmt) bool {
	if st.Background || st.Negated || st.Coprocess {
		return false
	}
	if b, ok := st.Cmd.(*syntax.BinaryCmd); ok {
		if b.Op != syntax.Pipe || len(st.Redirs) > 0 {
			return false
		}
		return flattenPipe(b.X, out) && flattenPipe(b.Y, out)
	}
	*out = append(*out, st)
	return true
}

func leafStage(st *syntax.Stmt, env expand.Environ) cmdStage {
	s := cmdStage{start: int(st.Pos().Offset()), end: int(st.End().Offset())}
	for _, r := range st.Redirs {
		s.redirs = append(s.redirs, redirOf(r, env))
	}
	call, ok := st.Cmd.(*syntax.CallExpr)
	if !ok || len(call.Assigns) > 0 || len(call.Args) == 0 {
		return s
	}
	for _, w := range call.Args {
		tok, bail := wordToken(w, env)
		if bail != "" && s.bail == "" {
			s.bail = bail
		}
		s.tokens = append(s.tokens, tok)
	}
	return s
}

func redirOf(r *syntax.Redirect, env expand.Environ) cmdRedir {
	rd := cmdRedir{op: r.Op.String(), start: int(r.Pos().Offset()), end: int(r.End().Offset())}
	if r.N != nil {
		rd.fd = r.N.Value
	}
	if r.Word != nil {
		tok, bail := wordToken(r.Word, env)
		rd.target, rd.bail = tok.text, bail
	}
	return rd
}

func wordToken(w *syntax.Word, env expand.Environ) (cmdToken, string) {
	tok := cmdToken{start: int(w.Pos().Offset()), end: int(w.End().Offset())}
	if bail := classifyParts(w.Parts, false, &tok); bail != "" {
		return tok, bail
	}
	tok.glob = tok.glob && !tok.quoted
	cfg := &expand.Config{Env: env}
	var text strings.Builder
	for i, p := range w.Parts {
		lit, isLit := p.(*syntax.Lit)
		if isLit && i > 0 {
			// Only a word's first literal is subject to tilde expansion.
			text.WriteString(unescape(lit.Value))
			continue
		}
		part, err := expand.Literal(cfg, &syntax.Word{Parts: []syntax.WordPart{p}})
		if err != nil {
			return tok, "shell expansion"
		}
		if isLit {
			part = unescape(part)
		}
		text.WriteString(part)
	}
	tok.text = text.String()
	return tok, ""
}

func unescape(s string) string {
	if !strings.Contains(s, "\\") {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+1 < len(s) {
			i++
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

func classifyParts(parts []syntax.WordPart, inDouble bool, tok *cmdToken) string {
	for _, p := range parts {
		switch x := p.(type) {
		case *syntax.Lit:
			if inDouble {
				continue
			}
			if strings.Contains(x.Value, "{") && strings.Contains(x.Value, "}") {
				return "brace expansion"
			}
			if strings.Contains(x.Value, "\\") {
				tok.quoted = true
			} else if strings.ContainsAny(x.Value, "*?[") {
				tok.glob = true
			}
		case *syntax.SglQuoted:
			tok.quoted = true
		case *syntax.DblQuoted:
			tok.quoted = true
			if bail := classifyParts(x.Parts, true, tok); bail != "" {
				return bail
			}
		case *syntax.ParamExp:
			tok.quoted = true
		case *syntax.CmdSubst:
			return "command substitution"
		case *syntax.ArithmExp:
			return "arithmetic expansion"
		case *syntax.ProcSubst:
			return "process substitution"
		case *syntax.ExtGlob:
			return "extended glob"
		case *syntax.BraceExp:
			return "brace expansion"
		default:
			return "shell expansion"
		}
	}
	return ""
}

func splitSimpleCommand(line string) ([]cmdToken, bool) {
	cl, ok := parseCommandLine(line, nil)
	if !ok || len(cl.stages) != 1 {
		return nil, false
	}
	s := cl.stages[0]
	if s.tokens == nil || s.bail != "" || len(s.redirs) > 0 {
		return nil, false
	}
	return s.tokens, true
}

func dispatchCommandLine(st *shellState, line string, cl *cmdLine) (bool, int) {
	stages := cl.stages
	last := len(stages) - 1
	fail := func(err error) (bool, int) {
		fmt.Println(colorRed+"Error:"+colorReset, err)
		return true, 1
	}
	isScheme := func(s cmdStage, first bool) bool {
		if s.tokens == nil || s.tokens[0].quoted {
			return false
		}
		head := s.tokens[0].text
		if first && slices.ContainsFunc(s.redirs, func(r cmdRedir) bool { return r.op == "<" }) {
			first = false
		}
		if meta, ok := eval.LookupCommand(head); ok && (meta.Command || (!first && meta.Stage)) {
			return meta.Globs || meta.Instruction || !hasGlobArg(s.tokens[1:])
		}
		return isUserFunction(st.chat, head)
	}
	rawStage := func(from, to int) eval.Value {
		return eval.Value([]eval.Value{eval.Symbol("sh"),
			eval.String(strings.TrimSpace(line[stages[from].start:stages[to-1].end]))})
	}

	commit := -1
	for i := range stages {
		if isScheme(stages[i], i == 0) {
			commit = i
			break
		}
	}
	if commit < 0 {
		return false, 0
	}
	form := []eval.Value{eval.Symbol("pipe")}
	if commit > 0 {
		form = append(form, rawStage(0, commit))
	}

	var out *cmdRedir // a > or >> on the last stage, applied after the form
	for i := commit; i <= last; i++ {
		s := stages[i]
		if !isScheme(s, i == 0) {
			j := i
			for j+1 <= last && !isScheme(stages[j+1], false) {
				j++
			}
			form = append(form, rawStage(i, j+1))
			i = j
			continue
		}
		head := s.tokens[0].text
		meta, isCmd := eval.LookupCommand(head)
		isUser := !isCmd && isUserFunction(st.chat, head)
		if s.bail != "" {
			return fail(fmt.Errorf("%s is a builtin — %s isn't run for its arguments; spell the call in parens, or prefix the line with ! for the shell", head, s.bail))
		}
		var args []eval.Value
		var nPos int
		var err error
		if meta.Instruction {
			if wantsHelpOnly(s.tokens[1:]) {
				return runHelp(st, head)
			}
			rest := s.tokens[1:]
			var opts eval.Dictionary
			if len(meta.Options) > 0 {
				var wantHelp bool
				opts, rest, wantHelp, err = instructionFlags(head, meta, rest)
				if wantHelp {
					return runHelp(st, head)
				}
				if err != nil {
					return fail(err)
				}
			}
			if len(rest) > 0 {
				for _, r := range s.redirs {
					if r.start < rest[0].start {
						return fail(fmt.Errorf("%s: put %s after the instruction — the text from the first non-flag word to the end of the line is the instruction", head, r.op))
					}
				}
				args = []eval.Value{eval.String(line[rest[0].start:s.end])}
			} else if len(s.redirs) > 0 {
				args = []eval.Value{eval.String(line[s.redirs[0].start:s.end])}
			}
			nPos = len(args)
			if opts != nil {
				args = append(args, opts)
			}
		} else {
			var wantHelp bool
			args, nPos, wantHelp, err = commandCallArgs(head, meta, s.tokens[1:], isUser)
			if wantHelp {
				return runHelp(st, head)
			}
			if err != nil {
				return fail(err)
			}
			in, o, err := stageRedirects(head, meta, isCmd, s, i == commit && commit == 0, i == last)
			if err != nil {
				return fail(err)
			}
			if in != "" {
				form = append(form, eval.Value([]eval.Value{eval.Symbol("cat"), eval.String(in)}))
			}
			if o != nil {
				out = o
			}
			if i == 0 && in == "" && isCmd && !arityOK(meta, nPos) {
				return fail(fmt.Errorf("%s", arityErrorHint(head, meta)))
			}
		}
		form = append(form, eval.Value(append([]eval.Value{eval.Symbol(head)}, args...)))
	}
	if out != nil {
		verb := "write-file"
		if out.op == ">>" {
			verb = "append-file"
		}
		form = append(form, eval.Value([]eval.Value{eval.Symbol(verb), eval.String(out.target)}))
	}

	// One stage, nothing threaded: the plain call, not a one-stage pipe.
	var expr eval.Value = form
	if len(form) == 2 {
		expr = form[1]
	}
	result, err := st.chat.Eval.Eval(expr, st.chat.Env)
	if err != nil {
		return fail(err)
	}
	return true, printCommandResult(st, result)
}

func stageRedirects(head string, meta eval.CommandMeta, isCmd bool, s cmdStage, first, last bool) (in string, out *cmdRedir, err error) {
	for i := range s.redirs {
		r := &s.redirs[i]
		if r.bail != "" {
			return "", nil, fmt.Errorf("%s: %s isn't run for a redirect target", head, r.bail)
		}
		switch {
		case (r.op == ">" || r.op == ">>" || r.op == ">|") && (r.fd == "" || r.fd == "1"):
			if head == "where" {
				return "", nil, fmt.Errorf("where compares with -gt -lt -ge -le (and = !=); %s here is a redirect to a file — where size -gt 100MB", r.op)
			}
			if !last {
				return "", nil, fmt.Errorf("%s: %s %s would starve the pipe — redirect on the last stage", head, r.op, r.target)
			}
			if out != nil {
				return "", nil, fmt.Errorf("%s: one output redirect per stage", head)
			}
			if r.op == ">|" {
				r.op = ">"
			}
			out = r
		case r.op == "<" && (r.fd == "" || r.fd == "0"):
			if head == "where" {
				return "", nil, fmt.Errorf("where compares with -gt -lt -ge -le (and = !=); < here is a redirect from a file")
			}
			if !first {
				return "", nil, fmt.Errorf("%s: < %s on a piped stage — its input is the pipe", head, r.target)
			}
			if isCmd && !meta.Stage {
				return "", nil, fmt.Errorf("%s takes no piped input, so < %s has nothing to feed", head, r.target)
			}
			if in != "" {
				return "", nil, fmt.Errorf("%s: one input redirect per stage", head)
			}
			in = r.target
		default:
			return "", nil, fmt.Errorf("%s is a builtin with one output, its value — %s%s doesn't apply; > and >> write the value to a file, or prefix the line with ! for the shell", head, r.fd, r.op)
		}
	}
	return in, out, nil
}

func hasGlobArg(tokens []cmdToken) bool {
	return slices.ContainsFunc(tokens, func(t cmdToken) bool { return t.glob && !t.quoted })
}
