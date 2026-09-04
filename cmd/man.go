package cmd

import (
	"fmt"
	"os"
	"regexp"
	"strings"

	"github.com/mattn/go-runewidth"
	"golang.org/x/term"
)

const (
	manBodyIndent = "       "        // 7 — man's paragraph indent
	manTagIndent  = "              " // 14 — man's option-description indent
	manCodeIndent = "           "    // 11 — verbatim examples
)

type man struct {
	b     strings.Builder
	width int
	color bool
}

func newMan(color bool) *man {
	width := 80
	if w, _, err := term.GetSize(int(os.Stdout.Fd())); err == nil && w > 0 {
		width = min(w, 100)
	}
	return &man{width: width, color: color}
}

func (m *man) bold(s string) string {
	if !m.color {
		return s
	}
	return "\x1b[1m" + s + "\x1b[0m"
}

func (m *man) underline(s string) string {
	if !m.color {
		return s
	}
	return "\x1b[4m" + s + "\x1b[0m"
}

func (m *man) header(title string, section int, manual string) {
	ref := strings.ToUpper(title) + fmt.Sprintf("(%d)", section)
	gap := m.width - 2*runewidth.StringWidth(ref) - runewidth.StringWidth(manual)
	left := max(gap/2, 1)
	right := max(gap-left, 1)
	m.b.WriteString(ref + strings.Repeat(" ", left) + manual + strings.Repeat(" ", right) + ref + "\n")
}

func (m *man) footer(title string, section int) {
	ref := strings.ToUpper(title) + fmt.Sprintf("(%d)", section)
	pad := max(m.width-runewidth.StringWidth("rf")-runewidth.StringWidth(ref), 1)
	m.b.WriteString("\nrf" + strings.Repeat(" ", pad) + ref + "\n")
}

func (m *man) section(heading string) {
	m.b.WriteString("\n" + m.bold(strings.ToUpper(heading)) + "\n")
}

func (m *man) para(text string) {
	for _, line := range m.wrap(m.inline(text), manBodyIndent, manBodyIndent) {
		m.b.WriteString(line + "\n")
	}
}

func (m *man) tagged(tag, desc string) {
	m.b.WriteString(manBodyIndent + m.bold(tag) + "\n")
	for _, line := range m.wrap(m.inline(desc), manTagIndent, manTagIndent) {
		m.b.WriteString(line + "\n")
	}
}

func (m *man) entry(term, doc string) {
	text := m.bold(term) + " — " + m.inline(doc)
	for _, line := range m.wrap(text, manBodyIndent, manTagIndent) {
		m.b.WriteString(line + "\n")
	}
}

func (m *man) verbatim(text string) {
	for _, line := range strings.Split(strings.TrimRight(text, "\n"), "\n") {
		m.b.WriteString(manCodeIndent + line + "\n")
	}
}

func (m *man) blank() { m.b.WriteString("\n") }

func (m *man) markdown(md string) {
	lines := strings.Split(md, "\n")
	var para []string
	var fence []string
	inFence := false
	flush := func() {
		if len(para) == 0 {
			return
		}
		text := strings.Join(para, " ")
		para = nil
		if strings.HasPrefix(text, "- ") {
			body := strings.TrimPrefix(text, "- ")
			for i, line := range m.wrap(m.inline(body), manBodyIndent+"- ", manBodyIndent+"  ") {
				_ = i
				m.b.WriteString(line + "\n")
			}
			return
		}
		m.para(text)
	}
	first := true
	for _, raw := range lines {
		line := strings.TrimRight(raw, " ")
		switch {
		case strings.HasPrefix(line, "```"):
			if inFence {
				m.verbatim(strings.Join(fence, "\n"))
				fence = nil
			} else {
				flush()
				if !first {
					m.blank()
				}
			}
			inFence = !inFence
		case inFence:
			fence = append(fence, line)
		case strings.TrimSpace(line) == "":
			flush()
			if !first {
				// paragraph gap; collapsed runs handled by flush emptiness
			}
			if len(para) == 0 && m.lastRune() != 0 {
				m.blank()
			}
		case strings.HasPrefix(line, "- "):
			flush()
			para = append(para, line)
		default:
			para = append(para, strings.TrimSpace(line))
		}
		if strings.TrimSpace(line) != "" {
			first = false
		}
	}
	flush()
	m.trimTrailingBlanks()
}

func (m *man) lastRune() byte {
	s := m.b.String()
	trimmed := strings.TrimRight(s, "\n")
	if trimmed == s || trimmed == "" {
		return 0
	}
	if strings.HasSuffix(s, "\n\n") {
		return 0
	}
	return s[len(s)-1]
}

func (m *man) trimTrailingBlanks() {
	s := strings.TrimRight(m.b.String(), "\n")
	m.b.Reset()
	m.b.WriteString(s + "\n")
}

func (m *man) String() string { return m.b.String() }

var inlineCodeRe = regexp.MustCompile("`([^`]+)`")
var inlineStrongRe = regexp.MustCompile(`\*\*([^*]+)\*\*`)
var ansiSGRRe = regexp.MustCompile(`\x1b\[[0-9;]*m`)

func (m *man) inline(s string) string {
	s = inlineCodeRe.ReplaceAllStringFunc(s, func(match string) string {
		return m.bold(strings.Trim(match, "`"))
	})
	s = inlineStrongRe.ReplaceAllStringFunc(s, func(match string) string {
		return m.bold(strings.Trim(match, "*"))
	})
	return s
}

func visibleWidth(s string) int {
	return runewidth.StringWidth(ansiSGRRe.ReplaceAllString(s, ""))
}

func (m *man) wrap(text, first, cont string) []string {
	words := strings.Fields(text)
	if len(words) == 0 {
		return nil
	}
	var lines []string
	line := first
	lineEmpty := true
	for _, word := range words {
		if !lineEmpty && visibleWidth(line)+1+visibleWidth(word) > m.width {
			lines = append(lines, line)
			line = cont
			lineEmpty = true
		}
		if lineEmpty {
			line += word
			lineEmpty = false
		} else {
			line += " " + word
		}
	}
	return append(lines, line)
}
