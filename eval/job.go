package eval

import (
	"context"
	"errors"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/retrofilter/rf/rsh"
)

const (
	jobStdoutCap = 4 << 20 // head-kept stdout bytes per job
	jobStderrCap = 256 * 1024
)

type shellJob struct {
	id       int
	command  string
	cancel   context.CancelFunc
	every    time.Duration // recurring interval; 0 = one-shot
	everyRaw string        // the interval as spelled, for rows
	done     chan struct{} // closed when the job can never run again

	mu     sync.Mutex
	stdout *cappedBuffer
	stderr *cappedBuffer
	next   time.Time // pending fire time; zero while running or finished
	active bool      // a run is in flight
	runs   int       // completed runs
	exit   int       // last completed run's exit
	runErr error
}

func (j *shellJob) running() bool {
	select {
	case <-j.done:
		return false
	default:
		return true
	}
}

func (j *shellJob) state() string {
	if !j.running() {
		j.mu.Lock()
		defer j.mu.Unlock()
		if j.runs > 0 {
			return "done"
		}
		return "canceled"
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.active {
		return "running"
	}
	return "scheduled"
}

var jobRegistry = struct {
	mu   sync.Mutex
	m    map[int]*shellJob
	next int
}{m: map[int]*shellJob{}}

func jobArg(name string, v Value) (*shellJob, error) {
	if d, ok := v.(Dictionary); ok {
		id, exists := d["job"]
		if !exists {
			return nil, fmt.Errorf("%s expects a job handle or id", name)
		}
		v = id
	}
	id, ok := numIndex(v)
	if !ok {
		if s, isStr := v.(String); isStr {
			if n, err := strconv.Atoi(string(s)); err == nil {
				id, ok = n, true
			}
		}
	}
	if !ok {
		return nil, fmt.Errorf("%s expects a job handle or id", name)
	}
	jobRegistry.mu.Lock()
	j := jobRegistry.m[id]
	jobRegistry.mu.Unlock()
	if j == nil {
		return nil, fmt.Errorf("%s: no job %d", name, id)
	}
	return j, nil
}

func jobStatusRow(j *shellJob) Dictionary {
	running := j.running()
	row := Dictionary{
		"job":     Integer(j.id),
		"command": String(j.command),
		"state":   String(j.state()),
		"running": running,
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.runs > 0 {
		row["exit"] = Integer(j.exit)
	}
	if j.every > 0 {
		row["every"] = String(j.everyRaw)
		row["runs"] = Integer(j.runs)
	}
	if !j.next.IsZero() {
		row["next"] = String(j.next.Format("2006-01-02 15:04:05"))
	}
	return row
}

// JobRows lists every job started this process as rows — the cmd `jobs`
// builtin appends these to the shell's stopped-job rows.
func JobRows() []Value {
	jobRegistry.mu.Lock()
	jobs := make([]*shellJob, 0, len(jobRegistry.m))
	for _, j := range jobRegistry.m {
		jobs = append(jobs, j)
	}
	jobRegistry.mu.Unlock()
	sort.Slice(jobs, func(a, b int) bool { return jobs[a].id < jobs[b].id })
	rows := make([]Value, 0, len(jobs))
	for _, j := range jobs {
		row := Dictionary{"job": Integer(j.id), "command": String(j.command), "state": String(j.state())}
		j.mu.Lock()
		if j.runs > 0 {
			row["exit"] = Integer(j.exit)
		}
		if j.every > 0 {
			row["every"] = String(j.everyRaw)
		}
		if !j.next.IsZero() {
			row["next"] = String(j.next.Format("2006-01-02 15:04:05"))
		}
		j.mu.Unlock()
		rows = append(rows, row)
	}
	return rows
}

func jobJoinable(j *shellJob, since int) bool {
	select {
	case <-j.done:
		return true
	default:
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.runs > since
}

func jobWait(j *shellJob, deadline time.Time, since int) (finished bool, err error) {
	for {
		if jobJoinable(j, since) {
			return true, nil
		}
		if Interrupted() {
			return false, ErrInterrupted
		}
		if !deadline.IsZero() && !time.Now().Before(deadline) {
			return false, nil
		}
		select {
		case <-j.done:
			return true, nil
		case <-time.After(100 * time.Millisecond):
		}
	}
}

func jobWaitDone(j *shellJob, deadline time.Time) (bool, error) {
	return jobWait(j, deadline, math.MaxInt)
}

// ParseSpan reads a schedule span: Go durations ("30s", "10m", "1h30m") plus
// day/week units ("1d", "2w") and the bare unit words minute, hour, day, week
// — `--every day` reads as English.
func ParseSpan(command, flag, raw string) (time.Duration, error) {
	s := strings.ToLower(strings.TrimSpace(raw))
	switch s {
	case "minute":
		return time.Minute, nil
	case "hour":
		return time.Hour, nil
	case "day":
		return 24 * time.Hour, nil
	case "week":
		return 7 * 24 * time.Hour, nil
	}
	if n, ok := strings.CutSuffix(s, "d"); ok {
		if f, err := strconv.ParseFloat(n, 64); err == nil {
			return time.Duration(f * float64(24*time.Hour)), nil
		}
	}
	if n, ok := strings.CutSuffix(s, "w"); ok {
		if f, err := strconv.ParseFloat(n, 64); err == nil {
			return time.Duration(f * float64(7*24*time.Hour)), nil
		}
	}
	d, err := time.ParseDuration(s)
	if err != nil {
		return 0, fmt.Errorf("%s: --%s expects a span like 30s, 10m, 4h, 1d, 1w (or minute/hour/day/week), got %q", command, flag, raw)
	}
	return d, nil
}

// ParseAt resolves --at's raw text against now: "HH:MM" is the next such
// clock time, "YYYY-MM-DD HH:MM" (or T-joined) an absolute local time, "YYYY-
// MM-DD" that day's midnight. A past time errors.
func ParseAt(command, raw string, now time.Time) (time.Time, error) {
	s := strings.TrimSpace(raw)
	loc := now.Location()
	if t, err := time.ParseInLocation("15:04", s, loc); err == nil {
		at := time.Date(now.Year(), now.Month(), now.Day(), t.Hour(), t.Minute(), 0, 0, loc)
		if !at.After(now) {
			at = at.AddDate(0, 0, 1)
		}
		return at, nil
	}
	for _, layout := range []string{"2006-01-02 15:04", "2006-01-02T15:04", "2006-01-02 15:04:05", "2006-01-02"} {
		at, err := time.ParseInLocation(layout, s, loc)
		if err != nil {
			continue
		}
		if !at.After(now) {
			return time.Time{}, fmt.Errorf("%s: --at %s is in the past", command, raw)
		}
		return at, nil
	}
	return time.Time{}, fmt.Errorf("%s: --at expects a time like 09:00 or 2026-03-01 09:00, got %q", command, raw)
}

func startJob(command string, first, every time.Duration, everyRaw string) *shellJob {
	ctx, cancel := context.WithCancel(context.Background())
	j := &shellJob{
		command:  command,
		cancel:   cancel,
		every:    every,
		everyRaw: everyRaw,
		done:     make(chan struct{}),
	}
	if first > 0 {
		j.next = time.Now().Add(first)
	} else {
		j.active = true
		j.stdout = &cappedBuffer{cap: jobStdoutCap}
		j.stderr = &cappedBuffer{cap: jobStderrCap}
	}
	jobRegistry.mu.Lock()
	jobRegistry.next++
	j.id = jobRegistry.next
	jobRegistry.m[j.id] = j
	jobRegistry.mu.Unlock()
	go func() {
		defer close(j.done)
		for {
			j.mu.Lock()
			next := j.next
			j.mu.Unlock()
			if !next.IsZero() {
				select {
				case <-ctx.Done():
					return
				case <-time.After(time.Until(next)):
				}
			}
			j.mu.Lock()
			j.next = time.Time{}
			j.active = true
			if j.stdout == nil || j.runs > 0 {
				j.stdout = &cappedBuffer{cap: jobStdoutCap}
				j.stderr = &cappedBuffer{cap: jobStderrCap}
			}
			out, errOut := j.stdout, j.stderr
			j.mu.Unlock()
			code, err := rsh.Run(ctx, j.command, nil, out, errOut)
			j.mu.Lock()
			j.active = false
			j.exit, j.runErr = code, err
			j.runs++
			again := j.every > 0 && ctx.Err() == nil
			if again {
				j.next = time.Now().Add(j.every)
			}
			j.mu.Unlock()
			if !again {
				return
			}
		}
	}()
	return j
}

var jobModeKeys = []string{"status", "wait", "output", "kill"}

func jobOutputLines(j *shellJob, wantStderr bool) (Value, error) {
	j.mu.Lock()
	buf := j.stdout
	if wantStderr {
		buf = j.stderr
	}
	j.mu.Unlock()
	if buf == nil {
		return nil, fmt.Errorf("job %d has not started yet (job -s shows its fire time)", j.id)
	}
	text := strings.TrimRight(buf.text(), "\n")
	var items []Value
	if text != "" {
		for _, line := range strings.Split(text, "\n") {
			items = append(items, String(line))
		}
	}
	return streamFromList(items, true), nil
}

func normalizeJobModeHandles(args []Value) []Value {
	out := make([]Value, len(args))
	for i, a := range args {
		out[i] = a
		d, ok := a.(Dictionary)
		if !ok {
			continue
		}
		var repl Dictionary
		for _, key := range jobModeKeys {
			h, isDict := d[key].(Dictionary)
			if !isDict {
				continue
			}
			id, hasID := h["job"]
			if !hasID {
				continue
			}
			if repl == nil {
				repl = Dictionary{}
				for k, v := range d {
					repl[k] = v
				}
			}
			repl[key] = id
		}
		if repl != nil {
			out[i] = repl
		}
	}
	return out
}

func jobBuiltins(env *Environment, approval *approvalGate) {
	global := env

	Register("job", "start a background command, returning a handle — -s polls, -w joins, -o peeks at output, -k stops; --in delays it, --every reruns it", CommandMeta{
		Command: true, Instruction: true, MinArgs: 0, MaxArgs: 1, Usage: "\"command\"",
		Options: []Option{
			{Long: "in", Kind: OptionString, Placeholder: "SPAN", Doc: "start after SPAN (30s, 10m, 4h, 1d, 1w)"},
			{Long: "every", Kind: OptionString, Placeholder: "SPAN", Doc: "rerun every SPAN (hour, day, week, or a span) until killed; lives with this shell"},
			{Long: "status", Short: "s", Kind: OptionInt, Placeholder: "JOB", Doc: "poll job JOB: {:job :command :state :running [:exit]}"},
			{Long: "wait", Short: "w", Kind: OptionInt, Placeholder: "JOB", Doc: "join job JOB and return {:stdout :stderr :exit} (a recurring job's next finished run)"},
			{Long: "output", Short: "o", Kind: OptionInt, Placeholder: "JOB", Doc: "job JOB's captured output so far as lines, without blocking — job -o 3 | tail -20"},
			{Long: "kill", Short: "k", Kind: OptionInt, Placeholder: "JOB", Doc: "stop job JOB (interrupt, then kill) and cancel its schedule"},
			{Long: "stderr", Kind: OptionBool, Doc: "with --output: the stderr buffer instead of stdout"},
			{Long: "timeout", Short: "t", Kind: OptionNumber, Placeholder: "SECONDS", Doc: "with --wait: give up after SECONDS (the job keeps running)"},
		},
	})
	env.Set("job", BuiltinFunc(func(args []Value, env *Environment) (Value, error) {
		pos, opts, err := ParseOptions("job", normalizeJobModeHandles(args))
		if err != nil {
			return nil, err
		}
		var mode string
		for _, key := range jobModeKeys {
			if _, ok := opts[key]; ok {
				if mode != "" {
					return nil, errors.New("job takes one of :status/:wait/:kill")
				}
				mode = key
			}
		}
		if mode != "" {
			if len(pos) != 0 {
				return nil, fmt.Errorf("job with :%s acts on an existing job and takes no command", mode)
			}
			for _, key := range []string{"in", "every"} {
				if _, ok := opts[key]; ok {
					return nil, fmt.Errorf("job: :%s schedules a new command, not a :%s", key, mode)
				}
			}
			if _, ok := opts["timeout"]; ok && mode != "wait" {
				return nil, errors.New("job: :timeout goes with :wait")
			}
			if _, ok := opts["stderr"]; ok && mode != "output" {
				return nil, errors.New("job: :stderr goes with :output")
			}
			j, err := jobArg("job", opts[mode])
			if err != nil {
				return nil, err
			}
			switch mode {
			case "status":
				return jobStatusRow(j), nil
			case "output":
				return jobOutputLines(j, OptBool(opts, "stderr"))
			case "kill":
				j.cancel()
				finished, err := jobWaitDone(j, time.Now().Add(15*time.Second))
				if err != nil {
					return nil, err
				}
				if !finished {
					return nil, fmt.Errorf("job -k: job %d did not stop", j.id)
				}
				return jobStatusRow(j), nil
			}
			var deadline time.Time
			secs := OptNumber(opts, "timeout", 0)
			if secs > 0 {
				deadline = time.Now().Add(time.Duration(secs * float64(time.Second)))
			}
			j.mu.Lock()
			since := j.runs
			j.mu.Unlock()
			finished, err := jobWait(j, deadline, since)
			if err != nil {
				return nil, err
			}
			if !finished {
				return nil, fmt.Errorf("job -w: job %d still running after %vs (job -k stops it)", j.id, secs)
			}
			j.mu.Lock()
			defer j.mu.Unlock()
			if j.runs == 0 {
				return nil, fmt.Errorf("job %d was canceled before it ran", j.id)
			}
			if j.runErr != nil {
				return nil, fmt.Errorf("job %d: %v", j.id, j.runErr)
			}
			return Dictionary{
				"stdout": String(strings.TrimRight(j.stdout.text(), "\n")),
				"stderr": String(strings.TrimRight(j.stderr.text(), "\n")),
				"exit":   Integer(j.exit),
			}, nil
		}

		if _, ok := opts["timeout"]; ok {
			return nil, errors.New("job: :timeout goes with :wait")
		}
		if _, ok := opts["stderr"]; ok {
			return nil, errors.New("job: :stderr goes with :output")
		}
		if len(pos) != 1 {
			return nil, errors.New(`job expects a command: (job "cmd" [{:in "4h" :every "1d"}]) — or one of {:status ID} {:wait ID} {:output ID} {:kill ID}`)
		}
		s, ok := pos[0].(String)
		if !ok {
			return nil, errors.New("job expects a string command")
		}
		var delay, every time.Duration
		everyRaw := OptString(opts, "every", "")
		if everyRaw != "" {
			if every, err = ParseSpan("job", "every", everyRaw); err != nil {
				return nil, err
			}
			if every < time.Second {
				return nil, errors.New("job: --every must be at least 1s")
			}
		}
		if raw := OptString(opts, "in", ""); raw != "" {
			if delay, err = ParseSpan("job", "in", raw); err != nil {
				return nil, err
			}
			if delay <= 0 {
				return nil, errors.New("job: --in must be positive")
			}
		}
		label := fmt.Sprintf("job %q", string(s))
		if everyRaw != "" {
			label = fmt.Sprintf("job --every %s %q", everyRaw, string(s))
		} else if delay > 0 {
			label = fmt.Sprintf("job --in %s %q", OptString(opts, "in", ""), string(s))
		}
		if err := approval.requireCommand(global, label, string(s)); err != nil {
			return nil, err
		}
		first := delay
		if first == 0 && every > 0 {
			first = every
		}
		j := startJob(string(s), first, every, everyRaw)
		handle := Dictionary{"job": Integer(j.id), "command": String(j.command)}
		j.mu.Lock()
		if !j.next.IsZero() {
			handle["next"] = String(j.next.Format("2006-01-02 15:04:05"))
		}
		j.mu.Unlock()
		if every > 0 {
			handle["every"] = String(everyRaw)
		}
		return handle, nil
	}))
}
