package console

import (
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"sort"
	"time"
)

const persistInterval = time.Minute

type persistedSession struct {
	SpawnName string `json:"spawn_name"`
	Name      string `json:"name"`
	Dir       string `json:"dir"`
	Claude    string `json:"claude,omitempty"`
}

func (m *Manager) persistLocked() {
	if m.statePath == "" {
		return
	}
	rows := make([]persistedSession, 0, len(m.sessions))
	for _, ls := range m.sessions {
		snap := ls.snapshot()
		rows = append(rows, persistedSession{SpawnName: snap.SpawnName, Name: snap.Name, Dir: snap.Cwd, Claude: snap.ClaudeID})
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].SpawnName < rows[j].SpawnName })
	data, err := json.MarshalIndent(rows, "", "  ")
	if err != nil {
		return
	}
	if string(data) == string(m.lastState) {
		return
	}
	tmp := m.statePath + ".tmp"
	if err := os.WriteFile(tmp, append(data, '\n'), 0600); err != nil {
		return
	}
	if err := os.Rename(tmp, m.statePath); err != nil {
		return
	}
	m.lastState = data
}

func (m *Manager) persistLoop() {
	for range time.Tick(persistInterval) {
		m.mu.Lock()
		m.persistLocked()
		m.mu.Unlock()
	}
}

var claudeIDRe = regexp.MustCompile(`^[a-zA-Z0-9-]{8,64}$`)

// Recover respawns the sessions recorded in the state file — the fleet as it
// stood when the previous server died.
func (m *Manager) Recover() ([]string, error) {
	if m.statePath == "" {
		return nil, nil
	}
	data, err := os.ReadFile(m.statePath)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var rows []persistedSession
	if err := json.Unmarshal(data, &rows); err != nil {
		return nil, fmt.Errorf("parse %s: %w", m.statePath, err)
	}
	var names []string
	for _, r := range rows {
		if r.SpawnName == "" {
			continue
		}
		dir := r.Dir
		if dir != "" {
			if info, err := os.Stat(dir); err != nil || !info.IsDir() {
				dir = "" // the directory is gone; a home-dir shell beats no shell
			}
		}
		initial := ""
		if dir != "" && r.Claude != "" && claudeIDRe.MatchString(r.Claude) {
			initial = "claude --resume " + shellQuote(r.Claude)
		}
		if _, err := m.Spawn(r.SpawnName, dir, initial); err != nil {
			continue
		}
		name := r.SpawnName
		if r.Name != "" && r.Name != r.SpawnName && m.Rename(r.SpawnName, r.Name) == nil {
			name = r.Name
		}
		names = append(names, name)
	}
	return names, nil
}
