package console

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Usage is the header-bar model. Utilization is percent, 0–100.
type Usage struct {
	Session UsageWindow `json:"session"` // 5-hour window
	Week    UsageWindow `json:"week"`    // 7-day window, all models
}

type UsageWindow struct {
	Utilization float64   `json:"utilization"`
	ResetsAt    time.Time `json:"resets_at"`
}

const usageCacheName = "usage-cache.json"

type usageCacheFile struct {
	FetchedAt time.Time `json:"fetched_at"`
	Usage     *Usage    `json:"usage"`
}

func usageCachePath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".claude", usageCacheName)
}

func saveUsageCache(path string, usage *Usage) error {
	b, err := json.MarshalIndent(usageCacheFile{FetchedAt: time.Now(), Usage: usage}, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), usageCacheName+".*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if err := tmp.Chmod(0600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(append(b, '\n')); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

type usageCache struct {
	path string

	mu      sync.Mutex
	usage   *Usage
	modTime time.Time
	size    int64
}

func newUsageCache() *usageCache {
	return &usageCache{path: usageCachePath()}
}

func (c *usageCache) Get() *Usage {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.path == "" {
		return nil
	}
	if info, err := os.Stat(c.path); err == nil {
		if !info.ModTime().Equal(c.modTime) || info.Size() != c.size {
			c.modTime, c.size = info.ModTime(), info.Size()
			c.usage = readUsageCache(c.path)
		}
	}
	return clampResets(c.usage, time.Now())
}

func readUsageCache(path string) *Usage {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var cached usageCacheFile
	if json.Unmarshal(b, &cached) != nil {
		return nil
	}
	return cached.Usage
}

func clampResets(u *Usage, now time.Time) *Usage {
	if u == nil {
		return nil
	}
	clamped := *u
	for _, w := range []*UsageWindow{&clamped.Session, &clamped.Week} {
		if !w.ResetsAt.IsZero() && w.ResetsAt.Before(now) {
			w.Utilization = 0
		}
	}
	return &clamped
}
