package cmd

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestColorIsLight(t *testing.T) {
	cases := []struct {
		spec  string
		light bool
	}{
		{"rgb:fafa/fafa/fafa", true},  // One Light background
		{"rgb:2828/2c2c/3434", false}, // One Dark background
		{"rgb:ffff/ffff/ffff", true},
		{"rgb:0000/0000/0000", false},
		{"rgb:ff/ff/ff", true},             // 2-digit channels
		{"rgb:f/f/f", true},                // 1-digit channels
		{"rgba:ffff/ffff/ffff/0000", true}, // alpha ignored
		{"rgb:1e1e/1e1e/1e1e", false},
		{"#FFFFFF", false}, // unsupported form keeps the dark default
		{"rgb:zz/zz/zz", false},
		{"rgb:ffff/ffff", false}, // missing channel
		{"", false},
	}
	for _, c := range cases {
		require.Equal(t, c.light, colorIsLight(c.spec), "spec %q", c.spec)
	}
}

func TestOSC11ReplyParsing(t *testing.T) {
	m := osc11ReplyRe.FindSubmatch([]byte("\x1b]11;rgb:fafa/fafa/fafa\x1b\\"))
	require.NotNil(t, m)
	require.True(t, colorIsLight(string(m[1])))

	m = osc11ReplyRe.FindSubmatch([]byte("junk\x1b]11;rgb:1e1e/1e1e/1e1e\ajunk"))
	require.NotNil(t, m)
	require.False(t, colorIsLight(string(m[1])))

	require.Nil(t, osc11ReplyRe.FindSubmatch([]byte("\x1b]11;rgb:fafa")))
}

func TestDetectLightTerminalNonTTY(t *testing.T) {
	require.False(t, detectLightTerminal())
}
