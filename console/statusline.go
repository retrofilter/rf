package console

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"time"

	"github.com/retrofilter/rf/rsh"
)

type statuslinePayload struct {
	Cwd   string `json:"cwd"`
	Model struct {
		DisplayName string `json:"display_name"`
	} `json:"model"`
	Workspace struct {
		CurrentDir string `json:"current_dir"`
	} `json:"workspace"`
	ContextWindow struct {
		UsedPercentage *float64 `json:"used_percentage"`
	} `json:"context_window"`
	RateLimits struct {
		FiveHour *statuslineWindow `json:"five_hour"`
		SevenDay *statuslineWindow `json:"seven_day"`
	} `json:"rate_limits"`
}

type statuslineWindow struct {
	UsedPercentage float64 `json:"used_percentage"`
	ResetsAt       int64   `json:"resets_at"`
}

// HandleStatusline processes one statusline invocation: payload JSON on
// stdin, the rendered line on stdout.
func HandleStatusline(stdin io.Reader, stdout, stderr io.Writer, wrap string) error {
	raw, err := io.ReadAll(io.LimitReader(stdin, 1<<20))
	if err != nil {
		raw = nil
	}
	var payload statuslinePayload
	parsed := json.Unmarshal(raw, &payload) == nil

	if parsed && (payload.RateLimits.FiveHour != nil || payload.RateLimits.SevenDay != nil) {
		if path := usageCachePath(); path != "" {
			_ = saveUsageCache(path, statuslineUsage(payload))
		}
	}

	if wrap != "" {
		_, err := rsh.Run(context.Background(), wrap, strings.NewReader(string(raw)), stdout, stderr)
		return err
	}
	if parsed {
		if line := defaultStatusline(payload); line != "" {
			fmt.Fprintln(stdout, line)
		}
	}
	return nil
}

func statuslineUsage(p statuslinePayload) *Usage {
	usage := &Usage{}
	if w := p.RateLimits.FiveHour; w != nil {
		usage.Session = UsageWindow{Utilization: w.UsedPercentage, ResetsAt: time.Unix(w.ResetsAt, 0)}
	}
	if w := p.RateLimits.SevenDay; w != nil {
		usage.Week = UsageWindow{Utilization: w.UsedPercentage, ResetsAt: time.Unix(w.ResetsAt, 0)}
	}
	return usage
}

func defaultStatusline(p statuslinePayload) string {
	var parts []string
	if p.Model.DisplayName != "" {
		parts = append(parts, p.Model.DisplayName)
	}
	dir := p.Workspace.CurrentDir
	if dir == "" {
		dir = p.Cwd
	}
	if dir != "" {
		parts = append(parts, filepath.Base(dir))
	}
	if pct := p.ContextWindow.UsedPercentage; pct != nil {
		parts = append(parts, fmt.Sprintf("ctx %d%%", clampPct(*pct)))
	}
	if w := p.RateLimits.FiveHour; w != nil {
		parts = append(parts, fmt.Sprintf("5h %d%%", clampPct(w.UsedPercentage)))
	}
	if w := p.RateLimits.SevenDay; w != nil {
		parts = append(parts, fmt.Sprintf("wk %d%%", clampPct(w.UsedPercentage)))
	}
	return strings.Join(parts, " · ")
}
