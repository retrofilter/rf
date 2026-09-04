package console

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/retrofilter/rf/core"
	"github.com/stretchr/testify/require"
)

func TestLoadTokenPersistsAndRotates(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	tok, err := LoadToken(false)
	require.NoError(t, err)
	require.Len(t, tok, 48)

	again, err := LoadToken(false)
	require.NoError(t, err)
	require.Equal(t, tok, again, "token must be stable across runs")

	rotated, err := LoadToken(true)
	require.NoError(t, err)
	require.NotEqual(t, tok, rotated)

	home, _ := os.UserHomeDir()
	info, err := os.Stat(filepath.Join(home, filepath.FromSlash(core.WebTokenRel)))
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0600), info.Mode().Perm())

	dirInfo, err := os.Stat(filepath.Join(home, core.StateDirName))
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0700), dirInfo.Mode().Perm())
}

func TestLoadTokenRotatesWhenAged(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	tok, err := LoadToken(false)
	require.NoError(t, err)

	home, _ := os.UserHomeDir()
	path := filepath.Join(home, filepath.FromSlash(core.WebTokenRel))
	stale := time.Now().Add(-tokenMaxAge - time.Hour)
	require.NoError(t, os.Chtimes(path, stale, stale))

	rotated, err := LoadToken(false)
	require.NoError(t, err)
	require.NotEqual(t, tok, rotated)

	// The fresh file is stable again.
	again, err := LoadToken(false)
	require.NoError(t, err)
	require.Equal(t, rotated, again)
}
