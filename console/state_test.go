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

func readState(t *testing.T, path string) []persistedSession {
	t.Helper()
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	var rows []persistedSession
	require.NoError(t, json.Unmarshal(data, &rows))
	return rows
}

func TestFleetStatePersistsOnChange(t *testing.T) {
	statePath := filepath.Join(t.TempDir(), "state.json")
	dir := t.TempDir()
	m := NewManager("cat", 0)
	m.statePath = statePath

	// Spawn writes the fleet immediately.
	id, err := m.Spawn("alpha", dir, "")
	require.NoError(t, err)
	rows := readState(t, statePath)
	require.Equal(t, []persistedSession{{SpawnName: "alpha", Name: "alpha", Dir: dir}}, rows)

	// A rename keeps the spawn identity and updates the display name.
	require.NoError(t, m.Rename("alpha", "omega"))
	rows = readState(t, statePath)
	require.Equal(t, []persistedSession{{SpawnName: "alpha", Name: "omega", Dir: dir}}, rows)

	moved := t.TempDir()
	m.Get(id).ingest([]byte("\x1b]7;file://host" + moved + "\a"))
	m.mu.Lock()
	m.persistLocked()
	m.mu.Unlock()
	rows = readState(t, statePath)
	require.Equal(t, moved, rows[0].Dir)

	require.NoError(t, m.Get(id).Write([]byte{0x04}))
	require.Eventually(t, func() bool {
		return len(readState(t, statePath)) == 0
	}, 5*time.Second, 50*time.Millisecond, "exited session dropped from state file")
}

func TestRecoverRespawnsFleet(t *testing.T) {
	statePath := filepath.Join(t.TempDir(), "state.json")
	dir := t.TempDir()
	rows := []persistedSession{
		{SpawnName: "alpha", Name: "alpha", Dir: dir},
		{SpawnName: "beta", Name: "beta-renamed", Dir: dir},
		{SpawnName: "gone", Name: "gone", Dir: filepath.Join(dir, "no-such-dir")},
	}
	data, err := json.Marshal(rows)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(statePath, data, 0600))

	m := NewManager("cat", 0)
	m.statePath = statePath
	names, err := m.Recover()
	require.NoError(t, err)
	require.Equal(t, []string{"alpha", "beta-renamed", "gone"}, names)

	home, err := os.UserHomeDir()
	require.NoError(t, err)
	alpha := m.Get(m.FindByName("alpha"))
	require.NotNil(t, alpha)
	require.Equal(t, dir, alpha.snapshot().Cwd)
	beta := m.Get(m.FindByName("beta-renamed"))
	require.NotNil(t, beta)
	require.Equal(t, "beta", beta.SpawnName, "rename identity survives recovery")
	gone := m.Get(m.FindByName("gone"))
	require.NotNil(t, gone)
	require.Equal(t, home, gone.snapshot().Cwd)

	// Recovery through the respawns rewrote the file as the live fleet.
	require.Len(t, readState(t, statePath), 3)
}

func TestClaudeSessionTracking(t *testing.T) {
	statePath := filepath.Join(t.TempDir(), "state.json")
	dir := t.TempDir()
	m := NewManager("cat", 0)
	m.statePath = statePath
	_, err := m.Spawn("alpha", dir, "")
	require.NoError(t, err)

	// A hook-reported claude id reaches the state file immediately.
	m.SetClaudeSession("alpha", "aaaa-1111-bbbb")
	require.Equal(t, "aaaa-1111-bbbb", readState(t, statePath)[0].Claude)

	m.SetClaudeSession("alpha", "cccc-2222-dddd")
	m.ClearClaudeSession("alpha", "aaaa-1111-bbbb")
	require.Equal(t, "cccc-2222-dddd", readState(t, statePath)[0].Claude)

	m.ClearClaudeSession("alpha", "cccc-2222-dddd")
	require.Empty(t, readState(t, statePath)[0].Claude)

	// Unknown spawn names and empty ids are no-ops.
	m.SetClaudeSession("nope", "eeee-3333-ffff")
	m.SetClaudeSession("alpha", "")
	require.Empty(t, readState(t, statePath)[0].Claude)
}

func TestRecoverResumesClaudeSessions(t *testing.T) {
	statePath := filepath.Join(t.TempDir(), "state.json")
	dir := t.TempDir()
	rows := []persistedSession{
		{SpawnName: "work", Name: "work", Dir: dir, Claude: "abcd-1234-ef56"},
		// A hand-edited or corrupt id must never reach a shell line.
		{SpawnName: "evil", Name: "evil", Dir: dir, Claude: "$(rm -rf ~)"},
		{SpawnName: "lost", Name: "lost", Dir: filepath.Join(dir, "no-such-dir"), Claude: "aaaa-2222-bbbb"},
	}
	data, err := json.Marshal(rows)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(statePath, data, 0600))

	m := NewManager("cat", 0)
	m.statePath = statePath
	// cat never paints a prompt, so the typed resume rides the fallback timer.
	m.startGrace = 100 * time.Millisecond
	names, err := m.Recover()
	require.NoError(t, err)
	require.Equal(t, []string{"work", "evil", "lost"}, names)

	replay := func(name string) string {
		ls := m.Get(m.FindByName(name))
		require.NotNil(t, ls)
		b, _, cancel := ls.Subscribe()
		cancel()
		return string(b)
	}

	require.Eventually(t, func() bool {
		return strings.Contains(replay("work"), "claude --resume 'abcd-1234-ef56'")
	}, 3*time.Second, 50*time.Millisecond)

	time.Sleep(200 * time.Millisecond)
	require.NotContains(t, replay("evil"), "claude --resume")
	require.NotContains(t, replay("lost"), "claude --resume")
}

func TestRecoverWithoutStateFileIsEmpty(t *testing.T) {
	m := NewManager("cat", 0)
	m.statePath = filepath.Join(t.TempDir(), "state.json")
	names, err := m.Recover()
	require.NoError(t, err)
	require.Empty(t, names)

	// Persistence off ("" path): Recover is inert, Spawn writes nothing.
	off := NewManager("cat", 0)
	names, err = off.Recover()
	require.NoError(t, err)
	require.Empty(t, names)
}
