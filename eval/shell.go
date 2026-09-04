package eval

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/rivo/uniseg"

	"github.com/retrofilter/rf/rsh"
)

func grepMark(pat String, elem Value) Value {
	dict, isDict := elem.(Dictionary)
	if !isDict {
		return elem
	}
	marked := make(Dictionary, len(dict)+1)
	maps.Copy(marked, dict)
	marked["_match"] = pat
	return marked
}

func grepStream(m *grepMatcher, pat String, src *Stream) *Stream {
	next := func() (Value, bool, error) {
		for {
			v, ok, err := src.Next()
			if err != nil || !ok {
				return nil, false, err
			}
			if grepMatch(m, v) {
				return grepMark(pat, v), true, nil
			}
		}
	}
	out := newStream(next, src.Close)
	out.lines = src.Lines()
	out.userCode = src.userCode
	return out
}

func grepRowStream(m *grepMatcher, pat String, src *Stream, copt grepCtxOpt) *Stream {
	tracker := &grepContext{opt: copt}
	lineNum := 0
	var pending []grepEmit
	next := func() (Value, bool, error) {
		for {
			if len(pending) > 0 {
				e := pending[0]
				pending = pending[1:]
				return grepLineRow(pat, e), true, nil
			}
			v, ok, err := src.Next()
			if err != nil || !ok {
				return nil, false, err
			}
			lineNum++
			text := lineText(v)
			pending = tracker.feed(lineNum, text, m.MatchString(text))
		}
	}
	out := newStream(next, src.Close)
	out.userCode = src.userCode
	return out
}

func grepLineRow(pat String, e grepEmit) Value {
	row := Dictionary{"line": Integer(e.num), "text": String(e.text)}
	if e.matched {
		row["_match"] = pat
	}
	return row
}

func grepMatch(m *grepMatcher, v Value) bool {
	switch val := v.(type) {
	case String:
		return m.MatchString(string(val))
	case Dictionary:
		for _, dv := range val {
			if grepMatch(m, dv) {
				return true
			}
		}
		return false
	default:
		return m.MatchString(PrintValue(v))
	}
}

// ApprovalFunc decides whether a gated action (rm, mv, sh, cat, ls, ...)
// may proceed. The action string is the human-readable form, e.g. `rm "notes.txt"`.
type ApprovalFunc func(action string) bool

type approvalGate struct {
	fn         ApprovalFunc
	allowDir   string // resolved absolute dir the assistant may touch; "" = none
	allowWrite bool   // grant covers writes too, not just reads
}

func (g *approvalGate) require(action string) error {
	if g.fn == nil {
		return nil
	}
	if !g.fn(action) {
		return fmt.Errorf("%s: denied by user", action)
	}
	return nil
}

func shellBuiltins(env *Environment, approval *approvalGate) {
	global := env

	Register("pwd", "the current working directory", CommandMeta{})
	env.Set("pwd", BuiltinFunc(func(args []Value, env *Environment) (Value, error) {
		if len(args) != 0 {
			return nil, errors.New("pwd expects no arguments")
		}
		cwd, err := os.Getwd()
		if err != nil {
			return nil, err
		}
		return String(cwd), nil
	}))

	env.SetBuiltin("cd", "change the working directory (default ~), returning the new cwd", BuiltinFunc(func(args []Value, env *Environment) (Value, error) {
		if len(args) > 1 {
			return nil, errors.New("cd expects 0 or 1 arguments: (cd [path])")
		}
		dir := ""
		if len(args) == 1 {
			s, ok := args[0].(String)
			if !ok {
				return nil, errors.New("cd expects a string path")
			}
			dir = expandHome(string(s))
		} else {
			home, err := os.UserHomeDir()
			if err != nil {
				return nil, err
			}
			dir = home
		}
		if err := approval.requireRead(fmt.Sprintf("cd %q", dir), dir); err != nil {
			return nil, err
		}
		if err := os.Chdir(dir); err != nil {
			return nil, err
		}
		cwd, err := os.Getwd()
		if err != nil {
			return nil, err
		}
		return String(cwd), nil
	}))

	lsOptions := []Option{
		{Long: "all", Short: "a", Kind: OptionBool, Doc: "include dotfiles"},
	}
	Register("ls", "list a directory as rows", CommandMeta{Usage: "[path]", Options: lsOptions})
	lsFnFor := func(name string) BuiltinFunc {
		return func(args []Value, env *Environment) (Value, error) {
			pos, opts, err := ParseOptions(name, args)
			if err != nil {
				return nil, err
			}
			showHidden := OptBool(opts, "all")
			var path Value
			for _, arg := range pos {
				switch arg.(type) {
				case String, []Value:
					if path != nil {
						return nil, fmt.Errorf("%s expects at most one path", name)
					}
					path = arg
				default:
					return nil, fmt.Errorf("%s expects a path or glob list and options: (%s [path] [{:all #t}])", name, name)
				}
			}
			if list, ok := path.([]Value); ok {
				paths, err := pathArgList(name, list)
				if err != nil {
					return nil, err
				}
				if err := approval.requireRead(readAction(name, paths), paths...); err != nil {
					return nil, err
				}
				result := make([]Value, 0, len(paths))
				for _, p := range paths {
					info, err := os.Stat(p)
					if err != nil {
						return nil, err
					}
					result = append(result, fileInfoDict(p, info))
				}
				return result, nil
			}
			dir := "."
			if s, ok := path.(String); ok {
				dir = expandHome(string(s))
			}
			if err := approval.requireRead(fmt.Sprintf("%s %q", name, dir), dir); err != nil {
				return nil, err
			}
			// Like ls(1), a non-directory path lists just that entry
			if info, err := os.Stat(dir); err == nil && !info.IsDir() {
				return []Value{fileInfoDict(dir, info)}, nil
			}
			entries, err := os.ReadDir(dir)
			if err != nil {
				return nil, err
			}
			result := make([]Value, 0, len(entries))
			for _, entry := range entries {
				if !showHidden && strings.HasPrefix(entry.Name(), ".") {
					continue
				}
				info, err := entry.Info()
				if err != nil {
					continue
				}
				result = append(result, fileInfoDict(entry.Name(), info))
			}
			return result, nil
		}
	}
	env.Set("ls", lsFnFor("ls"))

	Register("dir", "list a directory as rows, ls under a non-shadowing name",
		CommandMeta{Command: true, MaxArgs: 1, Globs: true, Usage: "[path]", Options: lsOptions})
	env.Set("dir", lsFnFor("dir"))

	Register("stat", "a file's metadata as a row", CommandMeta{})
	env.Set("stat", BuiltinFunc(func(args []Value, env *Environment) (Value, error) {
		if len(args) != 1 {
			return nil, errors.New("stat expects 1 argument")
		}
		paths, err := pathArgList("stat", args[0])
		if err != nil {
			return nil, err
		}
		if err := approval.requireRead(readAction("stat", paths), paths...); err != nil {
			return nil, err
		}
		rows := make([]Value, 0, len(paths))
		for _, path := range paths {
			info, err := os.Stat(path)
			if err != nil {
				return nil, err
			}
			dict := fileInfoDict(info.Name(), info)
			dict["path"] = String(path)
			rows = append(rows, dict)
		}
		if _, isList := args[0].([]Value); !isList {
			return rows[0], nil
		}
		return rows, nil
	}))

	Register("cat", "a file's contents as a line stream", CommandMeta{Options: []Option{
		{Long: "from", Kind: OptionInt, Placeholder: "N", Doc: "start at line N (1-based)"},
		{Long: "to", Kind: OptionInt, Placeholder: "N", Doc: "stop after line N (inclusive; reading stops there)"},
		{Long: "line-numbers", Kind: OptionBool, Doc: "emit {:line :text} rows instead of bare lines"},
	}})
	env.Set("cat", BuiltinFunc(func(args []Value, env *Environment) (Value, error) {
		pos, opts, err := ParseOptions("cat", args)
		if err != nil {
			return nil, err
		}
		if len(pos) != 1 {
			return nil, errors.New("cat expects 1 argument: (cat path [options])")
		}
		from := OptInt(opts, "from", 1)
		to := OptInt(opts, "to", 0)
		if from < 1 || (to != 0 && to < from) {
			return nil, errors.New("cat: :from starts at 1 and :to must not precede it")
		}
		numbered := OptBool(opts, "line-numbers")
		ranged := from > 1 || to != 0 || numbered
		if _, isList := pos[0].([]Value); isList {
			paths, err := pathArgList("cat", pos[0])
			if err != nil {
				return nil, err
			}
			if err := approval.requireRead(readAction("cat", paths), paths...); err != nil {
				return nil, err
			}
			src := catFiles(paths)
			if ranged {
				return catRangeStream(src, from, to, numbered), nil
			}
			return src, nil
		}
		path, err := onePath("cat", pos)
		if err != nil {
			return nil, err
		}
		if err := approval.requireRead(fmt.Sprintf("cat %q", path), path); err != nil {
			return nil, err
		}
		f, err := os.Open(path)
		if err != nil {
			return nil, err
		}
		if info, err := f.Stat(); err == nil && info.IsDir() {
			f.Close()
			return nil, fmt.Errorf("cat: %s is a directory", path)
		}
		src := streamFromReader(f, f.Close)
		if ranged {
			return catRangeStream(src, from, to, numbered), nil
		}
		return src, nil
	}))

	Register("write-file", "write a value to a file — the pipeline's > redirection", CommandMeta{Stage: true})
	env.Set("write-file", BuiltinFunc(func(args []Value, env *Environment) (Value, error) {
		return writeFileImpl("write-file", args, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, approval)
	}))

	Register("append-file", "append a value to a file — the pipeline's >> redirection", CommandMeta{Stage: true})
	env.Set("append-file", BuiltinFunc(func(args []Value, env *Environment) (Value, error) {
		return writeFileImpl("append-file", args, os.O_APPEND|os.O_CREATE|os.O_WRONLY, approval)
	}))

	Register("cp", "copy files", CommandMeta{})
	env.Set("cp", BuiltinFunc(func(args []Value, env *Environment) (Value, error) {
		srcs, dst, multi, err := srcsAndDst("cp", args)
		if err != nil {
			return nil, err
		}
		dstIsDir := false
		if info, err := os.Stat(dst); err == nil && info.IsDir() {
			dstIsDir = true
		}
		targetPaths := make([]string, len(srcs))
		copies := make([]string, len(srcs))
		for i, src := range srcs {
			target := dst
			if dstIsDir {
				target = filepath.Join(dst, filepath.Base(src))
			}
			targetPaths[i] = target
			copies[i] = fmt.Sprintf("cp %q %q", src, target)
		}
		if err := approval.check(strings.Join(copies, "\n"), srcs, targetPaths); err != nil {
			return nil, err
		}
		targets := make([]Value, 0, len(srcs))
		for i, src := range srcs {
			if err := copyFile(src, targetPaths[i]); err != nil {
				return nil, err
			}
			targets = append(targets, String(targetPaths[i]))
		}
		if !multi {
			return targets[0], nil
		}
		return Value(targets), nil
	}))

	Register("mv", "move or rename files", CommandMeta{})
	env.Set("mv", BuiltinFunc(func(args []Value, env *Environment) (Value, error) {
		srcs, dst, multi, err := srcsAndDst("mv", args)
		if err != nil {
			return nil, err
		}
		dstIsDir := false
		if info, err := os.Stat(dst); err == nil && info.IsDir() {
			dstIsDir = true
		}
		targets := make([]Value, 0, len(srcs))
		var moves []string
		for _, src := range srcs {
			target := dst
			if dstIsDir {
				target = filepath.Join(dst, filepath.Base(src))
			}
			targets = append(targets, String(target))
			moves = append(moves, fmt.Sprintf("mv %q %q", src, target))
		}
		if len(srcs) > 0 {
			// A move deletes the source, so both ends need the write grant.
			writes := make([]string, 0, len(srcs)*2)
			for i, src := range srcs {
				writes = append(writes, src, string(targets[i].(String)))
			}
			if err := approval.requireWrite(strings.Join(moves, "\n"), writes...); err != nil {
				return nil, err
			}
		}
		for i, src := range srcs {
			if err := os.Rename(src, string(targets[i].(String))); err != nil {
				return nil, err
			}
		}
		if !multi {
			return targets[0], nil
		}
		return Value(targets), nil
	}))

	Register("rm", "delete files (:recursive deletes directories)", CommandMeta{
		Usage: "path",
		Options: []Option{{Long: "recursive", Short: "r", Kind: OptionBool,
			Doc: "delete directories and their contents"}}})
	env.Set("rm", BuiltinFunc(func(args []Value, env *Environment) (Value, error) {
		pos, opts, err := ParseOptions("rm", args)
		if err != nil {
			return nil, err
		}
		if len(pos) != 1 {
			return nil, errors.New("rm expects a path: (rm path [:recursive])")
		}
		paths, err := pathArgList("rm", pos[0])
		if err != nil {
			return nil, err
		}
		recursive := OptBool(opts, "recursive")
		if len(paths) == 0 {
			return true, nil // empty expansion: nothing to delete
		}
		quoted := make([]string, len(paths))
		for i, p := range paths {
			quoted[i] = fmt.Sprintf("%q", p)
		}
		action := "rm " + strings.Join(quoted, " ")
		if recursive {
			action = "rm -r " + strings.Join(quoted, " ")
		}
		if err := approval.requireWrite(action, paths...); err != nil {
			return nil, err
		}
		for _, path := range paths {
			if recursive {
				if err := os.RemoveAll(path); err != nil {
					return nil, err
				}
			} else if err := os.Remove(path); err != nil {
				return nil, err
			}
		}
		return true, nil
	}))

	Register("mkdir", "create a directory, parents included", CommandMeta{})
	env.Set("mkdir", BuiltinFunc(func(args []Value, env *Environment) (Value, error) {
		path, err := onePath("mkdir", args)
		if err != nil {
			return nil, err
		}
		if err := approval.requireWrite(fmt.Sprintf("mkdir %q", path), path); err != nil {
			return nil, err
		}
		if err := os.MkdirAll(path, 0755); err != nil {
			return nil, err
		}
		return String(path), nil
	}))

	env.SetBuiltin("exists?", "whether a path exists", BuiltinFunc(func(args []Value, env *Environment) (Value, error) {
		path, err := onePath("exists?", args)
		if err != nil {
			return nil, err
		}
		_, statErr := os.Stat(path)
		return statErr == nil, nil
	}))

	env.SetBuiltin("glob", "paths matching a pattern as a list (** recurses, :all includes hidden)", BuiltinFunc(func(args []Value, env *Environment) (Value, error) {
		pattern := ""
		havePattern := false
		showHidden := false
		for _, arg := range args {
			switch a := arg.(type) {
			case String:
				if havePattern {
					return nil, errors.New("glob expects one pattern")
				}
				pattern = expandHome(string(a))
				havePattern = true
			case Keyword:
				switch string(a) {
				case "all":
					showHidden = true
				default:
					return nil, fmt.Errorf("glob: unknown option :%s (supported: :all)", string(a))
				}
			default:
				return nil, errors.New("glob expects a pattern and keyword options: (glob pattern [:all])")
			}
		}
		if !havePattern {
			return nil, errors.New("glob expects a pattern")
		}
		if err := approval.requireRead(fmt.Sprintf("glob %q", pattern), globBase(pattern)); err != nil {
			return nil, err
		}
		var matches []string
		var err error
		if strings.Contains(pattern, "**") {
			matches, err = globRecursive(pattern, showHidden)
		} else {
			matches, err = filepath.Glob(pattern)
			if err == nil && !showHidden && !strings.HasPrefix(filepath.Base(pattern), ".") {
				kept := matches[:0]
				for _, m := range matches {
					if !strings.HasPrefix(filepath.Base(m), ".") {
						kept = append(kept, m)
					}
				}
				matches = kept
			}
		}
		if err != nil {
			return nil, err
		}
		result := make([]Value, len(matches))
		for i, m := range matches {
			result[i] = String(m)
		}
		return result, nil
	}))

	Register("wc", "line, word and byte counts as a row", CommandMeta{})
	env.Set("wc", BuiltinFunc(func(args []Value, env *Environment) (Value, error) {
		if len(args) == 1 {
			switch in := normalizeList(args[0]).(type) {
			case String:
				// File path
				path := expandHome(string(in))
				if err := approval.requireRead(fmt.Sprintf("wc %q", path), path); err != nil {
					return nil, err
				}
				data, err := os.ReadFile(path)
				if err != nil {
					return nil, err
				}
				return Dictionary{
					"path":  String(path),
					"lines": Integer(bytes.Count(data, []byte("\n"))),
					"words": Integer(len(strings.Fields(string(data)))),
					"bytes": Integer(len(data)),
				}, nil
			case *Stream:
				// Stream of lines — count them
				lineCount, wordCount, byteCount := 0, 0, 0
				for {
					v, more, err := in.Next()
					if err != nil {
						return nil, err
					}
					if !more {
						break
					}
					line := lineText(v)
					lineCount++
					wordCount += len(strings.Fields(line))
					byteCount += len(line) + 1 // +1 for newline
				}
				return Dictionary{
					"lines": Integer(lineCount),
					"words": Integer(wordCount),
					"bytes": Integer(byteCount),
				}, nil
			case []Value:
				// List (e.g. from ls) — count rows
				return Dictionary{
					"lines": Integer(len(in)),
				}, nil
			}
		}
		if len(args) == 0 {
			return nil, errors.New("wc expects a file path, a stream, or a list")
		}
		return nil, errors.New("wc expects 1 argument: (wc path-or-input)")
	}))

	Register("grep", "filter lines or rows by regex — a bare string input names a file or directory", CommandMeta{Options: []Option{
		{Long: "before", Kind: OptionInt, Placeholder: "N", Doc: "also emit N lines before each match (grep -B; line inputs)"},
		{Long: "after", Kind: OptionInt, Placeholder: "N", Doc: "also emit N lines after each match (grep -A; line inputs)"},
		{Long: "context", Kind: OptionInt, Placeholder: "N", Doc: "N lines of context both sides (grep -C; sets before and after)"},
		{Long: "line-numbers", Kind: OptionBool, Doc: "emit {:line :text} rows instead of bare lines (implied by context; directory greps always carry line numbers)"},
	}})
	env.Set("grep", BuiltinFunc(func(args []Value, env *Environment) (Value, error) {
		pos, opts, err := ParseOptions("grep", args)
		if err != nil {
			return nil, err
		}
		if len(pos) != 2 {
			return nil, errors.New("grep expects 2 arguments: (grep pattern input [options])")
		}
		pat, ok := pos[0].(String)
		if !ok {
			return nil, errors.New("grep expects a string pattern")
		}
		m, err := compileGrepMatcher(string(pat))
		if err != nil {
			return nil, fmt.Errorf("grep: %w", err)
		}
		copt := grepCtxOpt{before: OptInt(opts, "before", 0), after: OptInt(opts, "after", 0)}
		if c := OptInt(opts, "context", 0); c > 0 {
			if copt.before == 0 {
				copt.before = c
			}
			if copt.after == 0 {
				copt.after = c
			}
		}
		if copt.before < 0 || copt.after < 0 {
			return nil, errors.New("grep: context counts must not be negative")
		}
		numbered := OptBool(opts, "line-numbers") || copt.active()
		switch in := normalizeList(pos[1]).(type) {
		case String:
			path := expandHome(string(in))
			if err := approval.requireRead(fmt.Sprintf("grep %q %q", string(pat), path), path); err != nil {
				return nil, err
			}
			if fi, err := os.Stat(path); err == nil && fi.IsDir() {
				return dirGrepStream(m, pat, path, copt), nil
			}
			f, err := os.Open(path)
			if err != nil {
				return nil, fmt.Errorf("grep: %v (the input string names a file — use (lines \"text\") to filter literal text)", err)
			}
			src := streamFromReader(f, f.Close)
			if numbered {
				return grepRowStream(m, pat, src, copt), nil
			}
			return grepStream(m, pat, src), nil
		case *Stream:
			if numbered {
				if !in.Lines() {
					return nil, errors.New("grep: context and line-number options apply to line input (a file, cat/sh output, or (lines text))")
				}
				return grepRowStream(m, pat, in, copt), nil
			}
			return grepStream(m, pat, in), nil
		case []Value:
			if numbered {
				tracker := &grepContext{opt: copt}
				result := make([]Value, 0, len(in))
				for i, elem := range in {
					if _, isDict := elem.(Dictionary); isDict {
						return nil, errors.New("grep: context and line-number options apply to line input (a file, cat/sh output, or (lines text))")
					}
					text := lineText(elem)
					for _, e := range tracker.feed(i+1, text, m.MatchString(text)) {
						result = append(result, grepLineRow(pat, e))
					}
				}
				return result, nil
			}
			result := make([]Value, 0, len(in))
			for _, elem := range in {
				if !grepMatch(m, elem) {
					continue
				}
				result = append(result, grepMark(pat, elem))
			}
			return result, nil
		default:
			return nil, errors.New("grep input must be a file path, a stream, or a list")
		}
	}))

	takeFn := BuiltinFunc(func(args []Value, env *Environment) (Value, error) {
		if len(args) != 2 {
			return nil, errors.New("take expects 2 arguments: (take n input)")
		}
		n := 0
		if idx, ok := numIndex(args[0]); ok {
			n = idx
		} else if s, isStr := args[0].(String); isStr {
			parsed, err := strconv.Atoi(string(s))
			if err != nil {
				return nil, errors.New("take: n must be a number")
			}
			n = parsed
		} else {
			return nil, errors.New("take: n must be a number")
		}
		if n < 0 {
			return nil, errors.New("take: n must not be negative")
		}
		var elems []Value
		switch in := normalizeList(args[1]).(type) {
		case *Stream:
			taken := 0
			next := func() (Value, bool, error) {
				if taken >= n {
					return nil, false, nil
				}
				v, ok, err := in.Next()
				if err != nil || !ok {
					return nil, false, err
				}
				taken++
				if taken >= n {
					_ = in.Close()
				}
				return v, true, nil
			}
			out := newStream(next, in.Close)
			out.lines = in.Lines()
			out.userCode = in.userCode
			return out, nil
		case String:
			lines := strings.Split(strings.TrimSuffix(string(in), "\n"), "\n")
			elems = make([]Value, len(lines))
			for i, line := range lines {
				elems[i] = String(line)
			}
		case []Value:
			elems = in
		default:
			return nil, errors.New("take input must be a string, a stream, or a list")
		}
		if n > len(elems) {
			n = len(elems)
		}
		return elems[:n], nil
	})
	Register("take", "the first n elements of a stream or list", CommandMeta{Stage: true})
	env.Set("take", takeFn)
	Register("head", "alias of take — the first n elements", CommandMeta{})
	env.Set("head", takeFn)

	Register("basename", "the file part of a path", CommandMeta{})
	env.Set("basename", BuiltinFunc(func(args []Value, env *Environment) (Value, error) {
		path, err := onePath("basename", args)
		if err != nil {
			return nil, err
		}
		return String(filepath.Base(path)), nil
	}))

	Register("dirname", "the directory part of a path", CommandMeta{})
	env.Set("dirname", BuiltinFunc(func(args []Value, env *Environment) (Value, error) {
		path, err := onePath("dirname", args)
		if err != nil {
			return nil, err
		}
		return String(filepath.Dir(path)), nil
	}))

	env.SetBuiltin("path-join", "join path segments into one path", BuiltinFunc(func(args []Value, env *Environment) (Value, error) {
		if len(args) == 0 {
			return nil, errors.New("path-join expects at least 1 argument")
		}
		parts := make([]string, len(args))
		for i, arg := range args {
			s, ok := arg.(String)
			if !ok {
				return nil, errors.New("path-join expects string arguments")
			}
			parts[i] = string(s)
		}
		return String(filepath.Join(parts...)), nil
	}))

	Register("sh", "run a shell command (native interpreter), stdout as a line stream (:full for exit/stderr)", CommandMeta{Options: []Option{
		{Long: "timeout", Kind: OptionNumber, Placeholder: "SECONDS", Doc: "kill the command after SECONDS (interrupt, then kill — rsh's escalation)"},
		{Long: "full", Kind: OptionBool, Doc: "return {:stdout :stderr :exit} eagerly instead of the output stream (no exit check)"},
	}})
	env.Set("sh", BuiltinFunc(func(args []Value, env *Environment) (Value, error) {
		if len(args) < 1 || len(args) > 4 {
			return nil, errors.New("sh expects 1 to 4 arguments: (sh \"command\" [:full] [{:timeout N}] [stdin]) — options and stdin in any order")
		}
		s, ok := args[0].(String)
		if !ok {
			return nil, errors.New("sh expects a string command")
		}
		full := false
		var timeout time.Duration
		var stdinVal Value
		for _, arg := range args[1:] {
			switch a := arg.(type) {
			case Keyword:
				if a != "full" {
					return nil, errors.New("sh options: :full ({:stdout :stderr :exit} instead of the output stream), {:timeout SECONDS}")
				}
				full = true
				continue
			case Dictionary:
				opts, isOpts, optErr := shOptionsDict(a)
				if optErr != nil {
					return nil, optErr
				}
				if isOpts {
					if OptBool(opts, "full") {
						full = true
					}
					if secs := OptNumber(opts, "timeout", 0); secs > 0 {
						timeout = time.Duration(secs * float64(time.Second))
					} else if _, set := opts["timeout"]; set {
						return nil, errors.New("sh: :timeout expects a positive number of seconds")
					}
					continue
				}
			}
			if stdinVal != nil {
				return nil, errors.New("sh takes at most one stdin value")
			}
			stdinVal = arg
		}
		stdin, stdinClose, err := shStdin(stdinVal)
		if err != nil {
			return nil, err
		}
		closeStdin := func() {
			if stdinClose != nil {
				_ = stdinClose()
			}
		}
		if err := approval.requireCommand(global, fmt.Sprintf("sh %q", string(s)), string(s)); err != nil {
			closeStdin()
			return nil, err
		}
		if full {
			ctx, stop := InterruptContext(context.Background())
			if timeout > 0 {
				var cancel context.CancelFunc
				ctx, cancel = context.WithTimeout(ctx, timeout)
				defer cancel()
			}
			var stdout, stderr lockedBuilder
			exit, err := rsh.Run(ctx, string(s), stdin, &stdout, &stderr)
			stop()
			closeStdin()
			if errors.Is(ctx.Err(), context.DeadlineExceeded) {
				return nil, fmt.Errorf("sh: timed out after %v", timeout)
			}
			if err != nil {
				return nil, err
			}
			return Dictionary{
				"stdout": String(strings.TrimRight(stdout.text(), "\n")),
				"stderr": String(strings.TrimRight(stderr.text(), "\n")),
				"exit":   Integer(exit),
			}, nil
		}
		return shStream(string(s), stdin, closeStdin, timeout)
	}))

	env.SetBuiltin("lines", "split a string (or drained stream) into a list of line strings", BuiltinFunc(func(args []Value, env *Environment) (Value, error) {
		if len(args) != 1 {
			return nil, errors.New("lines expects 1 argument: (lines text)")
		}
		switch in := args[0].(type) {
		case String:
			split := strings.Split(strings.TrimSuffix(string(in), "\n"), "\n")
			out := make([]Value, len(split))
			for i, l := range split {
				out[i] = String(l)
			}
			return out, nil
		case *Stream:
			lst, _, err := AsList(in)
			return lst, err
		default:
			return nil, errors.New("lines expects a string or a stream")
		}
	}))

	env.SetBuiltin("sleep", "pause n seconds (max 300; approval-gated in LLM tool calls)", BuiltinFunc(func(args []Value, env *Environment) (Value, error) {
		if len(args) != 1 {
			return nil, errors.New("sleep expects 1 argument: (sleep seconds)")
		}
		secs, ok := numFloat(args[0])
		if !ok || secs < 0 {
			return nil, errors.New("sleep expects a non-negative number of seconds")
		}
		if secs > 300 {
			return nil, errors.New("sleep is capped at 300 seconds")
		}
		if err := approval.require(fmt.Sprintf("sleep %v seconds", secs)); err != nil {
			return nil, err
		}
		deadline := time.Now().Add(time.Duration(secs * float64(time.Second)))
		for {
			if Interrupted() {
				return nil, ErrInterrupted
			}
			remaining := time.Until(deadline)
			if remaining <= 0 {
				return nil, nil
			}
			if remaining > 100*time.Millisecond {
				remaining = 100 * time.Millisecond
			}
			time.Sleep(remaining)
		}
	}))

	Register("which", "locate a command on PATH", CommandMeta{})
	env.Set("which", BuiltinFunc(func(args []Value, env *Environment) (Value, error) {
		name, err := onePath("which", args)
		if err != nil {
			return nil, err
		}
		path, lookErr := exec.LookPath(name)
		if lookErr != nil {
			return false, nil
		}
		return String(path), nil
	}))

	env.SetBuiltin("set-env", "set an environment variable for this process", BuiltinFunc(func(args []Value, env *Environment) (Value, error) {
		if len(args) != 2 {
			return nil, errors.New("set-env expects 2 arguments: (set-env name value)")
		}
		name, ok1 := args[0].(String)
		value, ok2 := args[1].(String)
		if !ok1 || !ok2 {
			return nil, errors.New("set-env expects strings")
		}
		if err := approval.require(fmt.Sprintf("set-env %q %q", string(name), string(value))); err != nil {
			return nil, err
		}
		if err := os.Setenv(string(name), string(value)); err != nil {
			return nil, err
		}
		return true, nil
	}))

	pathRows := func() Value {
		entries := filepath.SplitList(os.Getenv("PATH"))
		rows := make([]Value, 0, len(entries))
		for _, e := range entries {
			if e == "" {
				continue
			}
			rows = append(rows, Dictionary{"path": String(e)})
		}
		return rows
	}
	pathDirArgs := func(name string, args []Value) ([]string, error) {
		if len(args) == 0 {
			return nil, fmt.Errorf("%s expects at least 1 directory", name)
		}
		dirs := make([]string, len(args))
		for i, a := range args {
			s, ok := a.(String)
			if !ok {
				return nil, fmt.Errorf("%s expects string paths, got %s", name, PrintValue(a))
			}
			dirs[i] = expandHome(string(s))
		}
		return dirs, nil
	}

	Register("paths", "the PATH entries as {:path} rows", CommandMeta{Command: true, MaxArgs: 0})
	env.Set("paths", BuiltinFunc(func(args []Value, env *Environment) (Value, error) {
		if len(args) != 0 {
			return nil, errors.New("paths expects no arguments")
		}
		return pathRows(), nil
	}))

	Register("add-path", "idempotently prepend directories to PATH", CommandMeta{Command: true, MinArgs: 1, MaxArgs: -1, Usage: "dir ..."})
	env.Set("add-path", BuiltinFunc(func(args []Value, env *Environment) (Value, error) {
		dirs, err := pathDirArgs("add-path", args)
		if err != nil {
			return nil, err
		}
		if err := approval.require(readAction("add-path", dirs)); err != nil {
			return nil, err
		}
		current := filepath.SplitList(os.Getenv("PATH"))
		seen := make(map[string]bool, len(current)+len(dirs))
		for _, e := range current {
			seen[filepath.Clean(e)] = true
		}
		var added []string
		for _, d := range dirs {
			if key := filepath.Clean(d); !seen[key] {
				seen[key] = true
				added = append(added, d)
			}
		}
		if len(added) > 0 {
			joined := strings.Join(append(added, current...), string(os.PathListSeparator))
			if err := os.Setenv("PATH", joined); err != nil {
				return nil, err
			}
		}
		return pathRows(), nil
	}))

	Register("remove-path", "remove directories from PATH", CommandMeta{Command: true, MinArgs: 1, MaxArgs: -1, Usage: "dir ..."})
	env.Set("remove-path", BuiltinFunc(func(args []Value, env *Environment) (Value, error) {
		dirs, err := pathDirArgs("remove-path", args)
		if err != nil {
			return nil, err
		}
		if err := approval.require(readAction("remove-path", dirs)); err != nil {
			return nil, err
		}
		drop := make(map[string]bool, len(dirs))
		for _, d := range dirs {
			drop[filepath.Clean(d)] = true
		}
		var kept []string
		for _, e := range filepath.SplitList(os.Getenv("PATH")) {
			if !drop[filepath.Clean(e)] {
				kept = append(kept, e)
			}
		}
		if err := os.Setenv("PATH", strings.Join(kept, string(os.PathListSeparator))); err != nil {
			return nil, err
		}
		return pathRows(), nil
	}))

	Register("get", "index into a collection — (get key coll)", CommandMeta{Stage: true})
	env.Set("get", BuiltinFunc(func(args []Value, env *Environment) (Value, error) {
		if len(args) != 2 {
			return nil, errors.New("get expects 2 arguments: (get key-or-index dict-or-list)")
		}
		key, container := normalizeList(args[0]), normalizeList(args[1])
		// Indexing needs the whole sequence: streams materialize here.
		if s, isStream := container.(*Stream); isStream {
			lst, _, err := AsList(s)
			if err != nil {
				return nil, err
			}
			container = lst
		}
		switch c := container.(type) {
		case Dictionary:
			name := ""
			switch k := key.(type) {
			case String:
				name = string(k)
			case Keyword:
				name = string(k)
			case Symbol:
				name = string(k)
			default:
				return nil, errors.New("get: key must be a string, keyword, or symbol")
			}
			val, found := c[name]
			if !found {
				return false, nil
			}
			return val, nil
		case []Value:
			i := 0
			if idx, ok := numIndex(key); ok {
				i = idx
			} else if s, isStr := key.(String); isStr {
				// command mode passes words as strings: ls | get 0
				n, err := strconv.Atoi(string(s))
				if err != nil {
					return nil, errors.New("get: index must be a number")
				}
				i = n
			} else {
				return nil, errors.New("get: index must be a number")
			}
			if i < 0 || i >= len(c) {
				return nil, fmt.Errorf("get: index %d out of range (length %d)", i, len(c))
			}
			return c[i], nil
		case bool:
			if !c {
				return nil, errors.New("get expects a dictionary or list, got #f (a failed match?)")
			}
			return nil, errors.New("get expects a dictionary or list, got #t")
		default:
			return nil, fmt.Errorf("get expects a dictionary or list, got %s", PrintValue(c))
		}
	}))

	Register("keys", "a dictionary's keys", CommandMeta{Stage: true})
	env.Set("keys", BuiltinFunc(func(args []Value, env *Environment) (Value, error) {
		if len(args) != 1 {
			return nil, errors.New("keys expects 1 argument: (keys dict)")
		}
		dict, ok := args[0].(Dictionary)
		if !ok {
			return nil, errors.New("keys expects a dictionary")
		}
		names := make([]string, 0, len(dict))
		for k := range dict {
			names = append(names, k)
		}
		sort.Strings(names)
		result := make([]Value, len(names))
		for i, k := range names {
			result[i] = String(k)
		}
		return result, nil
	}))

	Register("builtins", "every registered callable as rows", CommandMeta{Command: true})
	env.Set("builtins", BuiltinFunc(func(args []Value, env *Environment) (Value, error) {
		if len(args) != 0 {
			return nil, errors.New("builtins expects no arguments (filter with: builtins | grep pattern)")
		}
		kinds := map[string]string{}
		collect := func(kind string, names func(e *Environment) []string) {
			for e := env; e != nil; e = e.parent {
				scope := names(e)
				for _, name := range scope {
					if _, seen := kinds[name]; !seen {
						kinds[name] = kind
					}
				}
			}
		}
		collect("special form", func(e *Environment) []string {
			var names []string
			for name, entry := range e.forms {
				if entry.isSpecialForm() {
					names = append(names, name)
				}
			}
			return names
		})
		collect("macro", func(e *Environment) []string {
			var names []string
			for name, entry := range e.forms {
				if !entry.isSpecialForm() && entry.mac != nil {
					names = append(names, name)
				}
			}
			return names
		})
		collect("function", func(e *Environment) []string {
			var fns []string
			for name, c := range e.bindings {
				switch c.val.(type) {
				case BuiltinFunc, *FastBuiltin:
					fns = append(fns, name)
				}
			}
			return fns
		})
		names := slices.Sorted(maps.Keys(kinds))
		rows := make([]Value, len(names))
		for i, name := range names {
			rows[i] = Dictionary{"name": String(name), "type": String(kinds[name]), "doc": String(BuiltinDoc(name))}
		}
		return rows, nil
	}))

	Register("table", "render rows as an aligned table string", CommandMeta{Stage: true})
	env.Set("table", BuiltinFunc(func(args []Value, env *Environment) (Value, error) {
		if len(args) != 1 {
			return nil, errors.New("table expects 1 argument: (table list-of-dicts)")
		}
		v := args[0]
		// Column widths need every row: streams materialize here.
		if stream, isStream := v.(*Stream); isStream {
			lst, _, err := AsList(stream)
			if err != nil {
				return nil, err
			}
			v = lst
		}
		s, ok := FormatTable(v)
		if !ok {
			return nil, errors.New("table expects a list of dictionaries or a dictionary")
		}
		return String(s), nil
	}))
}

const shStderrCap = 64 * 1024

type lockedBuilder struct {
	mu sync.Mutex
	b  strings.Builder
}

func (b *lockedBuilder) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.b.Write(p)
}

func (b *lockedBuilder) text() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.b.String()
}

type cappedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
	cap int
}

func (b *cappedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if room := b.cap - b.buf.Len(); room > 0 {
		if len(p) > room {
			b.buf.Write(p[:room])
		} else {
			b.buf.Write(p)
		}
	}
	return len(p), nil
}

func (b *cappedBuffer) text() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func shStdin(v Value) (io.Reader, func() error, error) {
	switch src := normalizeList(v).(type) {
	case nil:
		return nil, nil, nil
	case *Stream:
		if src.Lines() {
			return shStdinReader(src)
		}
	case String:
		return nil, nil, errors.New("sh: a bare string can't feed stdin — use (lines \"text\") for literal text, and quote the command: sh \"echo hi\"")
	}
	serialized, err := serializeValue(v, textLine)
	if err != nil {
		return nil, nil, fmt.Errorf("sh: stdin: %v", err)
	}
	stream, ok := serialized.(*Stream)
	if !ok {
		return nil, nil, fmt.Errorf("sh: stdin must be text lines, got %s", PrintValue(v))
	}
	return shStdinReader(stream)
}

func shStdinReader(s *Stream) (io.Reader, func() error, error) {
	if !s.userCode {
		return s.TextReader(), s.Close, nil
	}
	var b strings.Builder
	_, err := io.Copy(&b, s.TextReader())
	if cerr := s.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return nil, nil, fmt.Errorf("sh: stdin: %v", err)
	}
	return strings.NewReader(b.String()), nil, nil
}

func shOptionsDict(d Dictionary) (map[string]Value, bool, error) {
	registryMu.RLock()
	table := registry["sh"].meta.Options
	registryMu.RUnlock()
	opts := map[string]Value{}
	for k, v := range d {
		opt, ok := findOption(table, k)
		if !ok {
			return nil, false, nil
		}
		val, err := coerceOption("sh", opt, v)
		if err != nil {
			return nil, false, err
		}
		if val == nil { // boolean #f — unset
			continue
		}
		opts[opt.Long] = val
	}
	return opts, true, nil
}

func shStream(command string, stdin io.Reader, closeStdin func(), timeout time.Duration) (Value, error) {
	stderr := &cappedBuffer{cap: shStderrCap}
	pr, pw := io.Pipe()
	ctx, cancel := context.WithCancel(context.Background())
	runCtx, stopTimer := ctx, func() {}
	if timeout > 0 {
		runCtx, stopTimer = context.WithTimeout(ctx, timeout)
	}
	timedOut := func() bool { return errors.Is(runCtx.Err(), context.DeadlineExceeded) }
	type shResult struct {
		code int
		err  error
	}
	resCh := make(chan shResult, 1)
	go func() {
		code, err := rsh.Run(runCtx, command, stdin, pw, stderr)
		resCh <- shResult{code, err}
		_ = pw.Close()
	}()

	var waitOnce sync.Once
	var res shResult
	wait := func() shResult {
		waitOnce.Do(func() {
			closeStdin()
			res = <-resCh
			stopTimer()
		})
		return res
	}

	scanner := bufio.NewScanner(pr)
	scanner.Buffer(make([]byte, 64*1024), 4<<20)
	next := func() (Value, bool, error) {
		if scanner.Scan() {
			return String(scanner.Text()), true, nil
		}
		if scanErr := scanner.Err(); scanErr != nil {
			cancel()
			wait()
			return nil, false, fmt.Errorf("sh: %v", scanErr)
		}
		r := wait()
		if timedOut() {
			return nil, false, fmt.Errorf("sh: timed out after %v", timeout)
		}
		if r.err != nil {
			return nil, false, fmt.Errorf("sh: %v", r.err)
		}
		if r.code != 0 {
			msg := strings.TrimSpace(stderr.text())
			if msg != "" {
				return nil, false, fmt.Errorf("sh: exit %d: %s", r.code, msg)
			}
			return nil, false, fmt.Errorf("sh: exit %d", r.code)
		}
		return nil, false, nil
	}
	closefn := func() error {
		cancel()
		_ = pr.Close()
		wait()
		return nil
	}
	return newLineStream(next, closefn), nil
}

func globRecursive(pattern string, showHidden bool) ([]string, error) {
	re, err := globToRegexp(pattern)
	if err != nil {
		return nil, err
	}
	root := globWalkRoot(pattern)
	var matches []string
	err = filepath.WalkDir(root, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return nil // unreadable entries are skipped, not fatal
		}
		if !showHidden && path != root && strings.HasPrefix(d.Name(), ".") {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if path != root && re.MatchString(filepath.ToSlash(path)) {
			matches = append(matches, path)
		}
		return nil
	})
	return matches, err
}

func globWalkRoot(pattern string) string {
	root := "."
	dir := ""
	if strings.HasPrefix(filepath.ToSlash(pattern), "/") {
		root, dir = "/", "/"
	}
	for seg := range strings.SplitSeq(filepath.ToSlash(pattern), "/") {
		if seg == "" {
			continue
		}
		if strings.ContainsAny(seg, "*?[") {
			break
		}
		dir = filepath.Join(dir, seg)
		if info, err := os.Stat(dir); err != nil || !info.IsDir() {
			break
		}
		root = dir
	}
	return root
}

func globToRegexp(pattern string) (*regexp.Regexp, error) {
	var sb strings.Builder
	sb.WriteString("^")
	p := filepath.ToSlash(pattern)
	for i := 0; i < len(p); {
		switch {
		case strings.HasPrefix(p[i:], "**/"):
			sb.WriteString(`(?:[^/]+/)*`)
			i += 3
		case strings.HasPrefix(p[i:], "**"):
			sb.WriteString(`.*`)
			i += 2
		case p[i] == '*':
			sb.WriteString(`[^/]*`)
			i++
		case p[i] == '?':
			sb.WriteString(`[^/]`)
			i++
		default:
			sb.WriteString(regexp.QuoteMeta(string(p[i])))
			i++
		}
	}
	sb.WriteString("$")
	return regexp.Compile(sb.String())
}

var secretEnvName = regexp.MustCompile(`(?i)key|token|secret|password|passwd|credential|auth|private`)

const redactedEnvValue = "[redacted]"

func envBuiltin(env *Environment, ev *Evaluator) {
	Register("env", "the environment as rows, or one variable's value (:redact masks secrets)", CommandMeta{})
	env.Set("env", BuiltinFunc(func(args []Value, env *Environment) (Value, error) {
		var name string
		hasName := false
		redact := ev.Caller() == CallerAssistant
		for _, a := range args {
			switch v := a.(type) {
			case String:
				if hasName {
					return nil, errors.New("env expects at most one name: (env [name] [:redact])")
				}
				name, hasName = string(v), true
			case Keyword:
				if v != "redact" {
					return nil, fmt.Errorf("env: unknown option :%s (did you mean :redact?)", v)
				}
				redact = true
			default:
				return nil, errors.New("env expects a string name and/or :redact: (env [name] [:redact])")
			}
		}
		if hasName {
			val, found := os.LookupEnv(name)
			if !found {
				return false, nil
			}
			if redact && secretEnvName.MatchString(name) {
				return String(redactedEnvValue), nil
			}
			return String(val), nil
		}
		rows := make([]Value, 0, len(os.Environ()))
		for _, kv := range os.Environ() {
			k, v, found := strings.Cut(kv, "=")
			if !found {
				continue
			}
			if redact && secretEnvName.MatchString(k) {
				v = redactedEnvValue
			}
			rows = append(rows, Dictionary{"name": String(k), "value": String(v)})
		}
		sort.Slice(rows, func(i, j int) bool {
			return rows[i].(Dictionary)["name"].(String) < rows[j].(Dictionary)["name"].(String)
		})
		return rows, nil
	}))
}

func execBuiltin(env *Environment, ev *Evaluator) {
	env.SetBuiltin("exec", "run a program with the terminal attached (user-only; nil on exit 0)", BuiltinFunc(func(args []Value, env *Environment) (Value, error) {
		if err := ev.RequireUser("exec"); err != nil {
			return nil, err
		}
		if len(args) == 0 {
			return nil, errors.New("exec expects at least 1 argument: (exec \"cmd\" args...)")
		}
		argv := make([]string, len(args))
		for i, a := range args {
			switch v := a.(type) {
			case String:
				argv[i] = expandHome(string(v))
			case Integer, Number:
				argv[i] = PrintValue(v)
			default:
				return nil, fmt.Errorf("exec expects string arguments, got %s", PrintValue(a))
			}
		}
		cmd := exec.Command(argv[0], argv[1:]...)
		cmd.Stdin = os.Stdin
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		if ev.foreground != nil {
			code, err := ev.foreground(cmd, strings.Join(argv, " "))
			if err != nil {
				return nil, err
			}
			if code != 0 {
				return nil, fmt.Errorf("%s exited with status %d", argv[0], code)
			}
			return nil, nil
		}
		if err := cmd.Run(); err != nil {
			var exitErr *exec.ExitError
			if errors.As(err, &exitErr) {
				return nil, fmt.Errorf("%s exited with status %d", argv[0], exitErr.ExitCode())
			}
			return nil, err
		}
		return nil, nil
	}))
}

func expandHome(path string) string {
	if path == "~" || strings.HasPrefix(path, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, strings.TrimPrefix(path, "~"))
		}
	}
	return path
}

func contractHome(path string) string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return path
	}
	if path == home {
		return "~"
	}
	if strings.HasPrefix(path, home+string(filepath.Separator)) {
		return "~" + path[len(home):]
	}
	return path
}

func pathArgList(name string, v Value) ([]string, error) {
	switch p := v.(type) {
	case String:
		return []string{expandHome(string(p))}, nil
	case []Value:
		paths := make([]string, len(p))
		for i, e := range p {
			s, ok := e.(String)
			if !ok {
				return nil, fmt.Errorf("%s expects paths as strings, got %s", name, PrintValue(e))
			}
			paths[i] = expandHome(string(s))
		}
		return paths, nil
	}
	return nil, fmt.Errorf("%s expects a path or a list of paths", name)
}

func onePath(name string, args []Value) (string, error) {
	if len(args) != 1 {
		return "", fmt.Errorf("%s expects 1 argument", name)
	}
	s, ok := args[0].(String)
	if !ok {
		return "", fmt.Errorf("%s expects a string", name)
	}
	return expandHome(string(s)), nil
}

func globBase(pattern string) string {
	dir := pattern
	for strings.ContainsAny(dir, "*?[") {
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	return dir
}

func srcsAndDst(name string, args []Value) (srcs []string, dst string, multi bool, err error) {
	if len(args) != 2 {
		return nil, "", false, fmt.Errorf("%s expects 2 arguments: (%s src dst)", name, name)
	}
	srcs, err = pathArgList(name, args[0])
	if err != nil {
		return nil, "", false, err
	}
	d, ok := args[1].(String)
	if !ok {
		return nil, "", false, fmt.Errorf("%s expects a string destination", name)
	}
	dst = expandHome(string(d))
	_, multi = args[0].([]Value)
	if len(srcs) > 1 {
		if info, statErr := os.Stat(dst); statErr != nil || !info.IsDir() {
			return nil, "", false, fmt.Errorf("%s: %s is not a directory (needed for %d sources)", name, dst, len(srcs))
		}
	}
	return srcs, dst, multi, nil
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	info, err := in.Stat()
	if err != nil {
		return err
	}
	if info.IsDir() {
		return errors.New("cp: copying directories is not supported")
	}
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, info.Mode())
	if err != nil {
		return err
	}
	defer out.Close()
	_, err = io.Copy(out, in)
	return err
}

func catFiles(paths []string) *Stream {
	var cur *os.File
	var scanner *bufio.Scanner
	i := 0
	closeCur := func() error {
		if cur == nil {
			return nil
		}
		err := cur.Close()
		cur, scanner = nil, nil
		return err
	}
	next := func() (Value, bool, error) {
		for {
			if scanner == nil {
				if i >= len(paths) {
					return nil, false, nil
				}
				f, err := os.Open(paths[i])
				if err != nil {
					return nil, false, err
				}
				if info, err := f.Stat(); err == nil && info.IsDir() {
					f.Close()
					return nil, false, fmt.Errorf("cat: %s is a directory", paths[i])
				}
				i++
				cur = f
				scanner = bufio.NewScanner(f)
				// Same long-line allowance as streamFromReader.
				scanner.Buffer(make([]byte, 64*1024), 4<<20)
			}
			if scanner.Scan() {
				return String(scanner.Text()), true, nil
			}
			err := scanner.Err()
			if cerr := closeCur(); err == nil {
				err = cerr
			}
			if err != nil {
				return nil, false, err
			}
		}
	}
	return newLineStream(next, closeCur)
}

func catRangeStream(src *Stream, from, to int, numbered bool) *Stream {
	lineNum := 0
	next := func() (Value, bool, error) {
		for {
			if to != 0 && lineNum >= to {
				_ = src.Close()
				return nil, false, nil
			}
			v, ok, err := src.Next()
			if err != nil || !ok {
				return nil, false, err
			}
			lineNum++
			if lineNum < from {
				continue
			}
			if numbered {
				return Dictionary{"line": Integer(lineNum), "text": String(lineText(v))}, true, nil
			}
			return v, true, nil
		}
	}
	out := newStream(next, src.Close)
	out.lines = !numbered
	out.userCode = src.userCode
	return out
}

func writeFileImpl(name string, args []Value, flags int, approval *approvalGate) (Value, error) {
	if len(args) != 2 {
		return nil, fmt.Errorf("%s expects 2 arguments: (%s path content)", name, name)
	}
	pathArg, ok := args[0].(String)
	if !ok {
		return nil, fmt.Errorf("%s expects strings", name)
	}
	path := expandHome(string(pathArg))
	if err := approval.requireWrite(fmt.Sprintf("%s %q", name, string(pathArg)), path); err != nil {
		return nil, err
	}

	content := args[1]
	if s, isStream := content.(*Stream); !isStream || !s.Lines() {
		if _, isStr := content.(String); !isStr {
			if serialized, err := serializeValue(normalizeList(content), textLine); err == nil {
				if st, ok := serialized.(*Stream); ok {
					content = st
				}
			}
		}
	}
	if s, isStream := content.(*Stream); isStream && s.Lines() {
		f, err := os.OpenFile(path, flags, 0644)
		if err != nil {
			return nil, err
		}
		w := bufio.NewWriter(f)
		count := 0
		for {
			v, more, err := s.Next()
			if err != nil {
				_ = w.Flush()
				f.Close()
				return nil, err
			}
			if !more {
				break
			}
			line := lineText(v)
			w.WriteString(line)
			w.WriteByte('\n')
			count += len(line) + 1
		}
		if err := w.Flush(); err != nil {
			f.Close()
			return nil, err
		}
		if err := f.Close(); err != nil {
			return nil, err
		}
		return Integer(count), nil
	}

	text, ok := content.(String)
	if !ok {
		return nil, fmt.Errorf("%s expects strings", name)
	}
	f, err := os.OpenFile(path, flags, 0644)
	if err != nil {
		return nil, err
	}
	n, err := f.WriteString(string(text))
	if err != nil {
		f.Close()
		return nil, err
	}
	if err := f.Close(); err != nil {
		return nil, err
	}
	return Integer(n), nil
}

func fileInfoDict(name string, info os.FileInfo) Dictionary {
	fileType := "file"
	if info.IsDir() {
		fileType = "dir"
	} else if info.Mode()&os.ModeSymlink != 0 {
		fileType = "link"
	}
	return Dictionary{
		"name":     String(name),
		"type":     String(fileType),
		"size":     Integer(info.Size()),
		"mode":     String(info.Mode().String()),
		"modified": String(info.ModTime().Format("2006-01-02 15:04")),
	}
}

var tableColumnOrder = []string{"when", "dir", "pid", "name", "cpu", "mem", "time", "branch", "head", "type", "size", "modified", "mode", "path", "trees", "tasks", "lines", "words", "bytes", "id", "status", "age", "project", "title", "stdout", "stderr", "job", "state", "exit", "next", "every", "runs", "op", "file", "line", "chunk", "text", "command", "score", "doc"}

const (
	styleReset = "\x1b[0m"
	styleBold  = "\x1b[1m"
	styleDim   = "\x1b[2m"
	styleBlue  = "\x1b[1;34m"
	styleCyan  = "\x1b[36m"
	styleGreen = "\x1b[32m"
	styleMatch = "\x1b[1;31m" // grep-style bold red for matched text
)

func humanSize(n float64) string {
	units := []string{"B", "K", "M", "G", "T", "P"}
	i := 0
	for n >= 1024 && i < len(units)-1 {
		n /= 1024
		i++
	}
	if i == 0 {
		return fmt.Sprintf("%d%s", int64(n), units[0])
	}
	if n < 10 {
		return strings.TrimSuffix(fmt.Sprintf("%.1f", n), ".0") + units[i]
	}
	return fmt.Sprintf("%.0f%s", n, units[i])
}

func cellStyle(col string, row Dictionary) string {
	switch col {
	case "name", "path":
		t, ok := row["type"].(String)
		if !ok {
			return ""
		}
		switch string(t) {
		case "dir":
			return styleBlue
		case "link":
			return styleCyan
		}
		if mode, ok := row["mode"].(String); ok && strings.ContainsRune(string(mode), 'x') {
			return styleGreen
		}
	case "type", "mode", "modified":
		return styleDim
	}
	return ""
}

// FormatTable renders a list of dictionaries (or a single dictionary) as an
// aligned text table. Returns ok=false when the value has no tabular shape.
func FormatTable(v Value) (string, bool) {
	return formatTable(v, false, 0)
}

// FormatTableColor is FormatTable with ANSI styling for interactive display:
// bold headers and file names tinted by type. The plain variant remains for
// values that flow back into Scheme (the `table` builtin) and for tests.
func FormatTableColor(v Value) (string, bool) {
	return formatTable(v, true, 0)
}

// FormatTableWidth is FormatTable fitted to a display-width budget: the
// widest columns shrink and their cells wrap across continuation lines. A
// budget of 0 means unlimited.
func FormatTableWidth(v Value, width int) (string, bool) {
	return formatTable(v, false, width)
}

// FormatTableColorWidth is FormatTableWidth with ANSI styling — what the
// shell uses, passing the terminal width.
func FormatTableColorWidth(v Value, width int) (string, bool) {
	return formatTable(v, true, width)
}

const minCellWidth = 20

func fitTableWidths(widths []int, columns []string, budget int) {
	if budget <= 0 {
		return
	}
	sep := 2 * (len(widths) - 1)
	for {
		total := sep
		for _, w := range widths {
			total += w
		}
		if total <= budget {
			return
		}
		iMax, second := 0, 0
		for i, w := range widths {
			if w > widths[iMax] {
				iMax = i
			}
		}
		for i, w := range widths {
			if i != iMax && w > second {
				second = w
			}
		}
		target := widths[iMax] - (total - budget)
		if target < second {
			target = second
		}
		if minW := max(minCellWidth, uniseg.StringWidth(columns[iMax])); target < minW {
			target = minW
		}
		if target >= widths[iMax] {
			return // nothing left to shrink
		}
		widths[iMax] = target
	}
}

func splitCellLines(text string) []string {
	text = strings.ReplaceAll(text, "\r\n", "\n")
	text = strings.ReplaceAll(text, "\r", "\n")
	return strings.Split(text, "\n")
}

func wrapCell(text string, width int) []string {
	if strings.ContainsAny(text, "\n\r") {
		var lines []string
		for _, seg := range splitCellLines(text) {
			lines = append(lines, wrapCell(seg, width)...)
		}
		for len(lines) > 1 && lines[len(lines)-1] == "" {
			lines = lines[:len(lines)-1]
		}
		return lines
	}
	if width <= 0 || uniseg.StringWidth(text) <= width {
		return []string{text}
	}
	var lines []string
	var line strings.Builder
	lineW := 0
	flush := func() {
		lines = append(lines, line.String())
		line.Reset()
		lineW = 0
	}
	for word := range strings.SplitSeq(text, " ") {
		ww := uniseg.StringWidth(word)
		if lineW > 0 && lineW+1+ww > width {
			flush()
		}
		if lineW > 0 {
			line.WriteString(" ")
			lineW++
		}
		if ww > width {
			g := uniseg.NewGraphemes(word)
			for g.Next() {
				gw := g.Width()
				if lineW > 0 && lineW+gw > width {
					flush()
				}
				line.WriteString(g.Str())
				lineW += gw
			}
			continue
		}
		line.WriteString(word)
		lineW += ww
	}
	if lineW > 0 || len(lines) == 0 {
		flush()
	}
	return lines
}

func tableColumns(rows []Dictionary) []string {
	seen := map[string]bool{}
	var columns []string
	for _, key := range tableColumnOrder {
		for _, row := range rows {
			if _, ok := row[key]; ok && !seen[key] {
				seen[key] = true
				columns = append(columns, key)
				break
			}
		}
	}
	var rest []string
	for _, row := range rows {
		for key := range row {
			if !seen[key] && !strings.HasPrefix(key, "_") {
				seen[key] = true
				rest = append(rest, key)
			}
		}
	}
	sort.Strings(rest)
	return append(columns, rest...)
}

func formatTable(v Value, color bool, budget int) (string, bool) {
	var rows []Dictionary
	switch val := normalizeList(v).(type) {
	case Dictionary:
		rows = []Dictionary{val}
	case []Value:
		if len(val) == 0 {
			return "", false
		}
		for _, item := range val {
			dict, ok := item.(Dictionary)
			if !ok {
				return "", false
			}
			rows = append(rows, dict)
		}
	default:
		return "", false
	}

	columns := tableColumns(rows)

	cell := func(col string, v Value, ok bool) string {
		if !ok {
			return ""
		}
		if col == "size" {
			if n, isNum := numFloat(v); isNum {
				return humanSize(n)
			}
		}
		if s, isStr := v.(String); isStr {
			return string(s)
		}
		return PrintValue(v)
	}

	widths := make([]int, len(columns))
	for i, col := range columns {
		widths[i] = uniseg.StringWidth(col)
	}
	rendered := make([][]string, len(rows))
	for r, row := range rows {
		rendered[r] = make([]string, len(columns))
		for i, col := range columns {
			v, ok := row[col]
			text := cell(col, v, ok)
			rendered[r][i] = text
			for _, seg := range splitCellLines(text) {
				if w := uniseg.StringWidth(seg); w > widths[i] {
					widths[i] = w
				}
			}
		}
	}
	fitTableWidths(widths, columns, budget)

	var sb strings.Builder
	for i, col := range columns {
		text := col
		if color {
			text = styleBold + text + styleReset
		}
		sb.WriteString(text)
		sb.WriteString(strings.Repeat(" ", widths[i]-uniseg.StringWidth(col)))
		if i < len(columns)-1 {
			sb.WriteString("  ")
		}
	}
	sb.WriteString("\n")
	// Regexps for _match highlighting, compiled once per pattern.
	matchRes := map[string]*regexp.Regexp{}
	rowMatchRe := func(row Dictionary) *regexp.Regexp {
		pat, ok := row["_match"].(String)
		if !ok {
			return nil
		}
		re, ok := matchRes[string(pat)]
		if !ok {
			re, _ = regexp.Compile(string(pat))
			matchRes[string(pat)] = re
		}
		return re
	}
	for r := range rendered {
		cellLines := make([][]string, len(columns))
		height := 1
		for i := range columns {
			cellLines[i] = wrapCell(rendered[r][i], widths[i])
			if len(cellLines[i]) > height {
				height = len(cellLines[i])
			}
		}
		for ln := 0; ln < height; ln++ {
			for i, col := range columns {
				text := ""
				if ln < len(cellLines[i]) {
					text = cellLines[i][ln]
				}
				pad := widths[i] - uniseg.StringWidth(text)
				if pad < 0 {
					pad = 0
				}
				if color && text != "" {
					style := cellStyle(col, rows[r])
					if re := rowMatchRe(rows[r]); re != nil {
						text = re.ReplaceAllStringFunc(text, func(m string) string {
							return styleMatch + m + styleReset + style
						})
					}
					if style != "" {
						text = style + text + styleReset
					}
				}
				sb.WriteString(text)
				sb.WriteString(strings.Repeat(" ", pad))
				if i < len(columns)-1 {
					sb.WriteString("  ")
				}
			}
			if !(r == len(rendered)-1 && ln == height-1) {
				sb.WriteString("\n")
			}
		}
	}
	return sb.String(), true
}
