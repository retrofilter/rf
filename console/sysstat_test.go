//go:build linux || darwin

package console

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestSysStatsGet(t *testing.T) {
	s := newSysStats()
	require.True(t, s.primed)

	stat := s.Get()
	require.NotNil(t, stat)
	require.Greater(t, stat.Mem, 0.0)
	require.LessOrEqual(t, stat.Mem, 100.0)

	again := s.Get()
	require.Equal(t, stat, again)
	require.NotSame(t, stat, again)
	again.Mem = -1
	require.Equal(t, stat.Mem, s.Get().Mem)

	// Past the throttle a fresh sample replaces it.
	s.mu.Lock()
	s.sampledAt = time.Now().Add(-sysStatInterval - time.Second)
	before := s.last
	s.mu.Unlock()
	s.Get()
	s.mu.Lock()
	require.NotSame(t, before, s.last)
	s.mu.Unlock()
}

func TestSysStatsUnprimed(t *testing.T) {
	require.Nil(t, (&sysStats{}).Get())
}
