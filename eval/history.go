package eval

import (
	"errors"
	"path/filepath"

	"github.com/jmoiron/sqlx"
	"github.com/retrofilter/rf/models"
)

func historyBuiltins(env *Environment, ev *Evaluator, db *sqlx.DB) {
	Register("history", "search command history", CommandMeta{
		Command: true, MaxArgs: 1, Usage: "[pattern]",
		Options: []Option{
			{Long: "dir", Short: "d", Kind: OptionString, Placeholder: "PATH", Doc: "only lines run in PATH (or below)"},
			{Long: "mode", Short: "m", Kind: OptionString, Placeholder: "MODE", Doc: "command, scheme, or chat lines"},
			{Long: "project", Short: "p", Kind: OptionString, Placeholder: "NAME", Doc: "only lines from the named project"},
			{Long: "limit", Short: "n", Kind: OptionInt, Placeholder: "N", Doc: "max rows (default 25)"},
		}})
	env.Set("history", BuiltinFunc(func(args []Value, env *Environment) (Value, error) {
		pos, opts, err := ParseOptions("history", args)
		if err != nil {
			return nil, err
		}
		var q models.HistoryQuery
		for _, arg := range pos {
			s, ok := arg.(String)
			if !ok || q.Pattern != "" {
				return nil, errors.New("history expects at most one pattern string: (history [pattern] [{:dir ... :mode ... :project ... :limit ...}])")
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
		q.Mode = OptString(opts, "mode", "")
		q.Project = OptString(opts, "project", "")
		q.Limit = OptInt(opts, "limit", 0)
		q.ExcludeID = ev.inFlightHistory

		entries, err := models.SearchHistory(db, q)
		if err != nil {
			return nil, err
		}
		result := make([]Value, 0, len(entries))
		for _, e := range entries {
			dict := Dictionary{
				"when": String(e.CreatedAt.Local().Format("2006-01-02 15:04")),
				"dir":  String(contractHome(e.Cwd)),
				"mode": String(e.Mode),
				"line": String(e.Line),
			}
			if e.ExitCode.Valid {
				dict["exit"] = Integer(e.ExitCode.Int64)
			}
			result = append(result, dict)
		}
		return result, nil
	}))
}
