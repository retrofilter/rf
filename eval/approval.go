package eval

import (
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
)

// AllowDir grants standing access under dir for approval-gated operations:
// reads always, writes too when write is true.
func (e *Evaluator) AllowDir(dir string, write bool) {
	if dir == "" {
		e.approval.allowDir, e.approval.allowWrite = "", false
		return
	}
	if e.approval.allowDir != "" {
		return
	}
	e.approval.allowDir = resolvePath(dir)
	e.approval.allowWrite = write
}

// PathAllowed reports whether the standing grant covers path at the given
// access level — the hook for gates outside the evaluator (the native
// read/edit/write tools).
func (e *Evaluator) PathAllowed(path string, write bool) bool {
	return e.approval.allows(path, write)
}

func resolvePath(p string) string {
	abs, err := filepath.Abs(expandHome(p))
	if err != nil {
		return p
	}
	if r, err := filepath.EvalSymlinks(abs); err == nil {
		return r
	}
	if r, err := filepath.EvalSymlinks(filepath.Dir(abs)); err == nil {
		return filepath.Join(r, filepath.Base(abs))
	}
	return abs
}

var credentialPaths = []string{"~/.rf.env", "~/.rf/webtoken"}

func credentialPath(resolved string) bool {
	for _, p := range credentialPaths {
		if resolved == resolvePath(p) {
			return true
		}
	}
	return false
}

func (g *approvalGate) allows(path string, write bool) bool {
	if g.allowDir == "" || (write && !g.allowWrite) {
		return false
	}
	r := resolvePath(path)
	if credentialPath(r) {
		return false
	}
	return r == g.allowDir || strings.HasPrefix(r, g.allowDir+string(filepath.Separator))
}

func (g *approvalGate) check(action string, reads, writes []string) error {
	if g.fn == nil {
		return nil
	}
	if len(reads)+len(writes) > 0 {
		covered := true
		for _, p := range reads {
			if !g.allows(p, false) {
				covered = false
				break
			}
		}
		for _, p := range writes {
			if covered && !g.allows(p, true) {
				covered = false
				break
			}
		}
		if covered {
			return nil
		}
	}
	return g.require(action)
}

const allowCommandsBinding = "agent-allow-commands"

const shMetaChars = ";&|$`<>(){}\\\n"

func commandAllowlisted(global *Environment, command string) bool {
	v, err := global.Lookup(allowCommandsBinding)
	if err != nil {
		return false
	}
	patterns, ok := normalizeList(v).([]Value)
	if !ok {
		return false
	}
	if strings.ContainsAny(command, shMetaChars) {
		return false
	}
	words := strings.Fields(command)
	for _, p := range patterns {
		ps, ok := p.(String)
		if !ok {
			continue
		}
		pw := strings.Fields(string(ps))
		if len(pw) == 0 || len(pw) > len(words) {
			continue
		}
		if slices.Equal(words[:len(pw)], pw) {
			return true
		}
	}
	return false
}

func (g *approvalGate) requireCommand(global *Environment, action, command string) error {
	if g.fn == nil {
		return nil
	}
	if commandAllowlisted(global, command) {
		return nil
	}
	return g.require(action)
}

func (g *approvalGate) requireRead(action string, paths ...string) error {
	return g.check(action, paths, nil)
}

func (g *approvalGate) requireWrite(action string, paths ...string) error {
	return g.check(action, nil, paths)
}

func readAction(name string, paths []string) string {
	quoted := make([]string, len(paths))
	for i, p := range paths {
		quoted[i] = fmt.Sprintf("%q", p)
	}
	return name + " " + strings.Join(quoted, " ")
}

func approvalForms(env *Environment, ev *Evaluator) {
	Register("with-approval", "run body forms behind one approval prompt instead of per-operation gates", CommandMeta{})
	env.SetSpecialForm("with-approval", func(args []Value, env *Environment, e *Evaluator) (Value, error) {
		if len(args) == 0 {
			return nil, errors.New("with-approval expects at least one form")
		}
		gate := ev.approval
		if gate.fn != nil {
			parts := make([]string, len(args))
			for i, form := range args {
				parts[i] = printSource(form)
			}
			if err := gate.require("with-approval\n  " + strings.Join(parts, "\n  ")); err != nil {
				return nil, err
			}
			prev := gate.fn
			gate.fn = nil
			defer func() { gate.fn = prev }()
		}
		var res Value
		var err error
		for _, form := range args {
			if res, err = e.Eval(form, env); err != nil {
				return nil, err
			}
		}
		return res, nil
	})

}
