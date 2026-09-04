package schema

import (
	"context"
	"regexp"
	"strings"
	"time"

	"github.com/retrofilter/rf/logger"
)

// LoggingHooks implements the sqlhooks.Hooks interface for logging SQL queries
type LoggingHooks struct{}

var whitespaceRegex = regexp.MustCompile(`\s+`)

func normalizeWhitespace(s string) string {
	return strings.TrimSpace(whitespaceRegex.ReplaceAllString(s, " "))
}

type ctxKey int

const sqlStartTimeKey ctxKey = iota

const maxLoggedArg = 64

func truncateArg(arg interface{}) interface{} {
	switch v := arg.(type) {
	case string:
		if len(v) > maxLoggedArg {
			return v[:maxLoggedArg] + "…"
		}
	case []byte:
		if len(v) > maxLoggedArg {
			return string(v[:maxLoggedArg]) + "…"
		}
	}
	return arg
}

// Before is called before a query is executed
func (h *LoggingHooks) Before(ctx context.Context, query string, args ...interface{}) (context.Context, error) {
	return context.WithValue(ctx, sqlStartTimeKey, time.Now()), nil
}

// After is called after a query is executed
func (h *LoggingHooks) After(ctx context.Context, query string, args ...interface{}) (context.Context, error) {
	startTime, ok := ctx.Value(sqlStartTimeKey).(time.Time)
	if !ok {
		startTime = time.Now()
	}

	duration := time.Since(startTime)

	logged := make([]interface{}, len(args))
	for i, arg := range args {
		logged[i] = truncateArg(arg)
	}

	logger.Debug().
		Str("sql", normalizeWhitespace(query)).
		Interface("args", logged).
		Dur("duration", duration).
		Msg("SQL")

	return ctx, nil
}
