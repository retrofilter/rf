package eval

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strconv"
	"strings"
)

type psProc struct {
	pid, cpu, mem float64
	uid           int
	tty           string
	elapsed       int64 // seconds since the process started
	name          string
}

func psBuiltins(env *Environment) {
	Register("ps", "running processes as {:pid :name :cpu :mem :time} rows, busiest first (yours on terminals; :user for all yours, :all for everyone's)", CommandMeta{})
	env.Set("ps", BuiltinFunc(func(args []Value, env *Environment) (Value, error) {
		scope := "terminal"
		for _, arg := range args {
			kw, isKw := arg.(Keyword)
			if !isKw || (kw != "user" && kw != "all") {
				return nil, errors.New("ps options: :user (all your processes) or :all (every user's)")
			}
			scope = string(kw)
		}
		out, err := exec.Command("ps", "-axo", "pid=,pcpu=,pmem=,uid=,tty=,etime=,command=").Output()
		if err != nil {
			var exit *exec.ExitError
			if errors.As(err, &exit) && len(exit.Stderr) > 0 {
				return nil, fmt.Errorf("ps: %s", strings.TrimSpace(string(exit.Stderr)))
			}
			return nil, fmt.Errorf("ps: %v", err)
		}
		procs := parsePS(string(out))
		keep := func(psProc) bool { return true }
		switch scope {
		case "user":
			uid := os.Getuid()
			keep = func(p psProc) bool { return p.uid == uid }
		case "terminal":
			uid := os.Getuid()
			if psSelfTTY(procs) == "" {
				keep = func(p psProc) bool { return p.uid == uid }
			} else {
				keep = func(p psProc) bool { return p.uid == uid && p.tty != "" }
			}
		}
		return psRows(procs, keep), nil
	}))
}

func psSelfTTY(procs []psProc) string {
	self := float64(os.Getpid())
	for _, p := range procs {
		if p.pid == self {
			return p.tty
		}
	}
	return ""
}

func parsePS(out string) []psProc {
	var procs []psProc
	for _, line := range strings.Split(out, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 7 {
			continue
		}
		pid, errPid := strconv.ParseFloat(fields[0], 64)
		cpu, errCpu := strconv.ParseFloat(fields[1], 64)
		mem, errMem := strconv.ParseFloat(fields[2], 64)
		uid, errUid := strconv.Atoi(fields[3])
		elapsed, okElapsed := psElapsed(fields[5])
		if errPid != nil || errCpu != nil || errMem != nil || errUid != nil || !okElapsed {
			continue
		}
		tty := fields[4]
		if strings.Trim(tty, "?-") == "" {
			tty = ""
		}
		procs = append(procs, psProc{
			pid: pid, cpu: cpu, mem: mem, uid: uid, tty: tty, elapsed: elapsed,
			name: strings.Join(fields[6:], " "), // the command line, args and all
		})
	}
	return procs
}

func psElapsed(s string) (int64, bool) {
	var days int64
	if d, rest, found := strings.Cut(s, "-"); found {
		n, err := strconv.ParseInt(d, 10, 64)
		if err != nil {
			return 0, false
		}
		days, s = n, rest
	}
	parts := strings.Split(s, ":")
	if len(parts) < 2 || len(parts) > 3 {
		return 0, false
	}
	var clock int64 // the hh:mm:ss part folded to seconds
	for _, part := range parts {
		n, err := strconv.ParseInt(part, 10, 64)
		if err != nil {
			return 0, false
		}
		clock = clock*60 + n
	}
	return days*86400 + clock, true
}

func psTime(secs int64) string {
	if secs < 0 {
		secs = 0
	}
	m, h, d := secs/60, secs/3600, secs/86400
	switch {
	case secs < 2*60: // young enough that seconds are the story
		return fmt.Sprintf("%ds", secs)
	case m < 10:
		if s := secs % 60; s > 0 {
			return fmt.Sprintf("%dm%ds", m, s)
		}
		return fmt.Sprintf("%dm", m)
	case h < 1:
		return fmt.Sprintf("%dm", m)
	case h < 8:
		if rem := m % 60; rem > 0 {
			return fmt.Sprintf("%dh%dm", h, rem)
		}
		return fmt.Sprintf("%dh", h)
	case d < 2:
		return fmt.Sprintf("%dh", h)
	case d < 8:
		if rem := h % 24; rem > 0 {
			return fmt.Sprintf("%dd%dh", d, rem)
		}
		return fmt.Sprintf("%dd", d)
	default:
		return fmt.Sprintf("%dd", d)
	}
}

func psRows(procs []psProc, keep func(psProc) bool) []Value {
	var kept []psProc
	for _, p := range procs {
		if keep(p) {
			kept = append(kept, p)
		}
	}
	sort.SliceStable(kept, func(i, j int) bool {
		a, b := kept[i], kept[j]
		if a.cpu != b.cpu {
			return a.cpu > b.cpu
		}
		if a.mem != b.mem {
			return a.mem > b.mem
		}
		return a.pid < b.pid
	})
	rows := make([]Value, 0, len(kept))
	for _, p := range kept {
		rows = append(rows, Dictionary{
			"pid":  Integer(p.pid),
			"name": String(p.name),
			"cpu":  Number(p.cpu),
			"mem":  Number(p.mem),
			"time": String(psTime(p.elapsed)),
		})
	}
	return rows
}
