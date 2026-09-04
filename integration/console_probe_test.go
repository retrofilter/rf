package integration

import (
	"testing"
)

func TestConsoleSessionProbesCursor(t *testing.T) {
	t.Parallel()
	s := startShell(t, t.TempDir(), "RF_SESSION=probe-test")
	s.sendLine("(+ 1 2)")
	s.expect(`3`)
	s.mu.Lock()
	seen := s.dsrSeen
	s.mu.Unlock()
	if seen == 0 {
		t.Fatal("console session sent no ESC[6n cursor probes; probe must stay on under RF_SESSION")
	}
}

func TestPlainSessionStillProbes(t *testing.T) {
	t.Parallel()
	s := startShell(t, t.TempDir())
	s.sendLine("(+ 1 2)")
	s.expect(`3`)
	s.mu.Lock()
	seen := s.dsrSeen
	s.mu.Unlock()
	if seen == 0 {
		t.Fatal("plain session sent no ESC[6n cursor probes; probe should be on outside the console")
	}
}
