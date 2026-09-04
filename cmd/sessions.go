package cmd

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/retrofilter/rf/console"
	"github.com/retrofilter/rf/eval"
	"github.com/retrofilter/rf/llm"
)

func registerConsoleBuiltins(chat *llm.Chat) {
	eval.Register("rename-session", "rename this console session (needs rf console)",
		eval.CommandMeta{Command: true, MinArgs: 1, MaxArgs: 1, Usage: "name"})
	chat.Env.Set("rename-session", eval.BuiltinFunc(func(args []eval.Value, env *eval.Environment) (eval.Value, error) {
		name, ok := singleString(args)
		if !ok {
			return nil, errors.New("rename-session expects a name: (rename-session \"name\")")
		}
		con, err := console.NewFromEnv()
		if err != nil {
			return nil, fmt.Errorf("rename-session: %v", err)
		}
		if err := con.Rename(name); err != nil {
			return nil, fmt.Errorf("rename-session: %v", err)
		}
		return eval.String(name), nil
	}))

	eval.Register("sessions", "list the console's sessions as rows — live ones, then scheduled ones with their next run (needs rf console)",
		eval.CommandMeta{Command: true})
	chat.Env.Set("sessions", eval.BuiltinFunc(func(args []eval.Value, env *eval.Environment) (eval.Value, error) {
		if len(args) != 0 {
			return nil, errors.New("sessions expects no arguments")
		}
		con, err := console.NewFromEnv()
		if err != nil {
			return nil, fmt.Errorf("sessions: %v", err)
		}
		rows, err := con.Sessions()
		if err != nil {
			return nil, fmt.Errorf("sessions: %v", err)
		}
		result := make([]eval.Value, 0, len(rows))
		for _, s := range rows {
			if s.Status == console.StatusScheduled {
				row := eval.Dictionary{
					"name":    eval.String(s.Name),
					"status":  eval.String(s.Status),
					"dir":     eval.String(contractHome(s.Dir)),
					"command": eval.String(s.Command),
					"next":    eval.String(time.Unix(s.Next, 0).Format("2006-01-02 15:04")),
				}
				if s.Every != "" {
					row["every"] = eval.String(s.Every)
				}
				result = append(result, row)
				continue
			}
			result = append(result, eval.Dictionary{
				"name":    eval.String(s.Name),
				"status":  eval.String(s.Status),
				"harness": eval.String(s.Harness),
				"dir":     eval.String(contractHome(s.Dir)),
				"added":   eval.Integer(s.Added),
				"deleted": eval.Integer(s.Deleted),
				"elapsed": eval.Integer(s.Elapsed),
			})
		}
		return result, nil
	}))

	eval.Register("session", "open a console session running COMMAND — now, or scheduled with --in/--at/--every; named by --name or a slug of the command (needs rf console)",
		eval.CommandMeta{Command: true, MinArgs: 0, MaxArgs: 1, Usage: "[command]",
			Options: []eval.Option{
				{Long: "name", Short: "n", Kind: eval.OptionString, Placeholder: "NAME", Doc: "session name (default: a slug of the command; required for a plain shell and --cancel)"},
				{Long: "dir", Short: "d", Kind: eval.OptionString, Placeholder: "PATH", Doc: "working directory (default: this shell's cwd)"},
				{Long: "in", Kind: eval.OptionString, Placeholder: "SPAN", Doc: "open after SPAN (30s, 10m, 4h, 1d, 1w) instead of now"},
				{Long: "at", Kind: eval.OptionString, Placeholder: "TIME", Doc: "open at TIME (09:00 — the next one — or 2026-03-01 09:00)"},
				{Long: "every", Kind: eval.OptionString, Placeholder: "SPAN", Doc: "reopen every SPAN (hour, day, week, or a span); a run still up gets a numbered name (review-2)"},
				{Long: "cancel", Kind: eval.OptionBool, Doc: "drop the schedule of this name"},
			}})
	chat.Env.Set("session", eval.BuiltinFunc(func(args []eval.Value, env *eval.Environment) (eval.Value, error) {
		pos, opts, err := eval.ParseOptions("session", args)
		if err != nil {
			return nil, err
		}
		var run string
		if len(pos) > 0 {
			var ok bool
			if run, ok = singleString(pos); !ok {
				return nil, errors.New("session expects a command line: (session \"claude /review\" [{:name ... :dir ... :in ... :at ... :every ...}])")
			}
		}
		name := eval.OptString(opts, "name", "")
		if name == "" {
			if eval.OptBool(opts, "cancel") {
				return nil, errors.New("session: --cancel needs the schedule's --name (sessions lists them)")
			}
			if name = console.SessionSlug(run); name == "" {
				return nil, errors.New("session expects a command line, or --name for a plain shell: session 'claude /review', or session -n scratch")
			}
		}
		if eval.OptBool(opts, "cancel") {
			if run != "" {
				return nil, errors.New("session: --cancel drops a schedule and takes no command")
			}
			for _, key := range []string{"dir", "in", "at", "every"} {
				if _, set := opts[key]; set {
					return nil, fmt.Errorf("session: --cancel drops a schedule and takes no --%s", key)
				}
			}
			cg, err := eval.DefaultGraph()
			if err != nil {
				return nil, fmt.Errorf("session: %v", err)
			}
			found, err := console.DeleteSchedule(cg, name)
			if err != nil {
				return nil, fmt.Errorf("session: %v", err)
			}
			if !found {
				return nil, fmt.Errorf("session: no schedule named %q (sessions lists them)", name)
			}
			return true, nil
		}
		dir := expandHome(eval.OptString(opts, "dir", ""))
		if dir == "" {
			if dir, err = os.Getwd(); err != nil {
				return nil, fmt.Errorf("session: %v", err)
			}
		}

		var period time.Duration
		everyRaw := eval.OptString(opts, "every", "")
		if everyRaw != "" {
			if period, err = eval.ParseSpan("session", "every", everyRaw); err != nil {
				return nil, err
			}
			if period < time.Minute {
				return nil, errors.New("session: --every must be at least 1m")
			}
		}
		now := time.Now()
		var next time.Time
		inRaw, atRaw := eval.OptString(opts, "in", ""), eval.OptString(opts, "at", "")
		switch {
		case inRaw != "" && atRaw != "":
			return nil, errors.New("session: --in and --at both set the first run; give one")
		case inRaw != "":
			delay, err := eval.ParseSpan("session", "in", inRaw)
			if err != nil {
				return nil, err
			}
			if delay <= 0 {
				return nil, errors.New("session: --in must be positive")
			}
			next = now.Add(delay)
		case atRaw != "":
			if next, err = eval.ParseAt("session", atRaw, now); err != nil {
				return nil, err
			}
		case period > 0:
			next = now.Add(period)
		}

		if fn := chat.Eval.Approver(); fn != nil {
			action := fmt.Sprintf("session %q in %q", name, dir)
			if run != "" {
				action += fmt.Sprintf(" running %q", run)
			}
			if !next.IsZero() {
				action += " at " + next.Format("2006-01-02 15:04")
			}
			if everyRaw != "" {
				action += " every " + everyRaw
			}
			if !fn(action) {
				return nil, fmt.Errorf("%s: denied by user", action)
			}
		}

		if next.IsZero() {
			con, err := console.NewFromEnv()
			if err != nil {
				return nil, fmt.Errorf("session: %v", err)
			}
			if err := con.NewSession(name, dir, run); err != nil {
				return nil, fmt.Errorf("session: %v", err)
			}
			return eval.String(name), nil
		}
		cg, err := eval.DefaultGraph()
		if err != nil {
			return nil, fmt.Errorf("session: %v", err)
		}
		id, err := console.PutSchedule(cg, console.Schedule{
			Name: name, Dir: dir, Command: run, Every: everyRaw, Period: period, Next: next,
		})
		if err != nil {
			return nil, fmt.Errorf("session: %v", err)
		}
		handle := eval.Dictionary{
			"id":   eval.Integer(id),
			"name": eval.String(name),
			"next": eval.String(next.Format("2006-01-02 15:04")),
		}
		if everyRaw != "" {
			handle["every"] = eval.String(everyRaw)
		}
		return handle, nil
	}))
}

func singleString(args []eval.Value) (string, bool) {
	if len(args) != 1 {
		return "", false
	}
	s, ok := args[0].(eval.String)
	return string(s), ok
}

func contractHome(path string) string {
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		if path == home {
			return "~"
		}
		if strings.HasPrefix(path, home+string(os.PathSeparator)) {
			return "~" + path[len(home):]
		}
	}
	return path
}
