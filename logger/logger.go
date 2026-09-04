package logger

import (
	"bytes"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/rs/zerolog"
	"golang.org/x/term"
)

var zlog zerolog.Logger

const (
	colorReset   = "\x1b[0m"
	colorCyan    = "\x1b[36m"
	colorGreen   = "\x1b[32m"
	colorMagenta = "\x1b[35m"
	colorRed     = "\x1b[31m"
	colorGray    = "\x1b[90m"
)

var noColor = !term.IsTerminal(int(os.Stderr.Fd()))

func paint(color, s string) string {
	if noColor {
		return s
	}
	return color + s + colorReset
}

var levelColors = map[string]string{
	"trace": colorGray,
	"debug": colorGray,
	"info":  colorGreen,
	"warn":  colorMagenta,
	"error": colorRed,
	"fatal": colorRed,
	"panic": colorRed,
}

func sexpWriter() zerolog.ConsoleWriter {
	return zerolog.ConsoleWriter{
		Out: os.Stderr,
		PartsOrder: []string{
			zerolog.LevelFieldName,
			zerolog.TimestampFieldName,
			zerolog.CallerFieldName,
			zerolog.MessageFieldName,
		},
		// The level heads the form, so it carries the opening paren.
		FormatLevel: func(i interface{}) string {
			s, ok := i.(string)
			if !ok {
				s = "log"
			}
			color := levelColors[s]
			if color == "" {
				color = colorGray
			}
			return paint(colorGray, "(") + paint(color, s)
		},
		FormatTimestamp: func(i interface{}) string {
			ts := fmt.Sprintf("%v", i)
			if t, err := time.Parse(time.RFC3339, ts); err == nil {
				ts = t.Format("15:04:05")
			}
			return paint(colorGray, ts)
		},
		FormatCaller: func(i interface{}) string {
			c, _ := i.(string)
			if idx := strings.LastIndexByte(c, '/'); idx >= 0 {
				c = c[idx+1:]
			}
			return paint(colorCyan, c)
		},
		FormatMessage: func(i interface{}) string {
			if i == nil || i == "" {
				return ""
			}
			return fmt.Sprintf("%q", i)
		},
		FormatFieldName: func(i interface{}) string {
			return paint(colorMagenta, fmt.Sprintf(":%v", i)) + " "
		},
		FormatExtra: func(_ map[string]interface{}, buf *bytes.Buffer) error {
			b := bytes.TrimRight(buf.Bytes(), " ")
			buf.Truncate(len(b))
			buf.WriteString(paint(colorGray, ")"))
			return nil
		},
	}
}

func init() {
	level := zerolog.InfoLevel
	if debug := os.Getenv("DEBUG"); debug == "true" || debug == "1" {
		level = zerolog.DebugLevel
	}
	zlog = zerolog.New(sexpWriter()).Level(level).With().Timestamp().Caller().Logger()
}

func Trace() *zerolog.Event {
	return zlog.Trace()
}

func Debug() *zerolog.Event {
	return zlog.Debug()
}

func Info() *zerolog.Event {
	return zlog.Info()
}

func Fatal() *zerolog.Event {
	return zlog.Fatal()
}

func Warn() *zerolog.Event {
	return zlog.Warn()
}

func Error() *zerolog.Event {
	return zlog.Error()
}
