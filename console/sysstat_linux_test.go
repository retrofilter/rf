//go:build linux

package console

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestParseMemStat(t *testing.T) {
	stat, err := parseMemStat("MemTotal:       1000 kB\nMemFree:         100 kB\nMemAvailable:    250 kB\n")
	require.NoError(t, err)
	require.InDelta(t, 75.0, stat.Mem, 0.001)
	require.Equal(t, uint64(750), stat.UsedKB)

	_, err = parseMemStat("MemFree: 100 kB\n")
	require.Error(t, err)
	_, err = parseMemStat("MemTotal: 100 kB\nMemAvailable: 200 kB\n")
	require.Error(t, err)
}
