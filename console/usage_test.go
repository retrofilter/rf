package console

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func writeUsageFile(t *testing.T, path string, fetchedAt time.Time, usage *Usage) {
	t.Helper()
	b, err := json.Marshal(usageCacheFile{FetchedAt: fetchedAt, Usage: usage})
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0755))
	require.NoError(t, os.WriteFile(path, b, 0600))
}

func TestUsageCacheServesAnyAge(t *testing.T) {
	path := filepath.Join(t.TempDir(), "usage-cache.json")
	writeUsageFile(t, path, time.Now().Add(-48*time.Hour), &Usage{
		Session: UsageWindow{Utilization: 34.5, ResetsAt: time.Now().Add(time.Hour)},
		Week:    UsageWindow{Utilization: 12, ResetsAt: time.Now().Add(72 * time.Hour)},
	})

	c := &usageCache{path: path}
	usage := c.Get()
	require.NotNil(t, usage)
	require.InDelta(t, 34.5, usage.Session.Utilization, 0.001)
	require.InDelta(t, 12.0, usage.Week.Utilization, 0.001)
}

func TestUsageCacheReloadsOnChange(t *testing.T) {
	path := filepath.Join(t.TempDir(), "usage-cache.json")
	future := time.Now().Add(time.Hour)
	writeUsageFile(t, path, time.Now(), &Usage{Session: UsageWindow{Utilization: 10, ResetsAt: future}})

	c := &usageCache{path: path}
	require.InDelta(t, 10.0, c.Get().Session.Utilization, 0.001)

	writeUsageFile(t, path, time.Now(), &Usage{Session: UsageWindow{Utilization: 55, ResetsAt: future}})
	require.NoError(t, os.Chtimes(path, time.Now(), time.Now().Add(2*time.Second)))
	require.InDelta(t, 55.0, c.Get().Session.Utilization, 0.001)
}

func TestUsageCacheClampsPassedResets(t *testing.T) {
	path := filepath.Join(t.TempDir(), "usage-cache.json")
	writeUsageFile(t, path, time.Now().Add(-6*time.Hour), &Usage{
		Session: UsageWindow{Utilization: 80, ResetsAt: time.Now().Add(-time.Hour)},
		Week:    UsageWindow{Utilization: 40, ResetsAt: time.Now().Add(24 * time.Hour)},
	})

	c := &usageCache{path: path}
	usage := c.Get()
	require.NotNil(t, usage)
	require.Zero(t, usage.Session.Utilization, "session window reset while idle")
	require.InDelta(t, 40.0, usage.Week.Utilization, 0.001, "week window still running")

	require.InDelta(t, 80.0, c.usage.Session.Utilization, 0.001)
}

func TestUsageCacheDegradesGracefully(t *testing.T) {
	dir := t.TempDir()

	// No file yet → nil, no error.
	c := &usageCache{path: filepath.Join(dir, "usage-cache.json")}
	require.Nil(t, c.Get())

	// Corrupt file → nil.
	require.NoError(t, os.WriteFile(c.path, []byte("{not json"), 0600))
	require.Nil(t, c.Get())

	// No home dir resolved → nil.
	require.Nil(t, (&usageCache{}).Get())
}

func TestSaveUsageCacheRoundTrips(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", "usage-cache.json")
	usage := &Usage{Session: UsageWindow{Utilization: 23.5, ResetsAt: time.Now().Add(time.Hour).Truncate(time.Second)}}
	require.NoError(t, saveUsageCache(path, usage))

	c := &usageCache{path: path}
	got := c.Get()
	require.NotNil(t, got)
	require.InDelta(t, 23.5, got.Session.Utilization, 0.001)

	// fetched_at is stamped for other readers of the shared file.
	b, err := os.ReadFile(path)
	require.NoError(t, err)
	var cached usageCacheFile
	require.NoError(t, json.Unmarshal(b, &cached))
	require.WithinDuration(t, time.Now(), cached.FetchedAt, time.Minute)

	// No stray temp files survive the atomic write.
	entries, err := os.ReadDir(filepath.Dir(path))
	require.NoError(t, err)
	require.Len(t, entries, 1)
}
