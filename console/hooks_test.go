package console

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestInstallClaudeHooks(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "settings.json")
	// Pre-existing settings must survive with unrelated keys and events intact.
	require.NoError(t, os.WriteFile(path,
		[]byte(`{"permissions":{"allow":["Bash(ls:*)"]},"hooks":{"UserPromptSubmit":[{"hooks":[]}]}}`), 0644))

	status, capture, usage, err := ClaudeHooksInstalled(path)
	require.NoError(t, err)
	require.False(t, status)
	require.False(t, capture)
	require.False(t, usage)

	require.NoError(t, installClaudeHooks(path))

	b, err := os.ReadFile(path)
	require.NoError(t, err)
	var settings map[string]any
	require.NoError(t, json.Unmarshal(b, &settings))

	require.Contains(t, settings, "permissions", "unrelated keys preserved")
	hooks, ok := settings["hooks"].(map[string]any)
	require.True(t, ok)
	require.Contains(t, hooks, "UserPromptSubmit", "unrelated events preserved")
	for _, event := range hookEvents {
		require.Contains(t, hooks, event)
		blob, _ := json.Marshal(hooks[event])
		var entries []struct {
			Hooks []struct {
				Command string `json:"command"`
			} `json:"hooks"`
		}
		require.NoError(t, json.Unmarshal(blob, &entries))
		require.Len(t, entries, 1)
		require.Len(t, entries[0].Hooks, 1)
		cmd := entries[0].Hooks[0].Command
		require.Contains(t, cmd, "rf hook --fire")
		require.NotContains(t, cmd, "curl")
		require.NotContains(t, cmd, "RF_SESSION")
		if event == "SessionStart" {
			require.Equal(t, hookFireStdoutCommand, cmd)
		} else {
			require.Equal(t, hookFireCommand, cmd)
		}
	}

	// A fresh install also wires the usage statusline.
	sl, ok := settings["statusLine"].(map[string]any)
	require.True(t, ok)
	require.Equal(t, "command", sl["type"])
	require.Equal(t, statuslineCommand, sl["command"])

	status, capture, usage, err = ClaudeHooksInstalled(path)
	require.NoError(t, err)
	require.True(t, status)
	require.True(t, capture)
	require.True(t, usage)

	// A missing file is simply not installed.
	status, capture, usage, err = ClaudeHooksInstalled(filepath.Join(dir, "nope.json"))
	require.NoError(t, err)
	require.False(t, status)
	require.False(t, capture)
	require.False(t, usage)

	// Corrupt settings must error out rather than be clobbered.
	require.NoError(t, os.WriteFile(path, []byte("{not json"), 0644))
	require.Error(t, installClaudeHooks(path))
	_, _, _, err = ClaudeHooksInstalled(path)
	require.Error(t, err)
}

func TestInstallStatuslineWrapsExisting(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "settings.json")
	require.NoError(t, os.WriteFile(path,
		[]byte(`{"statusLine":{"type":"command","command":"~/.claude/statusline.sh","padding":2}}`), 0644))

	require.NoError(t, installClaudeHooks(path))

	b, err := os.ReadFile(path)
	require.NoError(t, err)
	var settings map[string]any
	require.NoError(t, json.Unmarshal(b, &settings))
	sl := settings["statusLine"].(map[string]any)
	require.Equal(t, `rf statusline --wrap '~/.claude/statusline.sh'`, sl["command"])
	require.Equal(t, float64(2), sl["padding"], "unrelated statusLine fields preserved")

	// Reinstall is idempotent — never double-wraps.
	require.NoError(t, installClaudeHooks(path))
	b, err = os.ReadFile(path)
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(b, &settings))
	sl = settings["statusLine"].(map[string]any)
	require.Equal(t, `rf statusline --wrap '~/.claude/statusline.sh'`, sl["command"])

	_, _, usage, err := ClaudeHooksInstalled(path)
	require.NoError(t, err)
	require.True(t, usage)
}

func TestInstallClaudeTaskSkill(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	require.False(t, ClaudeTaskSkillInstalled())

	path, err := InstallClaudeTaskSkill()
	require.NoError(t, err)
	require.Equal(t, filepath.Join(home, ".claude", "skills", "task", "SKILL.md"), path)
	require.True(t, ClaudeTaskSkillInstalled())

	b, err := os.ReadFile(path)
	require.NoError(t, err)
	content := string(b)
	// The skill teaches the exact agent spellings.
	require.Contains(t, content, "name: task")
	require.Contains(t, content, `rf -e '(task "`)
	require.Contains(t, content, "{:complete 42}")
	require.Contains(t, content, "(tasks)")
}
