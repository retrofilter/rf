package console

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

func (s *Server) handleHook(w http.ResponseWriter, r *http.Request) {
	shell := r.URL.Query().Get("session")
	if shell == "" {
		http.Error(w, "session parameter required", http.StatusBadRequest)
		return
	}
	var payload struct {
		Event     string `json:"hook_event_name"`
		SessionID string `json:"session_id"`
	}
	_ = json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&payload)
	if payload.Event == "" {
		payload.Event = r.URL.Query().Get("event")
	}
	if payload.SessionID == "" {
		payload.SessionID = r.URL.Query().Get("claude")
	}
	s.reg.ReportHook(shell, payload.Event)
	if payload.Event == "SessionEnd" {
		s.mgr.ClearClaudeSession(shell, payload.SessionID)
	} else {
		s.mgr.SetClaudeSession(shell, payload.SessionID)
	}
	w.WriteHeader(http.StatusNoContent)
}

var hookEvents = []string{"PreToolUse", "Notification", "Stop", "SessionStart", "SessionEnd"}

const hookFireCommand = `rf hook --fire >/dev/null 2>&1 || true`

const hookFireStdoutCommand = `rf hook --fire 2>/dev/null || true`

func hookCommand(event string) string {
	if event == "SessionStart" {
		return hookFireStdoutCommand
	}
	return hookFireCommand
}

const statuslineCommand = "rf statusline"

func installStatusline(settings map[string]any) {
	sl, _ := settings["statusLine"].(map[string]any)
	if sl == nil {
		sl = map[string]any{}
	}
	existing, _ := sl["command"].(string)
	if strings.Contains(existing, statuslineCommand) {
		return
	}
	command := statuslineCommand
	if existing != "" {
		command = statuslineCommand + " --wrap " + shellQuote(existing)
	}
	sl["type"] = "command"
	sl["command"] = command
	settings["statusLine"] = sl
}

func installClaudeHooks(path string) error {
	settings := map[string]any{}
	if b, err := os.ReadFile(path); err == nil {
		if err := json.Unmarshal(b, &settings); err != nil {
			return fmt.Errorf("%s exists but is not valid JSON: %w", path, err)
		}
	}
	hooks, _ := settings["hooks"].(map[string]any)
	if hooks == nil {
		hooks = map[string]any{}
	}
	for _, event := range hookEvents {
		cmds := []any{map[string]any{"type": "command", "command": hookCommand(event)}}
		hooks[event] = []any{map[string]any{"hooks": cmds}}
	}
	settings["hooks"] = hooks
	installStatusline(settings)
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(b, '\n'), 0644)
}

// ClaudeSettingsPath is where rf hook --install writes.
func ClaudeSettingsPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".claude", "settings.json"), nil
}

// InstallClaudeHooks installs the status and transcript-capture hooks into
// the user's global Claude Code settings.
func InstallClaudeHooks() (string, error) {
	path, err := ClaudeSettingsPath()
	if err != nil {
		return "", err
	}
	return path, installClaudeHooks(path)
}

// ClaudeHooksInstalled reports what the settings file holds: status (console
// callbacks on every hookEvent), capture (transcript sync on Stop), usage
// (the statusLine entry). A missing file is not installed.
func ClaudeHooksInstalled(path string) (status, capture, usage bool, err error) {
	b, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return false, false, false, nil
	}
	if err != nil {
		return false, false, false, err
	}
	var settings struct {
		Hooks      map[string]json.RawMessage `json:"hooks"`
		StatusLine struct {
			Command string `json:"command"`
		} `json:"statusLine"`
	}
	if err := json.Unmarshal(b, &settings); err != nil {
		return false, false, false, fmt.Errorf("%s: %w", path, err)
	}
	status = true
	for _, event := range hookEvents {
		if !strings.Contains(string(settings.Hooks[event]), "rf hook --fire") {
			status = false
		}
	}
	capture = strings.Contains(string(settings.Hooks["Stop"]), "rf hook --fire")
	usage = strings.Contains(settings.StatusLine.Command, statuslineCommand)
	return status, capture, usage, nil
}
