package console

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

const statuslineInput = `{
	"cwd": "/home/u/src/retrofilter",
	"model": {"id": "claude-opus-5", "display_name": "Opus"},
	"workspace": {"current_dir": "/home/u/src/retrofilter"},
	"context_window": {"used_percentage": 8.4},
	"rate_limits": {
		"five_hour": {"used_percentage": 23.5, "resets_at": 1786500000},
		"seven_day": {"used_percentage": 41.2, "resets_at": 1786900000}
	}
}`

func statuslineHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	return filepath.Join(home, ".claude", usageCacheName)
}

func TestHandleStatuslineRecordsUsage(t *testing.T) {
	cachePath := statuslineHome(t)

	var out strings.Builder
	require.NoError(t, HandleStatusline(strings.NewReader(statuslineInput), &out, os.Stderr, ""))

	// The default line: model, dir, context, both windows.
	require.Equal(t, "Opus · retrofilter · ctx 8% · 5h 24% · wk 41%", strings.TrimSpace(out.String()))

	// The payload's rate_limits landed in the shared cache the console reads.
	b, err := os.ReadFile(cachePath)
	require.NoError(t, err)
	var cached usageCacheFile
	require.NoError(t, json.Unmarshal(b, &cached))
	require.NotNil(t, cached.Usage)
	require.InDelta(t, 23.5, cached.Usage.Session.Utilization, 0.001)
	require.InDelta(t, 41.2, cached.Usage.Week.Utilization, 0.001)
	require.Equal(t, time.Unix(1786500000, 0).UTC(), cached.Usage.Session.ResetsAt.UTC())
	require.Equal(t, time.Unix(1786900000, 0).UTC(), cached.Usage.Week.ResetsAt.UTC())
}

func TestHandleStatuslineWithoutRateLimits(t *testing.T) {
	cachePath := statuslineHome(t)

	var out strings.Builder
	input := `{"model": {"display_name": "Opus"}, "workspace": {"current_dir": "/tmp/x"}}`
	require.NoError(t, HandleStatusline(strings.NewReader(input), &out, os.Stderr, ""))
	require.Equal(t, "Opus · x", strings.TrimSpace(out.String()))
	require.NoFileExists(t, cachePath)
}

func TestHandleStatuslineWrapPassesThrough(t *testing.T) {
	cachePath := statuslineHome(t)

	var out strings.Builder
	require.NoError(t, HandleStatusline(strings.NewReader(statuslineInput), &out, os.Stderr, "cat"))
	require.Equal(t, statuslineInput, out.String())
	require.FileExists(t, cachePath)
}

func TestHandleStatuslineBadInput(t *testing.T) {
	cachePath := statuslineHome(t)

	var out strings.Builder
	require.NoError(t, HandleStatusline(strings.NewReader("not json"), &out, os.Stderr, ""))
	require.Empty(t, out.String())
	require.NoFileExists(t, cachePath)
}
