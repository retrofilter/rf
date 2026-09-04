package console

import (
	"bytes"
	"maps"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// The registry assembles the session rail from the Manager's live PTYs, each
// joined with its directory's diff stats and whatever status evidence exists.
const (
	StatusRunning = "RUNNING"
	StatusBlocked = "BLOCKED"
	StatusReview  = "REVIEW"
	StatusIdle    = "IDLE"
	// StatusScheduled marks a rail row for a session that doesn't exist yet
	// — a schedule (schedule.go) waiting for its fire time.
	StatusScheduled = "SCHEDULED"
)

const activityWindow = 5 * time.Second

const cacheTTL = 5 * time.Second

// Session is one rail row. ID is the identity (attach targets, selection);
// Name is the user's chosen session name.
type Session struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Title   string `json:"title"`   // what the program reports via OSC 0/2
	Dir     string `json:"dir"`     // last OSC 7 working-directory report
	Harness string `json:"harness"` // foreground command: "rf", "claude", …
	Status  string `json:"status"`
	Task    string `json:"task"`
	Elapsed int64  `json:"elapsed"` // seconds since the session started
	Added   int    `json:"added"`
	Deleted int    `json:"deleted"`

	// Scheduled rows only (Status == StatusScheduled): the command the
	// session will open with, its cadence as spelled, and the next fire
	// time as unix seconds.
	Command string `json:"command,omitempty"`
	Every   string `json:"every,omitempty"`
	Next    int64  `json:"next,omitempty"`
}

type hookState struct {
	Status string
	At     time.Time
}

type Registry struct {
	mgr *Manager

	buildMu sync.Mutex

	mu      sync.Mutex
	hooks   map[string]hookState
	cache   []Session
	cacheAt time.Time
	now     func() time.Time // injectable for tests
}

func NewRegistry(mgr *Manager) *Registry {
	return &Registry{
		mgr:   mgr,
		hooks: map[string]hookState{},
		now:   time.Now,
	}
}

// ReportHook records a hook event for a session (see hooks.go for the wire
// format; the key is the RF_SESSION spawn name). Unknown event names leave
// status untouched.
func (r *Registry) ReportHook(session, event string) {
	status := map[string]string{
		"PreToolUse":       StatusRunning,
		"PostToolUse":      StatusRunning,
		"UserPromptSubmit": StatusRunning,
		"Notification":     StatusBlocked,
		"Stop":             StatusReview,
	}[event]
	r.mu.Lock()
	defer r.mu.Unlock()
	prev := r.hooks[session]
	if status == "" {
		status = prev.Status
	}
	r.hooks[session] = hookState{Status: status, At: r.now()}
	r.cacheAt = time.Time{} // next Sessions() sees the new status immediately
}

// Invalidate drops the rail cache so the next Sessions() is fresh — called
// after a spawn so the UI's immediate re-poll sees the new session.
func (r *Registry) Invalidate() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.cacheAt = time.Time{}
}

// Sessions returns the current rail rows, cached for cacheTTL.
func (r *Registry) Sessions() ([]Session, error) {
	r.mu.Lock()
	if r.cache != nil && r.now().Sub(r.cacheAt) < cacheTTL {
		rows := r.cache
		r.mu.Unlock()
		return rows, nil
	}
	r.mu.Unlock()

	r.buildMu.Lock()
	defer r.buildMu.Unlock()
	// A poller that waited on buildMu usually finds the winner's cache.
	r.mu.Lock()
	if r.cache != nil && r.now().Sub(r.cacheAt) < cacheTTL {
		rows := r.cache
		r.mu.Unlock()
		return rows, nil
	}
	hooks := maps.Clone(r.hooks)
	r.mu.Unlock()

	rows := []Session{}
	live := map[string]bool{}
	diffs := map[string][2]int{} // several sessions in one directory diff once
	for _, ls := range r.mgr.List() {
		snap := ls.snapshot()
		live[snap.SpawnName] = true
		a := Session{
			ID:      snap.ID,
			Name:    snap.Name,
			Title:   snap.Title,
			Dir:     snap.Cwd,
			Harness: harnessOf(ls.ForegroundCommand()),
			Status:  statusOf(hooks[snap.SpawnName], snap.Created, snap.LastOutput, r.now()),
		}
		if !snap.Created.IsZero() {
			a.Elapsed = int64(r.now().Sub(snap.Created).Seconds())
		}
		d, ok := diffs[snap.Cwd]
		if !ok {
			d[0], d[1] = gitDiffStat(snap.Cwd)
			diffs[snap.Cwd] = d
		}
		a.Added, a.Deleted = d[0], d[1]
		rows = append(rows, a)
	}
	sort.Slice(rows, func(i, j int) bool { return sessionLess(rows[i].Name, rows[j].Name) })

	r.mu.Lock()
	for name := range r.hooks {
		if !live[name] {
			delete(r.hooks, name)
		}
	}
	r.cache = rows
	r.cacheAt = r.now()
	r.mu.Unlock()
	return rows, nil
}

func statusOf(hs hookState, created, activity, now time.Time) string {
	if hs.Status != "" && hs.At.After(created) {
		if !activity.After(hs.At.Add(2 * time.Second)) {
			return hs.Status
		}
	}
	if !activity.IsZero() && now.Sub(activity) < activityWindow {
		return StatusRunning
	}
	return StatusIdle
}

func sessionLess(a, b string) bool {
	na, erra := strconv.Atoi(a)
	nb, errb := strconv.Atoi(b)
	switch {
	case erra == nil && errb == nil:
		return na < nb
	case erra == nil:
		return true
	case errb == nil:
		return false
	}
	return a < b
}

func harnessOf(command string) string {
	switch {
	case strings.Contains(command, "claude"):
		return "claude"
	case command == "rf":
		return "rf"
	}
	return ""
}

func gitDiffStat(dir string) (added, deleted int) {
	if dir == "" {
		return 0, 0
	}
	cmd := exec.Command("git", "-C", dir, "diff", "HEAD", "--numstat")
	var out bytes.Buffer
	cmd.Stdout = &out
	if err := cmd.Run(); err != nil {
		return 0, 0
	}
	for line := range strings.SplitSeq(out.String(), "\n") {
		f := strings.Fields(line)
		if len(f) < 2 {
			continue
		}
		if a, err := strconv.Atoi(f[0]); err == nil {
			added += a
		}
		if d, err := strconv.Atoi(f[1]); err == nil {
			deleted += d
		}
	}
	return added, deleted
}
