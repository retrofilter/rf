package eval

import (
	"testing"
	"time"

	"github.com/retrofilter/rf/core"
	"github.com/retrofilter/rf/models"
	"github.com/stretchr/testify/require"
)

func TestMessagesBuiltin(t *testing.T) {
	db := createTestDB(t)
	defer db.Close()
	ev := NewEvaluatorWithEnvironment(db, core.NewGraphStore(db))
	env := ev.globalEnv

	// A synced Claude Code session in the past...
	t0 := time.Date(2026, 8, 1, 10, 0, 0, 0, time.UTC)
	require.NoError(t, models.EnsureClaudeSession(db, "cc-12345678-9abc", "/home/u/src/bar", "bar", "fix retry", t0, t0.Add(time.Minute)))
	add := func(kind, text, uuid string, ts time.Time) {
		t.Helper()
		added, err := models.AppendClaudeMessage(db, "cc-12345678-9abc", kind, text, uuid, ts)
		require.NoError(t, err)
		require.True(t, added)
	}
	add("user", "fix the retry backoff bug", "u1", t0)
	add("assistant", "the bug is in retry.go", "a1", t0.Add(time.Minute))

	// ...and an rf chat session now: text rows qualify, the tool row not.
	require.NoError(t, models.EnsureSession(db, "rf-sess-1", "/home/u/src/foo/main", "foo", ""))
	record := func(m *models.Message) {
		t.Helper()
		_, err := models.AppendSessionMessage(db, "rf-sess-1", m)
		require.NoError(t, err)
	}
	record(&models.Message{Role: models.MessageRoleUser, Type: models.MessageTypeText, Content: "how do we deploy the console"})
	record(&models.Message{Role: models.MessageRoleAssistant, Type: models.MessageTypeTool, Content: "(ls)", Code: "(ls)", FunctionName: "scheme"})
	record(&models.Message{Role: models.MessageRoleAssistant, Type: models.MessageTypeText, Content: "run rf console with hooks"})

	rows := func(expr string) []Value {
		t.Helper()
		v, err := evalExpr(expr, ev, env)
		require.NoError(t, err, expr)
		list, ok := v.([]Value)
		require.True(t, ok, "%s should return a list, got %v", expr, v)
		return list
	}
	texts := func(list []Value) []string {
		var out []string
		for _, r := range list {
			out = append(out, string(r.(Dictionary)["text"].(String)))
		}
		return out
	}

	all := rows(`(messages)`)
	require.Equal(t, []string{
		"fix the retry backoff bug",
		"the bug is in retry.go",
		"how do we deploy the console",
		"run rf console with hooks",
	}, texts(all))

	// Row shape: source, role, contracted dir, short session id.
	first := all[0].(Dictionary)
	require.Equal(t, String("claude"), first["source"])
	require.Equal(t, String("user"), first["role"])
	require.Equal(t, String("/home/u/src/bar"), first["dir"])
	require.Equal(t, String("cc-12345"), first["session"])
	require.Equal(t, String("rf"), all[2].(Dictionary)["source"])

	// Pattern, role, source, dir, project, session-prefix, and limit filters.
	require.Equal(t, []string{"fix the retry backoff bug", "the bug is in retry.go"},
		texts(rows(`(messages "retry")`)))
	require.Equal(t, []string{"fix the retry backoff bug", "how do we deploy the console"},
		texts(rows(`(messages {:role "user"})`)))
	require.Equal(t, []string{"how do we deploy the console", "run rf console with hooks"},
		texts(rows(`(messages {:source "rf"})`)))
	require.Equal(t, []string{"fix the retry backoff bug", "the bug is in retry.go"},
		texts(rows(`(messages {:dir "/home/u/src/bar"})`)))
	require.Equal(t, []string{"how do we deploy the console", "run rf console with hooks"},
		texts(rows(`(messages {:project "foo"})`)))
	require.Equal(t, []string{"fix the retry backoff bug", "the bug is in retry.go"},
		texts(rows(`(messages {:session "cc-12345"})`)))
	require.Equal(t, []string{"run rf console with hooks"},
		texts(rows(`(messages {:limit 1})`)))

	// Bad option values fail loud.
	_, err := evalExpr(`(messages {:role "tool"})`, ev, env)
	require.ErrorContains(t, err, "user or assistant")
	_, err = evalExpr(`(messages {:source "gemini"})`, ev, env)
	require.ErrorContains(t, err, "rf or claude")
	_, err = evalExpr(`(messages {:folder "x"})`, ev, env)
	require.ErrorContains(t, err, ":dir")
}
