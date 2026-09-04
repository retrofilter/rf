package rsh

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
	"mvdan.cc/sh/v3/expand"
	"mvdan.cc/sh/v3/interp"
)

// JobControl wires a Session's external children into the caller's job
// table. Set once on the Session; a nil JobControl leaves the default
// exec handler (children share rf's group, no parking).
type JobControl struct {
	// TTY is the terminal descriptor handed to each job's process group
	// for its foreground lifetime; negative skips terminal handoff
	// (children still get their own group, so direct signals park).
	TTY int
	// OnStop is called once per line, from the exec handler, when the
	// foreground job stops: the caller records {pgid, line} and prints
	// its stopped-job message before the prompt returns.
	OnStop func(pgid int)
	// OnBackground is called at the end of a line for each external child it
	// left running (a `&` survivor) — synchronously, before the prompt returns,
	// so the caller can print a `[bg pid]` notice.
	OnBackground func(pid int, cmd string)
}

// BgJob is a still-running background child: its pid and the command it
// was started with.
type BgJob struct {
	PID int
	Cmd string
}

// ErrStopped is the fatal error a parked line's run returns; Session.Run
// maps it to exit 128+SIGTSTP.
var ErrStopped = errors.New("stopped")

const sigkillGrace = 2 * time.Second

type lineJob struct {
	jc     *JobControl
	sess   *Session // owner of the live-children registry
	serial int64    // this line's registry tag
	mu     sync.Mutex
	pgid   int
	live   int  // processes started and not yet reaped
	parked bool // OnStop already fired for this line
}

type bgEntry struct {
	cmd    string
	serial int64
}

func (s *Session) nextSerial() int64 {
	s.bgMu.Lock()
	defer s.bgMu.Unlock()
	s.bgSerial++
	return s.bgSerial
}

func (s *Session) bgAdd(pid int, cmd string, serial int64) {
	s.bgMu.Lock()
	defer s.bgMu.Unlock()
	if s.bg == nil {
		s.bg = map[int]bgEntry{}
	}
	s.bg[pid] = bgEntry{cmd: cmd, serial: serial}
}

func (s *Session) bgRemove(pid int) {
	s.bgMu.Lock()
	defer s.bgMu.Unlock()
	delete(s.bg, pid)
}

func (s *Session) bgSurvivors(serial int64) []BgJob {
	return s.bgList(func(e bgEntry) bool { return e.serial == serial })
}

// Background returns every still-running background child, oldest line
// first. Dead entries (reaped elsewhere, or missed) are pruned by a
// liveness check, so the list is self-healing.
func (s *Session) Background() []BgJob {
	return s.bgList(func(bgEntry) bool { return true })
}

func (s *Session) bgList(keep func(bgEntry) bool) []BgJob {
	s.bgMu.Lock()
	defer s.bgMu.Unlock()
	jobs := make([]BgJob, 0, len(s.bg))
	for pid, e := range s.bg {
		if syscall.Kill(pid, 0) != nil {
			delete(s.bg, pid)
			continue
		}
		if keep(e) {
			jobs = append(jobs, BgJob{PID: pid, Cmd: e.cmd})
		}
	}
	sort.Slice(jobs, func(i, j int) bool { return jobs[i].PID < jobs[j].PID })
	return jobs
}

func startInGroup(base exec.Cmd, attr *syscall.SysProcAttr, fallback bool) (*exec.Cmd, error) {
	c := base
	c.SysProcAttr = attr
	err := c.Start()
	if err == nil || !fallback {
		return &c, err
	}
	c = base
	c.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := c.Start(); err != nil {
		return nil, err
	}
	return &c, nil
}

func (j *lineJob) exec(ctx context.Context, args []string) error {
	hc := interp.HandlerCtx(ctx)
	path, err := interp.LookPathDir(hc.Dir, hc.Env, args[0])
	if err != nil {
		fmt.Fprintln(hc.Stderr, err)
		j.sess.noteNotFound(args[0])
		return interp.ExitStatus(127)
	}
	cmd := exec.Cmd{
		Path: path,
		Args: args,
		Env:  execEnv(hc.Env),
		Dir:  hc.Dir,
	}
	var bridge bridged
	cmd.Stdin, err = bridge.reader(hc.Stdin)
	if err != nil {
		return err
	}
	cmd.Stdout, err = bridge.writer(hc.Stdout)
	if err != nil {
		return err
	}
	cmd.Stderr, err = bridge.writer(hc.Stderr)
	if err != nil {
		return err
	}

	j.mu.Lock()
	first := j.pgid == 0
	var attr *syscall.SysProcAttr
	switch {
	case first && j.jc.TTY >= 0:
		attr = &syscall.SysProcAttr{Setpgid: true, Foreground: true, Ctty: j.jc.TTY}
	case first:
		attr = &syscall.SysProcAttr{Setpgid: true}
	default:
		attr = &syscall.SysProcAttr{Setpgid: true, Pgid: j.pgid}
	}
	started, err := startInGroup(cmd, attr, !first)
	if err == nil {
		if first {
			j.pgid = started.Process.Pid
		}
		j.live++
	}
	j.mu.Unlock()
	bridge.started()
	if err != nil {
		fmt.Fprintln(hc.Stderr, err)
		return interp.ExitStatus(127)
	}
	j.sess.bgAdd(started.Process.Pid, strings.Join(args, " "), j.serial)

	stopf := context.AfterFunc(ctx, func() {
		j.mu.Lock()
		pgid := j.pgid
		j.mu.Unlock()
		_ = syscall.Kill(-pgid, syscall.SIGINT)
		time.AfterFunc(sigkillGrace, func() {
			j.mu.Lock()
			live := j.live
			j.mu.Unlock()
			if live > 0 {
				_ = syscall.Kill(-pgid, syscall.SIGKILL)
			}
		})
	})
	defer stopf()

	pid := started.Process.Pid
	for {
		var ws syscall.WaitStatus
		if _, err := syscall.Wait4(pid, &ws, syscall.WUNTRACED, nil); err != nil {
			if err == syscall.EINTR {
				continue
			}
			j.reaped(pid)
			bridge.drain()
			return err
		}
		switch {
		case ws.Stopped():
			j.sess.bgRemove(pid)
			j.park()
			return ErrStopped
		case ws.Exited():
			j.reaped(pid)
			bridge.drain()
			if code := ws.ExitStatus(); code != 0 {
				return interp.ExitStatus(code)
			}
			return nil
		case ws.Signaled():
			j.reaped(pid)
			bridge.drain()
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return interp.ExitStatus(128 + ws.Signal())
		}
	}
}

func (j *lineJob) park() {
	j.mu.Lock()
	first := !j.parked
	j.parked = true
	pgid := j.pgid
	j.mu.Unlock()
	if first && j.jc.OnStop != nil {
		j.jc.OnStop(pgid)
	}
}

func (j *lineJob) reaped(pid int) {
	j.sess.bgRemove(pid)
	j.mu.Lock()
	j.live--
	j.mu.Unlock()
}

func (jc *JobControl) restoreForeground() {
	if jc.TTY >= 0 {
		_ = unix.IoctlSetPointerInt(jc.TTY, unix.TIOCSPGRP, syscall.Getpgrp())
	}
}

const bridgeGrace = time.Second

type bridged struct {
	afterStart []io.Closer     // parent's pipe ends, closed once the child holds its dups
	flushed    []chan struct{} // one per output copier, closed when its writer has all bytes
}

func (b *bridged) reader(r io.Reader) (io.Reader, error) {
	switch r := r.(type) {
	case nil, *os.File:
		return r, nil
	default:
		pr, pw, err := os.Pipe()
		if err != nil {
			return nil, err
		}
		go func() {
			_, _ = io.Copy(pw, r)
			_ = pw.Close()
		}()
		b.afterStart = append(b.afterStart, pr)
		return pr, nil
	}
}

func (b *bridged) writer(w io.Writer) (io.Writer, error) {
	switch w := w.(type) {
	case *os.File:
		return w, nil
	default:
		pr, pw, err := os.Pipe()
		if err != nil {
			return nil, err
		}
		done := make(chan struct{})
		go func() {
			defer close(done)
			_, _ = io.Copy(w, pr)
			_ = pr.Close()
		}()
		b.afterStart = append(b.afterStart, pw)
		b.flushed = append(b.flushed, done)
		return pw, nil
	}
}

func (b *bridged) started() {
	for _, c := range b.afterStart {
		_ = c.Close()
	}
	b.afterStart = nil
}

func (b *bridged) drain() {
	if len(b.flushed) == 0 {
		return
	}
	deadline := time.NewTimer(bridgeGrace)
	defer deadline.Stop()
	for _, done := range b.flushed {
		select {
		case <-done:
		case <-deadline.C:
			return
		}
	}
}

func execEnv(env expand.Environ) []string {
	list := make([]string, 0, 64)
	env.Each(func(name string, vr expand.Variable) bool {
		if vr.Exported && vr.Kind == expand.String {
			list = append(list, name+"="+vr.String())
		}
		return true
	})
	return list
}
