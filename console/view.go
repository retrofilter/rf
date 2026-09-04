package console

import (
	"fmt"
	"regexp"
	"strings"
	"time"
)

const (
	viewTerminal = "terminal"
	viewNew      = "new"
	viewOverview = "overview"
	viewGraph    = "graph"
)

type viewModel struct {
	Sessions  []Session
	Scheduled []Session // pending schedules (schedule.go); rail rows, never tabs
	Active    string    // selected session id, "" when none
	View      string    // viewTerminal, viewNew, viewOverview, or viewGraph
	GraphOpen bool      // the graph tab exists (client state, like Active/View)
	Usage     *Usage
	Sys       *SysStat
	Whoami    string
	Now       time.Time
}

func (m viewModel) runningCount() int {
	n := 0
	for _, s := range m.Sessions {
		if s.Status == StatusRunning {
			n++
		}
	}
	return n
}

func (m viewModel) activeSession() *Session {
	for i := range m.Sessions {
		if m.Sessions[i].ID == m.Active {
			return &m.Sessions[i]
		}
	}
	return nil
}

func (m viewModel) selected(s Session) bool {
	return m.View == viewTerminal && s.ID == m.Active
}

func (m viewModel) harnessLabel() string {
	a := m.activeSession()
	if a == nil {
		return ""
	}
	switch a.Harness {
	case "claude":
		return "Claude"
	case "":
		return "shell"
	}
	return a.Harness
}

var homeRe = regexp.MustCompile(`^/(?:Users|home)/[^/]+`)

func dirLabel(dir string) string {
	return homeRe.ReplaceAllString(dir, "~")
}

func detailOf(s Session) string {
	parts := []string{}
	if s.Title != "" {
		parts = append(parts, s.Title)
	}
	if s.Dir != "" {
		parts = append(parts, dirLabel(s.Dir))
	}
	if len(parts) > 0 {
		return strings.Join(parts, " · ")
	}
	if s.Task != "" {
		return s.Task
	}
	return "adopted session"
}

func fmtOpenCount(n int) string {
	if n == 1 {
		return "1 open"
	}
	return fmt.Sprintf("%d open", n)
}

func fmtElapsed(s int64) string {
	switch {
	case s <= 0:
		return ""
	case s >= 3600:
		return fmt.Sprintf("%dh %dm", s/3600, s%3600/60)
	case s >= 60:
		return fmt.Sprintf("%dm %ds", s/60, s%60)
	}
	return fmt.Sprintf("%ds", s)
}

func fmtDiff(s Session) string {
	if s.Added == 0 && s.Deleted == 0 {
		return "no changes"
	}
	return fmt.Sprintf("+%d −%d", s.Added, s.Deleted)
}

func fmtReset(at, now time.Time) string {
	if at.IsZero() {
		return ""
	}
	s := int64(at.Sub(now).Seconds())
	switch {
	case s <= 0:
		return ""
	case s >= 86400:
		return fmt.Sprintf("%dd %dh", s/86400, s%86400/3600)
	case s >= 3600:
		return fmt.Sprintf("%dh %dm", s/3600, s%3600/60)
	}
	return fmt.Sprintf("%dm", s/60)
}

func sessionWindow(u *Usage) *UsageWindow {
	if u == nil {
		return nil
	}
	return &u.Session
}

func weekWindow(u *Usage) *UsageWindow {
	if u == nil {
		return nil
	}
	return &u.Week
}

func memPct(s *SysStat) *float64 {
	if s == nil {
		return nil
	}
	return &s.Mem
}

func memGB(s *SysStat) string {
	if s == nil {
		return ""
	}
	gb := float64(s.UsedKB) / (1024 * 1024)
	if gb >= 10 {
		return fmt.Sprintf("%.0fG", gb)
	}
	return fmt.Sprintf("%.1fG", gb)
}

func sysFill(p *float64) string {
	if p == nil {
		return "width: 0%"
	}
	return fmt.Sprintf("width: %d%%", clampPct(*p))
}

func fillWidth(w *UsageWindow) string {
	if w == nil {
		return "width: 0%"
	}
	return fmt.Sprintf("width: %d%%", clampPct(w.Utilization))
}

func clampPct(u float64) int {
	if u < 0 {
		return 0
	}
	if u > 100 {
		return 100
	}
	return int(u + 0.5)
}
