package eval

import (
	"errors"
	"fmt"
	"path/filepath"

	"github.com/jmoiron/sqlx"
	"github.com/retrofilter/rf/agent"
	"github.com/retrofilter/rf/models"

	_ "github.com/retrofilter/rf/agent/claude"
)

func messagesBuiltins(env *Environment, db *sqlx.DB) {
	Register("messages", "search chat messages across rf and Claude Code sessions", CommandMeta{
		Command: true, MaxArgs: 1, Usage: "[pattern]",
		Options: []Option{
			{Long: "dir", Short: "d", Kind: OptionString, Placeholder: "PATH", Doc: "only sessions run in PATH (or below)"},
			{Long: "project", Short: "p", Kind: OptionString, Placeholder: "NAME", Doc: "only sessions from the named project"},
			{Long: "role", Short: "r", Kind: OptionString, Placeholder: "ROLE", Doc: "user or assistant rows only"},
			{Long: "source", Short: "s", Kind: OptionString, Placeholder: "SOURCE", Doc: "rf or claude sessions only"},
			{Long: "session", Kind: OptionString, Placeholder: "ID", Doc: "one session, by id or prefix"},
			{Long: "limit", Short: "n", Kind: OptionInt, Placeholder: "N", Doc: "max rows (default 50)"},
			{Long: "all", Short: "a", Kind: OptionBool, Doc: "no row limit"},
			{Long: "sync", Kind: OptionBool, Doc: "import new Claude Code transcript lines first"},
		}})
	env.Set("messages", BuiltinFunc(func(args []Value, env *Environment) (Value, error) {
		pos, opts, err := ParseOptions("messages", args)
		if err != nil {
			return nil, err
		}
		if OptBool(opts, "sync") {
			if _, err := agent.Sync(db, agent.Options{Project: projectFor}); err != nil {
				return nil, err
			}
		}
		var q models.MessageQuery
		for _, arg := range pos {
			s, ok := arg.(String)
			if !ok || q.Pattern != "" {
				return nil, errors.New("messages expects at most one pattern string: (messages [pattern] [{:dir ... :role ... :limit ...}])")
			}
			q.Pattern = string(s)
		}
		if dir := OptString(opts, "dir", ""); dir != "" {
			abs, err := filepath.Abs(expandHome(dir))
			if err != nil {
				return nil, err
			}
			q.Dir = abs
		}
		q.Project = OptString(opts, "project", "")
		q.Role = OptString(opts, "role", "")
		if q.Role != "" && q.Role != "user" && q.Role != "assistant" {
			return nil, fmt.Errorf("messages: :role must be user or assistant, not %q", q.Role)
		}
		q.Source = OptString(opts, "source", "")
		if q.Source != "" && q.Source != models.SessionSourceRF && q.Source != models.SessionSourceClaude {
			return nil, fmt.Errorf("messages: :source must be rf or claude, not %q", q.Source)
		}
		q.Session = OptString(opts, "session", "")
		q.Limit = OptInt(opts, "limit", 0)
		q.All = OptBool(opts, "all")

		rows, err := models.SearchMessages(db, q)
		if err != nil {
			return nil, err
		}
		result := make([]Value, 0, len(rows))
		for _, r := range rows {
			result = append(result, Dictionary{
				"when":    String(r.CreatedAt.Local().Format("2006-01-02 15:04")),
				"source":  String(r.Source),
				"session": String(shortSessionID(r.Session)),
				"role":    String(r.Kind),
				"dir":     String(contractHome(r.Cwd)),
				"text":    String(r.Text),
			})
		}
		return result, nil
	}))
}

func projectFor(cwd string) string {
	if name, _, ok := FindProject(cwd); ok {
		return name
	}
	return ""
}

func shortSessionID(id string) string {
	if len(id) > 8 {
		return id[:8]
	}
	return id
}
