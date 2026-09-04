package cmd

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"syscall"

	"golang.org/x/sys/unix"
	"golang.org/x/term"

	"github.com/retrofilter/rf/eval"
)

type job struct {
	pgid int
	line string
}

func ttyFD() (int, bool) {
	fd := int(os.Stdin.Fd())
	return fd, term.IsTerminal(fd)
}

func (st *shellState) runForeground(cmd *exec.Cmd, line string) (int, error) {
	fd, isTTY := ttyFD()
	if !isTTY {
		err := cmd.Run()
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return exitErr.ExitCode(), nil
		}
		if err != nil {
			return 127, err
		}
		return 0, nil
	}

	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true, Foreground: true, Ctty: fd}
	if err := cmd.Start(); err != nil {
		return 127, err
	}
	return st.waitForeground(cmd.Process.Pid, line), nil
}

func (st *shellState) waitForeground(pgid int, line string) int {
	defer func() {
		if fd, isTTY := ttyFD(); isTTY {
			_ = unix.IoctlSetPointerInt(fd, unix.TIOCSPGRP, syscall.Getpgrp())
		}
	}()
	for {
		var ws syscall.WaitStatus
		if _, err := syscall.Wait4(pgid, &ws, syscall.WUNTRACED, nil); err != nil {
			if err == syscall.EINTR {
				continue
			}
			fmt.Println(colorRed+"Error:"+colorReset, err)
			return 1
		}
		switch {
		case ws.Stopped():
			st.parkJob(pgid, line)
			return 128 + int(ws.StopSignal())
		case ws.Exited():
			return ws.ExitStatus()
		case ws.Signaled():
			return 128 + int(ws.Signal())
		}
	}
}

func (st *shellState) parkJob(pgid int, line string) {
	st.jobs = append(st.jobs, job{pgid: pgid, line: line})
	fmt.Printf("\n%s[%d]+ stopped%s  %s  %s(fg resumes)%s\n",
		colorGray, len(st.jobs), colorReset, line, colorGray, colorReset)
}

func (st *shellState) fg() int {
	n := len(st.jobs)
	if n == 0 {
		fmt.Println("fg: no stopped jobs")
		return 1
	}
	j := st.jobs[n-1]
	st.jobs = st.jobs[:n-1]
	fmt.Println(j.line)
	if fd, isTTY := ttyFD(); isTTY {
		_ = unix.IoctlSetPointerInt(fd, unix.TIOCSPGRP, j.pgid)
	}
	if err := syscall.Kill(-j.pgid, syscall.SIGCONT); err != nil {
		fmt.Println("fg:", err)
		return 1
	}
	return st.waitForeground(j.pgid, j.line)
}

func (st *shellState) jobRows() []eval.Value {
	var rows []eval.Value
	for i, j := range st.jobs {
		rows = append(rows, eval.Dictionary{
			"job":     eval.Integer(i + 1),
			"state":   eval.String("stopped"),
			"command": eval.String(j.line),
		})
	}
	for _, b := range st.rsh.Background() {
		rows = append(rows, eval.Dictionary{
			"job":     eval.Integer(b.PID),
			"state":   eval.String("running"),
			"command": eval.String(b.Cmd),
		})
	}
	return append(rows, eval.JobRows()...)
}

func (st *shellState) hangupJobs() {
	for _, j := range st.jobs {
		_ = syscall.Kill(-j.pgid, syscall.SIGHUP)
		_ = syscall.Kill(-j.pgid, syscall.SIGCONT)
	}
	st.jobs = nil
}
