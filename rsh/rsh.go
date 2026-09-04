// Package rsh executes shell command lines natively via mvdan.cc/sh's
// interpreter — rf never forks /bin/sh.
package rsh

import (
	"context"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"strings"
	"sync"
	"time"

	"mvdan.cc/sh/v3/expand"
	"mvdan.cc/sh/v3/interp"
	"mvdan.cc/sh/v3/syntax"
)

// Session carries cross-line shell state that is not process state: non-
// exported shell variables and shell functions.
type Session struct {
	// Jobs, when set, gives every line's external children their own
	// process group with terminal foreground and ^Z parking — see
	// jobctl.go. Set once before the first Run.
	Jobs *JobControl

	mu    sync.Mutex
	vars  map[string]expand.Variable
	funcs map[string]*syntax.Stmt

	bgMu     sync.Mutex
	bg       map[int]bgEntry
	bgSerial int64

	nfMu     sync.Mutex
	notFound []string
}

func NewSession() *Session {
	return &Session{
		vars:  map[string]expand.Variable{},
		funcs: map[string]*syntax.Stmt{},
	}
}

// Environ is the expansion environment for `$VAR` words in command mode: the
// process env with the session's shell vars on top, the same overlay Run
// hydrates from.
func (s *Session) Environ() expand.Environ {
	s.mu.Lock()
	defer s.mu.Unlock()
	return overlayEnviron{base: expand.ListEnviron(os.Environ()...), vars: maps.Clone(s.vars)}
}

type overlayEnviron struct {
	base expand.Environ
	vars map[string]expand.Variable
}

func (o overlayEnviron) Get(name string) expand.Variable {
	if vr, ok := o.vars[name]; ok {
		return vr
	}
	return o.base.Get(name)
}

func (o overlayEnviron) Each(fn func(string, expand.Variable) bool) {
	stop := false
	o.base.Each(func(name string, vr expand.Variable) bool {
		if _, shadowed := o.vars[name]; shadowed {
			return true
		}
		if !fn(name, vr) {
			stop = true
			return false
		}
		return true
	})
	if stop {
		return
	}
	for name, vr := range o.vars {
		if !fn(name, vr) {
			return
		}
	}
}

func parse(src, name string) (*syntax.File, error) {
	return syntax.NewParser().Parse(strings.NewReader(src), name)
}

const bgSettle = 15 * time.Millisecond

var pathBuiltins = map[string]bool{"kill": true}

func pathBuiltinCalls(isFunc func(string) bool) interp.RunnerOption {
	return interp.CallHandler(func(ctx context.Context, args []string) ([]string, error) {
		if len(args) > 0 && pathBuiltins[args[0]] && (isFunc == nil || !isFunc(args[0])) {
			hc := interp.HandlerCtx(ctx)
			if path, err := interp.LookPathDir(hc.Dir, hc.Env, args[0]); err == nil {
				args[0] = path
			}
		}
		return args, nil
	})
}

func allBackground(file *syntax.File) bool {
	if len(file.Stmts) == 0 {
		return false
	}
	for _, st := range file.Stmts {
		if !st.Background && !st.Disown {
			return false
		}
	}
	return true
}

func hasBackground(file *syntax.File) bool {
	found := false
	syntax.Walk(file, func(n syntax.Node) bool {
		if st, ok := n.(*syntax.Stmt); ok && st.Background {
			found = true
		}
		return !found
	})
	return found
}

// Run executes a whole command-mode line against the session and harvests
// state changes back into the process and session.
func (s *Session) Run(ctx context.Context, line string, stdin io.Reader, stdout, stderr io.Writer) int {
	file, err := parse(line, "")
	if err != nil {
		fmt.Fprintln(stderr, "rf:", err)
		return 2
	}
	cwd, err := os.Getwd()
	if err != nil {
		fmt.Fprintln(stderr, "rf:", err)
		return 1
	}
	s.mu.Lock()
	env := overlayEnviron{base: expand.ListEnviron(os.Environ()...), vars: maps.Clone(s.vars)}
	funcs := maps.Clone(s.funcs)
	s.mu.Unlock()
	s.nfMu.Lock()
	s.notFound = nil
	s.nfMu.Unlock()

	opts := []interp.RunnerOption{
		interp.StdIO(stdin, stdout, stderr),
		interp.Env(env),
		interp.Dir(cwd),
		interp.Interactive(true),
		pathBuiltinCalls(func(name string) bool {
			s.mu.Lock()
			defer s.mu.Unlock()
			return s.funcs[name] != nil
		}),
	}
	var job *lineJob
	if s.Jobs != nil {
		jc := s.Jobs
		if allBackground(file) {
			noTTY := *s.Jobs
			noTTY.TTY = -1
			jc = &noTTY
		}
		job = &lineJob{jc: jc, sess: s, serial: s.nextSerial()}
		opts = append(opts, interp.ExecHandlers(func(interp.ExecHandlerFunc) interp.ExecHandlerFunc {
			return job.exec
		}))
		defer s.Jobs.restoreForeground()
	}
	r, err := interp.New(opts...)
	if err != nil {
		fmt.Fprintln(stderr, "rf:", err)
		return 1
	}
	r.Reset()
	r.Funcs = funcs

	runErr := r.Run(ctx, file)
	s.harvest(r, cwd)
	if job != nil && hasBackground(file) {
		time.Sleep(bgSettle)
		for _, b := range s.bgSurvivors(job.serial) {
			if s.Jobs.OnBackground != nil {
				s.Jobs.OnBackground(b.PID, b.Cmd)
			}
		}
	}
	if runErr == nil {
		return 0
	}
	if errors.Is(runErr, ErrStopped) {
		return 148
	}
	var status interp.ExitStatus
	if errors.As(runErr, &status) {
		return int(status)
	}
	if ctx.Err() != nil {
		return 130
	}
	fmt.Fprintln(stderr, "rf:", runErr)
	return 1
}

func (s *Session) noteNotFound(name string) {
	s.nfMu.Lock()
	defer s.nfMu.Unlock()
	s.notFound = append(s.notFound, name)
}

// NotFound returns the command names the most recent Run failed to resolve,
// in the order they failed.
func (s *Session) NotFound() []string {
	s.nfMu.Lock()
	defer s.nfMu.Unlock()
	return append([]string(nil), s.notFound...)
}

var harvestSkip = map[string]bool{
	"IFS": true, "OPTIND": true, "GID": true, "UID": true, "EUID": true,
	"SHLVL": true, "_": true, "PS1": true, "PS2": true, "LINENO": true,
}

func (s *Session) harvest(r *interp.Runner, startCwd string) {
	if r.Dir != startCwd {
		_ = os.Chdir(r.Dir)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for name, vr := range r.Vars {
		if harvestSkip[name] {
			continue
		}
		switch {
		case !vr.IsSet():
			// unset FOO — clear it everywhere.
			_ = os.Unsetenv(name)
			delete(s.vars, name)
		case vr.Exported && vr.Kind == expand.String:
			_ = os.Setenv(name, vr.Str)
			delete(s.vars, name)
		default:
			// Non-exported (or non-string: arrays) shell state.
			s.vars[name] = vr
		}
	}
	maps.Copy(s.funcs, r.Funcs)
}

// Run executes a command with the given stdio and no state harvest — fresh-
// shell semantics for pipeline stages and the (sh ...) builtin.
func Run(ctx context.Context, command string, stdin io.Reader, stdout, stderr io.Writer) (int, error) {
	file, err := parse(command, "sh")
	if err != nil {
		fmt.Fprintln(stderr, "sh:", err)
		return 2, nil
	}
	cwd, err := os.Getwd()
	if err != nil {
		return 1, err
	}
	r, err := interp.New(
		interp.StdIO(stdin, stdout, stderr),
		interp.Env(expand.ListEnviron(os.Environ()...)),
		interp.Dir(cwd),
		pathBuiltinCalls(nil),
	)
	if err != nil {
		return 1, err
	}
	runErr := r.Run(ctx, file)
	if runErr == nil {
		return 0, nil
	}
	var status interp.ExitStatus
	if errors.As(runErr, &status) {
		return int(status), nil
	}
	return 1, runErr
}
