package console

import (
	"bytes"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/creack/pty"
	"golang.org/x/sys/unix"
)

const (
	ringMax    = 2 << 20   // bytes of replay kept per session
	ringKeep   = 1 << 20   // trimmed down to this when over
	subMax     = 8 << 20   // bytes a subscriber may fall behind before its backlog is cut
	subKeep    = 4 << 20   // cut down to this (at a line boundary) when over
	subChunk   = 256 << 10 // largest single frame handed to a subscriber
	pendingMax = 256       // bytes of held cursor probes; the blocked asker sends one
)

type subscriber struct {
	ch      chan []byte   // what the attach loop reads; closed on cancel or session exit
	wake    chan struct{} // cap 1; closed when the subscriber is retired
	done    chan struct{} // closed on cancel: the reader is gone, stop delivering
	backlog []byte        // output not yet handed to ch (under liveSession.mu)
}

type Manager struct {
	spawnCommand string        // test override; "" means the running rf binary
	port         int           // RF_WEB_PORT for spawned sessions
	onExit       func()        // called when a session leaves the fleet (PTY EOF)
	startGrace   time.Duration // initial-command fallback when no prompt is ever seen

	mu        sync.Mutex
	sessions  map[string]*liveSession
	nextID    int
	statePath string // fleet state file for restart recovery; "" disables
	lastState []byte // last state written, to skip no-change rewrites
}

func NewManager(spawnCommand string, port int) *Manager {
	return &Manager{spawnCommand: spawnCommand, port: port, startGrace: 5 * time.Second, sessions: map[string]*liveSession{}}
}

type liveSession struct {
	ID        string
	Name      string // display/find name; renameable (Manager.Rename)
	SpawnName string
	Created   time.Time
	cmd       *exec.Cmd
	ptmx      *os.File

	mu         sync.Mutex
	ring       []byte
	carry      []byte        // tail kept so escapes split across reads still parse
	pending    []byte        // cursor probes seen while detached, forwarded on next attach
	unattended bool          // initial command queued and no tab yet: cursor probes get a synthesized answer
	started    chan struct{} // closed at the shell's first prompt (OSC title/cwd seen); releases a queued initial command
	closed     bool          // reader cleanup ran; no new subscribers
	subs       map[chan []byte]*subscriber
	primary    chan []byte // the answering terminal (latest attach)
	lastOutput time.Time
	title      string
	cwd        string
	claudeID   string // the claude session id live in this shell (hook-reported)
	fullscreen bool
}

// ErrNameTaken reports a spawn against an existing session name.
var ErrNameTaken = fmt.Errorf("session name already in use")

// Spawn starts a new session: an rf shell (or the test override) on a fresh
// PTY, with the console environment stamped in.
func (m *Manager) Spawn(name, dir, initial string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, ls := range m.sessions {
		if ls.Name == name || ls.SpawnName == name {
			return "", ErrNameTaken
		}
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	if dir == "" {
		dir = home
	} else if info, err := os.Stat(dir); err != nil || !info.IsDir() {
		return "", fmt.Errorf("no such directory: %s", dir)
	}
	command := m.spawnCommand
	if command == "" {
		if exe, err := os.Executable(); err == nil {
			command = shellQuote(exe)
		} else {
			command = "rf"
		}
	}
	cmd := exec.Command("/bin/sh", "-c", command)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"RF_SESSION="+name,
		fmt.Sprintf("RF_WEB_PORT=%d", m.port),
		"TERM=xterm-256color",
		"COLORTERM=truecolor",
	)
	ptmx, err := pty.StartWithSize(cmd, &pty.Winsize{Cols: 80, Rows: 24})
	if err != nil {
		return "", err
	}

	m.nextID++
	ls := &liveSession{
		ID:        fmt.Sprintf("s%d", m.nextID),
		Name:      name,
		SpawnName: name,
		Created:   time.Now(),
		cmd:       cmd,
		ptmx:      ptmx,
		subs:      map[chan []byte]*subscriber{},
		cwd:       dir,
	}
	m.sessions[ls.ID] = ls
	m.persistLocked()
	initial = stripControl(initial)
	ls.unattended = initial != ""
	if initial != "" {
		ls.started = make(chan struct{})
		started := ls.started
		line := []byte("\x1b[200~" + initial + "\x1b[201~\r")
		grace := m.startGrace
		go func() {
			select {
			case <-started:
			case <-time.After(grace):
			}
			_, _ = ls.ptmx.Write(line)
		}()
	}
	go m.reader(ls)
	return ls.ID, nil
}

func (m *Manager) reader(ls *liveSession) {
	buf := make([]byte, 8192)
	for {
		n, err := ls.ptmx.Read(buf)
		if n > 0 {
			ls.ingest(buf[:n])
		}
		if err != nil {
			break
		}
	}
	_ = ls.ptmx.Close()
	_ = ls.cmd.Wait()
	m.mu.Lock()
	delete(m.sessions, ls.ID)
	m.persistLocked()
	m.mu.Unlock()
	if m.onExit != nil {
		m.onExit()
	}
	ls.mu.Lock()
	ls.closed = true
	for _, sub := range ls.subs {
		sub.retireLocked()
	}
	ls.subs = nil
	ls.mu.Unlock()
}

func (sub *subscriber) retireLocked() {
	close(sub.wake)
}

func (sub *subscriber) pump(ls *liveSession) {
	defer close(sub.ch)
	for {
		if _, open := <-sub.wake; !open {
			// Retired: deliver whatever is left, then close.
			sub.drain(ls)
			return
		}
		if !sub.drain(ls) {
			return
		}
	}
}

func (sub *subscriber) drain(ls *liveSession) bool {
	for {
		ls.mu.Lock()
		if len(sub.backlog) == 0 {
			ls.mu.Unlock()
			return true
		}
		n := len(sub.backlog)
		if n > subChunk {
			n = subChunk
		}
		frame := sub.backlog[:n:n]
		sub.backlog = sub.backlog[n:]
		if len(sub.backlog) == 0 {
			sub.backlog = nil // let the drained buffer go rather than pinning it
		}
		ls.mu.Unlock()
		select {
		case sub.ch <- frame:
		case <-sub.done:
			return false
		}
	}
}

func (sub *subscriber) pushLocked(c []byte) {
	sub.backlog = append(sub.backlog, c...)
	if len(sub.backlog) > subMax {
		cut := len(sub.backlog) - subKeep
		if i := bytes.IndexByte(sub.backlog[cut:], '\n'); i >= 0 && i < 64<<10 {
			cut += i + 1
		}
		sub.backlog = append([]byte{}, sub.backlog[cut:]...)
	}
	select {
	case sub.wake <- struct{}{}:
	default:
	}
}

func (ls *liveSession) ingest(chunk []byte) {
	c := make([]byte, len(chunk))
	copy(c, chunk)

	ls.mu.Lock()
	ls.lastOutput = time.Now()
	ls.ring = append(ls.ring, c...)
	if len(ls.ring) > ringMax {
		ls.ring = append([]byte{}, ls.ring[len(ls.ring)-ringKeep:]...)
	}
	answers := ls.scan(c)
	for _, sub := range ls.subs {
		sub.pushLocked(c)
	}
	ls.mu.Unlock()

	for _, a := range answers {
		_, _ = ls.ptmx.WriteString(a)
	}
}

func (ls *liveSession) promoteLocked(ch chan []byte) {
	if ls.primary != ch {
		return
	}
	ls.primary = nil
	for other := range ls.subs {
		ls.primary = other
		break
	}
}

var (
	oscRe = regexp.MustCompile(`\x1b\](\d+);([^\x07\x1b]*)(?:\x07|\x1b\\)`)
	altRe = regexp.MustCompile(`\x1b\[\?(?:1049|1047|47)([hl])`)
)

var queryRe = regexp.MustCompile("\x1b\\[(6n|5n|0?c)")

var oscColorQueryRe = regexp.MustCompile("\x1b\\](1[01]);\\?(\x07|\x1b\\\\)")

const unknownCursorPos = "\x1b[0;0R"

var oscColorAnswers = map[string]string{
	"10": "rgb:3838/3a3a/4242",
	"11": "rgb:fafa/fafa/fafa",
}

func (ls *liveSession) releaseStarted() {
	if ls.started != nil {
		close(ls.started)
		ls.started = nil
	}
}

func (ls *liveSession) scan(chunk []byte) (answers []string) {
	boundary := len(ls.carry)
	data := append(append([]byte{}, ls.carry...), chunk...)
	for _, mt := range oscRe.FindAllSubmatch(data, -1) {
		code, body := string(mt[1]), string(mt[2])
		switch code {
		case "0", "2":
			ls.title = body
			ls.releaseStarted()
		case "7":
			if u, err := url.Parse(body); err == nil && u.Path != "" {
				if p, err := url.PathUnescape(u.Path); err == nil {
					ls.cwd = p
				} else {
					ls.cwd = u.Path
				}
			}
			ls.releaseStarted()
		}
	}
	if alts := altRe.FindAllSubmatch(data, -1); len(alts) > 0 {
		ls.fullscreen = string(alts[len(alts)-1][1]) == "h"
	}
	if len(ls.subs) == 0 {
		for _, m := range queryRe.FindAllSubmatchIndex(data, -1) {
			if m[1] <= boundary {
				continue // already answered in an earlier ingest
			}
			switch string(data[m[2]:m[3]]) {
			case "6n":
				if ls.unattended {
					answers = append(answers, unknownCursorPos)
				} else if len(ls.pending) < pendingMax {
					ls.pending = append(ls.pending, data[m[0]:m[1]]...)
				}
			case "5n":
				answers = append(answers, "\x1b[0n")
			default: // DA1
				answers = append(answers, "\x1b[?6c")
			}
		}
		for _, m := range oscColorQueryRe.FindAllSubmatchIndex(data, -1) {
			if m[1] <= boundary {
				continue // already answered in an earlier ingest
			}
			code := string(data[m[2]:m[3]])
			terminator := string(data[m[4]:m[5]])
			answers = append(answers, "\x1b]"+code+";"+oscColorAnswers[code]+terminator)
		}
	}
	if len(data) > 256 {
		data = data[len(data)-256:]
	}
	ls.carry = append([]byte{}, data...)
	return answers
}

var replayScrub = regexp.MustCompile(
	"\x1b\\[[0-9;?>=]*[nct]" +
		"|\x1b\\[\\?[0-9;]*\\$p" +
		"|\x1bP[^\x1b]*\x1b\\\\" +
		"|\x1b\\](?:1[0-2]|4;[0-9]+);\\?(?:\x07|\x1b\\\\)")

func (ls *liveSession) Subscribe() (replay []byte, ch chan []byte, cancel func()) {
	sub := &subscriber{ch: make(chan []byte), wake: make(chan struct{}, 1), done: make(chan struct{})}
	ch = sub.ch
	ls.mu.Lock()
	if ls.subs == nil {
		ls.subs = map[chan []byte]*subscriber{}
	}
	if ls.closed {
		ls.mu.Unlock()
		close(ch)
		return nil, ch, func() {}
	}
	replay = replayScrub.ReplaceAll(ls.ring, nil)
	replay = append(replay, ls.pending...)
	ls.subs[ch] = sub
	ls.primary = ch // latest attach answers queries (and drives sizing)
	ls.unattended = false
	ls.mu.Unlock()
	go sub.pump(ls)
	return replay, ch, func() {
		ls.mu.Lock()
		if _, ok := ls.subs[ch]; ok {
			close(sub.done)
			sub.retireLocked()
			delete(ls.subs, ch)
			ls.promoteLocked(ch)
		}
		ls.mu.Unlock()
	}
}

var responseScrub = regexp.MustCompile(
	"\x1b\\[[0-9;?]*[Rnc]" +
		"|\x1bP[^\x1b]*\x1b\\\\" +
		"|\x1b\\](?:1[0-2]|4;[0-9]+);[^\x07\x1b]*(?:\x07|\x1b\\\\)")

var cprRe = regexp.MustCompile("\x1b\\[[0-9;]*R")

func (ls *liveSession) WriteInput(data []byte, ch chan []byte) error {
	ls.mu.Lock()
	primary := ls.primary == ch
	if primary && len(ls.pending) > 0 && cprRe.Match(data) {
		ls.pending = nil
	}
	ls.mu.Unlock()
	if !primary {
		data = responseScrub.ReplaceAll(data, nil)
		if len(data) == 0 {
			return nil
		}
	}
	return ls.Write(data)
}

func (ls *liveSession) Write(data []byte) error {
	_, err := ls.ptmx.Write(data)
	return err
}

func (ls *liveSession) Resize(cols, rows uint16) {
	if cols > 0 && rows > 0 {
		_ = pty.Setsize(ls.ptmx, &pty.Winsize{Cols: cols, Rows: rows})
	}
}

type sessionSnapshot struct {
	ID, Name, SpawnName, Title, Cwd string
	ClaudeID                        string
	Created, LastOutput             time.Time
	Fullscreen                      bool
}

func (ls *liveSession) snapshot() sessionSnapshot {
	ls.mu.Lock()
	defer ls.mu.Unlock()
	return sessionSnapshot{
		ID: ls.ID, Name: ls.Name, SpawnName: ls.SpawnName, Title: ls.title, Cwd: ls.cwd,
		ClaudeID: ls.claudeID,
		Created:  ls.Created, LastOutput: ls.lastOutput, Fullscreen: ls.fullscreen,
	}
}

// SetClaudeSession records the claude session id live in a shell — hook
// evidence (hooks.go), keyed like every hook by the spawn-time RF_SESSION.
func (m *Manager) SetClaudeSession(spawnName, claudeID string) {
	m.setClaudeSession(spawnName, claudeID, false)
}

// ClearClaudeSession retires a claude session id when its conversation ends
// (the SessionEnd hook): the shell is back to being a plain shell, so a
// restart respawns it plain.
func (m *Manager) ClearClaudeSession(spawnName, claudeID string) {
	m.setClaudeSession(spawnName, claudeID, true)
}

func (m *Manager) setClaudeSession(spawnName, claudeID string, clear bool) {
	if claudeID == "" {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, ls := range m.sessions {
		if ls.SpawnName != spawnName {
			continue
		}
		ls.mu.Lock()
		switch {
		case clear && ls.claudeID == claudeID:
			ls.claudeID = ""
		case !clear:
			ls.claudeID = claudeID
		}
		ls.mu.Unlock()
		m.persistLocked()
		return
	}
}

func (ls *liveSession) ForegroundCommand() string {
	pgid, err := unix.IoctlGetInt(int(ls.ptmx.Fd()), unix.TIOCGPGRP)
	if err != nil || pgid <= 0 {
		return ""
	}
	out, err := exec.Command("ps", "-o", "comm=", "-p", fmt.Sprint(pgid)).Output()
	if err != nil {
		return ""
	}
	return filepath.Base(strings.TrimSpace(string(out)))
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

func stripControl(s string) string {
	return strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return -1
		}
		return r
	}, s)
}

// ErrNoSession reports an operation against a session that isn't in the
// fleet.
var ErrNoSession = fmt.Errorf("no such session")

// Rename changes a session's display name.
func (m *Manager) Rename(spawnName, newName string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	var target *liveSession
	for _, ls := range m.sessions {
		if ls.SpawnName == spawnName {
			target = ls
		} else if ls.Name == newName || ls.SpawnName == newName {
			return ErrNameTaken
		}
	}
	if target == nil {
		return ErrNoSession
	}
	target.mu.Lock()
	target.Name = newName
	target.mu.Unlock()
	m.persistLocked()
	return nil
}

// FindByName returns the id of the session with the given name, "" when
// absent (names are unique — Spawn enforces it).
func (m *Manager) FindByName(name string) string {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, ls := range m.sessions {
		if ls.Name == name {
			return ls.ID
		}
	}
	return ""
}

// Close ends a session from the console (a tab's close button): hang up its
// terminal.
func (m *Manager) Close(id string) error {
	ls := m.Get(id)
	if ls == nil {
		return fmt.Errorf("no such session: %s", id)
	}
	if ls.cmd.Process != nil {
		_ = unix.Kill(-ls.cmd.Process.Pid, unix.SIGHUP)
	}
	_ = ls.ptmx.Close()
	return nil
}

// Get returns a live session by id, nil when absent.
func (m *Manager) Get(id string) *liveSession {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.sessions[id]
}

// List snapshots the current sessions.
func (m *Manager) List() []*liveSession {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]*liveSession, 0, len(m.sessions))
	for _, ls := range m.sessions {
		out = append(out, ls)
	}
	return out
}
