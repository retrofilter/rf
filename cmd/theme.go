package cmd

import (
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"golang.org/x/sys/unix"
	"golang.org/x/term"
)

const detectTimeout = 200 * time.Millisecond

var osc11ReplyRe = regexp.MustCompile("\x1b\\]11;([^\x07\x1b]*)(?:\x07|\x1b\\\\)")

func detectLightTerminal() bool {
	fd := int(os.Stdin.Fd())
	if !term.IsTerminal(fd) || !term.IsTerminal(int(os.Stdout.Fd())) {
		return false
	}
	old, err := term.MakeRaw(fd)
	if err != nil {
		return false
	}
	defer term.Restore(fd, old)
	if _, err := os.Stdout.WriteString("\x1b]11;?\a"); err != nil {
		return false
	}
	deadline := time.Now().Add(detectTimeout)
	var buf []byte
	chunk := make([]byte, 64)
	for {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return false
		}
		fds := []unix.PollFd{{Fd: int32(fd), Events: unix.POLLIN}}
		n, err := unix.Poll(fds, int(remaining.Milliseconds())+1)
		if err == unix.EINTR {
			continue
		}
		if err != nil || n == 0 {
			return false
		}
		nr, err := os.Stdin.Read(chunk)
		if nr > 0 {
			buf = append(buf, chunk[:nr]...)
			if m := osc11ReplyRe.FindSubmatch(buf); m != nil {
				return colorIsLight(string(m[1]))
			}
		}
		if err != nil {
			return false
		}
	}
}

func colorIsLight(spec string) bool {
	body, ok := strings.CutPrefix(spec, "rgb:")
	if !ok {
		body, ok = strings.CutPrefix(spec, "rgba:")
	}
	if !ok {
		return false
	}
	parts := strings.Split(body, "/")
	if len(parts) < 3 {
		return false
	}
	var ch [3]float64
	for i := range ch {
		p := parts[i]
		if len(p) == 0 || len(p) > 4 {
			return false
		}
		v, err := strconv.ParseUint(p, 16, 32)
		if err != nil {
			return false
		}
		max := float64(uint64(1)<<(4*len(p))) - 1
		ch[i] = float64(v) / max
	}
	// ITU-R BT.709 relative luminance — plenty to split light from dark.
	return 0.2126*ch[0]+0.7152*ch[1]+0.0722*ch[2] > 0.5
}
