package console

import (
	"sync"
	"time"
)

// SysStat is the status-bar model. Mem is percent, 0–100 (drives the bar
// fill); UsedKB is the same used figure raw, for the GB label.
type SysStat struct {
	Mem    float64
	UsedKB uint64
}

const sysStatInterval = 30 * time.Second

type sysStats struct {
	mu        sync.Mutex
	primed    bool // the constructor's probe could read /proc
	sampledAt time.Time
	last      *SysStat
}

func newSysStats() *sysStats {
	s := &sysStats{}
	if stat, err := readMemStat(); err == nil {
		s.primed = true
		s.sampledAt = time.Now()
		s.last = &stat
	}
	return s
}

func (s *sysStats) Get() *SysStat {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.primed {
		return nil
	}
	if time.Since(s.sampledAt) >= sysStatInterval {
		if stat, err := readMemStat(); err == nil {
			s.sampledAt = time.Now()
			s.last = &stat
		}
	}
	cp := *s.last
	return &cp
}
