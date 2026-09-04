package integration

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/creack/pty"
)

var rfBinary string

func TestMain(m *testing.M) {
	if os.Getenv("RF_SKIP_INTEGRATION") != "" {
		os.Exit(0)
	}
	tmp, err := os.MkdirTemp("", "rf-integration")
	if err != nil {
		fmt.Fprintln(os.Stderr, "mkdtemp:", err)
		os.Exit(1)
	}
	defer os.RemoveAll(tmp)

	rfBinary = filepath.Join(tmp, "rf")
	build := exec.Command("go", "build", "-o", rfBinary, "..")
	build.Stderr = os.Stderr
	if err := build.Run(); err != nil {
		fmt.Fprintln(os.Stderr, "failed to build rf:", err)
		os.Exit(1)
	}

	os.Exit(m.Run())
}

var (
	ansiRE  = regexp.MustCompile(`\x1b(\[[0-9;?]*[ -/]*[@-~]|\][^\x07]*\x07|[()][0-9A-B])`)
	dsrRE   = []byte("\x1b[6n")
	osc11RE = []byte("\x1b]11;?\a")
)

type shell struct {
	t        *testing.T
	pty      *os.File
	cmd      *exec.Cmd
	mu       sync.Mutex
	text     bytes.Buffer
	dsrSeen  int
	lastRead time.Time
	lastLine string
	idle     bool
	done     chan struct{}
}

func modelPrelude(t *testing.T, dir, url string) {
	t.Helper()
	scm := fmt.Sprintf("(define default-model {:provider \"ollama\" :base-url %q})\n", url)
	if err := os.WriteFile(filepath.Join(dir, ".rf.scm"), []byte(scm), 0644); err != nil {
		t.Fatal(err)
	}
}

func isolatedEnv(key string) bool {
	switch key {
	case "HOME", "RF_SESSION", "RF_WEB_PORT":
		return true
	}
	return strings.HasPrefix(key, "RF_") && strings.HasSuffix(key, "_API_KEY")
}

func startShell(t *testing.T, dir string, extraEnv ...string) *shell {
	t.Helper()
	s := launchShell(t, dir, extraEnv...)
	s.expect(`\$ `)
	return s
}

func launchShell(t *testing.T, dir string, extraEnv ...string) *shell {
	t.Helper()

	if resolved, err := filepath.EvalSymlinks(dir); err == nil {
		dir = resolved
	}

	cmd := exec.Command(rfBinary)
	cmd.Dir = dir
	for _, kv := range os.Environ() {
		key, _, _ := strings.Cut(kv, "=")
		if isolatedEnv(key) {
			continue
		}
		cmd.Env = append(cmd.Env, kv)
	}
	cmd.Env = append(cmd.Env, "TERM=xterm-256color", "HOME="+dir, "RF_SKIP_ONBOARDING=1")
	if os.Getenv("GO_POTION_HOME") == "" {
		if cache, err := os.UserCacheDir(); err == nil {
			cmd.Env = append(cmd.Env, "GO_POTION_HOME="+filepath.Join(cache, "go-potion"))
		}
	}
	cmd.Env = append(cmd.Env, extraEnv...)

	master, err := pty.StartWithSize(cmd, &pty.Winsize{Rows: 24, Cols: 200})
	if err != nil {
		t.Fatalf("failed to start rf under pty: %v", err)
	}
	ptmx := pollable(t, master)

	s := &shell{t: t, pty: ptmx, cmd: cmd, done: make(chan struct{})}
	go s.pump()

	t.Cleanup(func() { s.close() })
	return s
}

func pollable(t *testing.T, f *os.File) *os.File {
	t.Helper()
	fd, err := syscall.Dup(int(f.Fd()))
	if err != nil {
		t.Fatal(err)
	}
	f.Close()
	if err := syscall.SetNonblock(fd, true); err != nil {
		t.Fatal(err)
	}
	return os.NewFile(uintptr(fd), "pty")
}

func (s *shell) pump() {
	defer close(s.done)
	buf := make([]byte, 4096)
	for {
		n, err := s.pty.Read(buf)
		if n > 0 {
			chunk := buf[:n]
			// Answer every ESC[6n cursor query like a real terminal
			queries := bytes.Count(chunk, dsrRE)
			for i := queries; i > 0; i-- {
				s.pty.Write([]byte("\x1b[24;1R"))
			}
			// Answer the startup background probe like a dark terminal.
			for i := bytes.Count(chunk, osc11RE); i > 0; i-- {
				s.pty.Write([]byte("\x1b]11;rgb:1e1e/1e1e/1e1e\x1b\\"))
			}
			s.mu.Lock()
			s.dsrSeen += queries
			s.text.Write(ansiRE.ReplaceAll(chunk, nil))
			s.lastRead = time.Now()
			s.mu.Unlock()
		}
		if err != nil {
			return
		}
	}
}

const expectTimeout = 8 * time.Second

func (s *shell) expect(pattern string) string {
	s.t.Helper()
	re := regexp.MustCompile(pattern)
	deadline := time.Now().Add(expectTimeout)
	for time.Now().Before(deadline) {
		s.mu.Lock()
		match := re.Find(s.text.Bytes())
		s.mu.Unlock()
		if match != nil {
			return string(match)
		}
		time.Sleep(20 * time.Millisecond)
	}
	s.mu.Lock()
	transcript := s.text.String()
	s.mu.Unlock()
	s.t.Fatalf("timed out waiting for %q\n--- transcript ---\n%s", pattern, transcript)
	return ""
}

func (s *shell) expectNot(pattern string) {
	s.t.Helper()
	re := regexp.MustCompile(pattern)
	s.settle()
	s.mu.Lock()
	defer s.mu.Unlock()
	if match := re.Find(s.text.Bytes()); match != nil {
		s.t.Fatalf("unexpected output %q\n--- transcript ---\n%s", match, s.text.String())
	}
}

func (s *shell) clear() {
	s.mu.Lock()
	if re := s.promptAfterEcho(); re != nil && re.Match(s.text.Bytes()) {
		s.idle = true
	}
	s.text.Reset()
	s.mu.Unlock()
}

func (s *shell) promptAfterEcho() *regexp.Regexp {
	if s.lastLine == "" {
		return nil
	}
	return regexp.MustCompile(regexp.QuoteMeta(s.lastLine) + `\r*\n(?s:.*)\$ `)
}

func (s *shell) send(text string) {
	s.t.Helper()
	s.lastLine = ""
	s.idle = false
	if _, err := s.pty.Write([]byte(text)); err != nil {
		s.t.Fatalf("pty write: %v", err)
	}
}

const settleTimeout = 2 * time.Second

func (s *shell) settle() {
	done := s.promptAfterEcho()
	if done == nil {
		time.Sleep(500 * time.Millisecond)
		return
	}
	deadline := time.Now().Add(settleTimeout)
	for time.Now().Before(deadline) {
		s.mu.Lock()
		ok := done.Match(s.text.Bytes())
		s.mu.Unlock()
		if ok {
			return
		}
		time.Sleep(pollInterval)
	}
}

const (
	readyQuiet   = 50 * time.Millisecond
	readyWait    = 2 * time.Second
	pollInterval = 5 * time.Millisecond
)

func (s *shell) waitReady() {
	if s.idle {
		return
	}
	done := s.promptAfterEcho()
	deadline := time.Now().Add(readyWait)
	for time.Now().Before(deadline) {
		s.mu.Lock()
		quiet := !s.lastRead.IsZero() && time.Since(s.lastRead) >= readyQuiet
		prompted := done != nil && done.Match(s.text.Bytes())
		s.mu.Unlock()
		if quiet || prompted {
			return
		}
		time.Sleep(pollInterval)
	}
}

func (s *shell) sendLine(text string) {
	s.t.Helper()
	s.waitReady()
	s.send(text + "\r")
	s.lastLine = text
}

func (s *shell) close() {
	s.cmd.Process.Kill()
	s.pty.Close() // unblocks pump while a background child holds the slave
	<-s.done
	s.cmd.Wait()
}
