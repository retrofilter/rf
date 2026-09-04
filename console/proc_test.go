package console

import (
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestScanParsesMetadataAcrossChunks(t *testing.T) {
	ls := &liveSession{subs: map[chan []byte]*subscriber{}}

	// Title (OSC 2, BEL-terminated) and cwd (OSC 7, percent-encoded).
	ls.ingest([]byte("\x1b]2;proj@main\a\x1b]7;file://box/Users/dev/my%20dir\a"))
	require.Equal(t, "proj@main", ls.title)
	require.Equal(t, "/Users/dev/my dir", ls.cwd)

	// An escape split across two reads still parses via the carry.
	ls.ingest([]byte("\x1b]2;sp"))
	ls.ingest([]byte("lit\a"))
	require.Equal(t, "split", ls.title)

	// Alternate-screen toggles; the last one wins.
	ls.ingest([]byte("\x1b[?1049h"))
	require.True(t, ls.fullscreen)
	ls.ingest([]byte("stuff\x1b[?1049h more \x1b[?1049l"))
	require.False(t, ls.fullscreen)

	// OSC 0 also sets the title (claude uses it); ST termination accepted.
	ls.ingest([]byte("\x1b]0;claude session\x1b\\"))
	require.Equal(t, "claude session", ls.title)
}

func TestInitialCommandWaitsForPrompt(t *testing.T) {
	m := NewManager("cat", 0)
	id, err := m.Spawn("resume", "", "claude --resume 'abcd-1234'")
	require.NoError(t, err)
	ls := m.Get(id)
	require.NotNil(t, ls)

	// Plain output (cat echoing boot noise) must not release the line.
	require.NoError(t, ls.Write([]byte("boot noise\n")))
	time.Sleep(300 * time.Millisecond)
	replay, _, cancel := ls.Subscribe()
	cancel()
	require.NotContains(t, string(replay), "claude --resume", "typed before the prompt painted")

	require.NoError(t, ls.Write([]byte("\x1b]2;rf\a\n")))
	require.Eventually(t, func() bool {
		replay, _, cancel := ls.Subscribe()
		cancel()
		return strings.Contains(string(replay), "\x1b[200~claude --resume 'abcd-1234'\x1b[201~")
	}, 3*time.Second, 25*time.Millisecond)
}

func TestReplayScrubsTerminalQueries(t *testing.T) {
	ls := &liveSession{subs: map[chan []byte]*subscriber{}}

	_, ch, cancel := ls.Subscribe()
	defer cancel()

	raw := "before\x1b[6n\x1b[0c\x1b[>0c\x1b[18t\x1b[?2026$p\x1bP+q544e\x1b\\\x1b]11;?\a\x1b]10;?\x1b\\after"
	ls.ingest([]byte(raw))
	require.Equal(t, raw, string(<-ch), "live stream passes queries through")

	replay, _, cancel2 := ls.Subscribe()
	cancel2()
	require.Equal(t, "beforeafter", string(replay))

	// Display-affecting sequences survive the scrub.
	ls2 := &liveSession{subs: map[chan []byte]*subscriber{}}
	styled := "\x1b[31mred\x1b[0m\x1b[?1049h\x1b[2J\x1b[H\x1b]2;title\a"
	ls2.ingest([]byte(styled))
	replay, _, cancel3 := ls2.Subscribe()
	cancel3()
	require.Equal(t, styled, string(replay))
}

func TestDetachedCursorProbeForwardedUntilAnswered(t *testing.T) {
	ls := &liveSession{subs: map[chan []byte]*subscriber{}}

	ls.ingest([]byte("prompt> \x1b[6n"))
	require.Equal(t, "\x1b[6n", string(ls.pending))

	replay, _, cancel := ls.Subscribe()
	cancel() // this tab dies before answering
	require.Equal(t, "prompt> \x1b[6n", string(replay))

	replay2, ch2, cancel2 := ls.Subscribe()
	defer cancel2()
	require.Equal(t, "prompt> \x1b[6n", string(replay2))

	_ = ls.WriteInput([]byte("\x1b[9;9R"), ch2)
	require.Empty(t, ls.pending)
	replay3, _, cancel3 := ls.Subscribe()
	cancel3()
	require.Equal(t, "prompt> ", string(replay3))

	// With a tab attached, probes pass through live and are not held.
	ls.ingest([]byte("\x1b[6n"))
	require.Empty(t, ls.pending)
}

func TestUnattendedInitialCommandAnswersCursorProbe(t *testing.T) {
	ls := &liveSession{subs: map[chan []byte]*subscriber{}, unattended: true}

	// Detached with a command queued: the probe is answered, not parked.
	answers := ls.scan([]byte("prompt> \x1b[6n"))
	require.Equal(t, []string{unknownCursorPos}, answers)
	require.Empty(t, ls.pending)
	require.Empty(t, ls.scan([]byte("more output")))

	// The first attach ends the mode; a later detached probe is held as before.
	_, _, cancel := ls.Subscribe()
	cancel()
	require.False(t, ls.unattended)
	require.Empty(t, ls.scan([]byte("\x1b[6n")))
	require.Equal(t, "\x1b[6n", string(ls.pending))
}

func TestUnattendedInitialCommandRunsWithoutAttach(t *testing.T) {
	// Readline stand-in: prompt, query the cursor, act on input only once answered.
	script := `stty -icanon -echo min 1 time 0
printf '\033]2;rf\a\033[6n'
buf=
while :; do buf="$buf$(dd bs=1 count=1 2>/dev/null)"
case "$buf" in *R*201~*|*201~*R*) break;; esac
done
printf 'seen:%s\n' "$buf"`
	m := NewManager(script, 0)
	id, err := m.Spawn("night", "", "echo hi")
	require.NoError(t, err)
	ls := m.Get(id)
	require.NotNil(t, ls)

	require.Eventually(t, func() bool {
		ls.mu.Lock()
		defer ls.mu.Unlock()
		return strings.Contains(string(ls.ring), "seen:")
	}, 5*time.Second, 25*time.Millisecond)
	ls.mu.Lock()
	ring := string(ls.ring)
	ls.mu.Unlock()
	require.Contains(t, ring, unknownCursorPos)
	require.Contains(t, ring, "\x1b[200~echo hi\x1b[201~")
	require.Empty(t, ls.pending)
}

func TestDetachedColorQueriesAnsweredOneLight(t *testing.T) {
	ls := &liveSession{subs: map[chan []byte]*subscriber{}}

	answers := ls.scan([]byte("\x1b]11;?\a\x1b]10;?\x1b\\"))
	require.Equal(t, []string{
		"\x1b]11;rgb:fafa/fafa/fafa\a",
		"\x1b]10;rgb:3838/3a3a/4242\x1b\\",
	}, answers)

	require.Empty(t, ls.scan([]byte("more output")))

	// Attached, the tab's xterm answers — the server stays silent.
	_, _, cancel := ls.Subscribe()
	defer cancel()
	require.Empty(t, ls.scan([]byte("\x1b]11;?\a")))
}

func TestManagerLifecycleAndReplay(t *testing.T) {
	m := NewManager("cat", 0)
	id, err := m.Spawn("probe", "", "")
	require.NoError(t, err)
	require.NotEmpty(t, id)

	// Names are unique while the session lives.
	_, err = m.Spawn("probe", "", "")
	require.ErrorIs(t, err, ErrNameTaken)

	ls := m.Get(id)
	require.NotNil(t, ls)

	// Type into the PTY; cat echoes it back into the ring.
	require.NoError(t, ls.Write([]byte("marco\r")))
	require.Eventually(t, func() bool {
		replay, _, cancel := ls.Subscribe()
		cancel()
		return strings.Contains(string(replay), "marco")
	}, 5*time.Second, 50*time.Millisecond, "echo lands in the replay ring")

	// A live subscriber receives new output as it happens.
	_, ch, cancel := ls.Subscribe()
	defer cancel()
	require.NoError(t, ls.Write([]byte("polo\r")))
	deadline := time.After(5 * time.Second)
	var live strings.Builder
	for !strings.Contains(live.String(), "polo") {
		select {
		case chunk, ok := <-ch:
			require.True(t, ok, "channel closed before output arrived")
			live.Write(chunk)
		case <-deadline:
			t.Fatalf("live subscriber never saw output; got %q", live.String())
		}
	}

	// EOF ends cat, and the session leaves the fleet.
	require.NoError(t, ls.Write([]byte{0x04}))
	require.Eventually(t, func() bool {
		return m.Get(id) == nil
	}, 5*time.Second, 50*time.Millisecond, "session removed after exit")

	// The name is free again.
	_, err = m.Spawn("probe", "", "")
	require.NoError(t, err)
}

func TestSessionExitNotifiesBeforeSubscribersClose(t *testing.T) {
	m := NewManager("cat", 0)
	var exited atomic.Bool
	m.onExit = func() { exited.Store(true) }

	id, err := m.Spawn("bye", "", "")
	require.NoError(t, err)
	ls := m.Get(id)
	require.NotNil(t, ls)

	_, ch, cancel := ls.Subscribe()
	defer cancel()

	require.NoError(t, ls.Write([]byte{0x04}))
	for range ch {
	}
	require.True(t, exited.Load(), "onExit must fire before subscriber channels close")
	require.Nil(t, m.Get(id))
}

func TestStripControl(t *testing.T) {
	in := "fix bug\rrm -rf ~\n\x1b]0;evil\x07\ttail"
	require.Equal(t, "fix bugrm -rf ~]0;eviltail", stripControl(in))
	require.Equal(t, "plain text stays", stripControl("plain text stays"))
}

func TestSubscribeAfterExitGetsClosedChannel(t *testing.T) {
	m := NewManager("cat", 0)
	id, err := m.Spawn("late", "", "")
	require.NoError(t, err)
	ls := m.Get(id)
	require.NotNil(t, ls)

	// End the session and wait for the reader's cleanup to finish.
	require.NoError(t, ls.Write([]byte{0x04}))
	require.Eventually(t, func() bool {
		ls.mu.Lock()
		defer ls.mu.Unlock()
		return ls.closed
	}, 5*time.Second, 10*time.Millisecond, "reader cleanup must mark the session closed")

	// The stale handle a concurrent attach still holds.
	replay, ch, cancel := ls.Subscribe()
	defer cancel()
	require.Empty(t, replay)
	select {
	case _, ok := <-ch:
		require.False(t, ok, "channel must be closed, not delivering")
	case <-time.After(time.Second):
		t.Fatal("subscribe-after-exit channel never closed")
	}
}

func TestSlowSubscriberCoalescesInsteadOfDetaching(t *testing.T) {
	ls := &liveSession{subs: map[chan []byte]*subscriber{}}
	_, ch, cancel := ls.Subscribe()
	defer cancel()

	var want strings.Builder
	line := strings.Repeat("x", 4095) + "\n"
	for i := 0; i < 1024; i++ { // 4MB, 1024 chunks — 4× the old drop depth
		ls.ingest([]byte(line))
		want.WriteString(line)
	}
	ls.mu.Lock()
	_, still := ls.subs[ch]
	ls.mu.Unlock()
	require.True(t, still, "a slow subscriber must not be dropped")

	var got strings.Builder
	deadline := time.After(10 * time.Second)
	for got.Len() < want.Len() {
		select {
		case chunk, ok := <-ch:
			require.True(t, ok, "channel closed before the backlog drained")
			require.LessOrEqual(t, len(chunk), subChunk, "frames stay bounded")
			got.Write(chunk)
		case <-deadline:
			t.Fatalf("drained %d of %d bytes", got.Len(), want.Len())
		}
	}
	require.Equal(t, want.String(), got.String())

	for i := 0; i < 3*subMax/len(line); i++ {
		ls.ingest([]byte(line))
	}
	ls.ingest([]byte("prompt> "))
	ls.mu.Lock()
	backlog := len(ls.subs[ch].backlog)
	ls.mu.Unlock()
	require.LessOrEqual(t, backlog, subMax)
	var tail strings.Builder
	deadline = time.After(10 * time.Second)
	for !strings.HasSuffix(tail.String(), "prompt> ") {
		select {
		case chunk, ok := <-ch:
			require.True(t, ok)
			tail.Write(chunk)
		case <-deadline:
			t.Fatal("tail never arrived after the cut")
		}
	}
	require.True(t, strings.HasPrefix(tail.String(), "xxxx"), "cut resumes at a line start, got %q", tail.String()[:8])
	require.LessOrEqual(t, tail.Len(), subMax)

	// Cancelling with a backlog outstanding retires the pump promptly.
	ls.ingest([]byte(line))
	cancel()
	select {
	case _, ok := <-ch:
		if ok {
			for range ch {
			}
		}
	case <-time.After(5 * time.Second):
		t.Fatal("channel not closed after cancel")
	}
}
