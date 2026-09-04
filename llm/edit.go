package llm

import (
	"fmt"
	"sort"
	"strings"
)

type editSpec struct {
	OldText string `json:"oldText"`
	NewText string `json:"newText"`
}

type span struct {
	start, end int
	newText    string
}

func applyEdits(content string, edits []editSpec) (string, error) {
	if len(edits) == 0 {
		return "", fmt.Errorf("edits must contain at least one replacement")
	}

	bom := ""
	if strings.HasPrefix(content, "\uFEFF") {
		bom = "\uFEFF"
		content = strings.TrimPrefix(content, "\uFEFF")
	}
	crlf := strings.Contains(content, "\r\n")
	text := strings.ReplaceAll(content, "\r\n", "\n")

	normalized := make([]editSpec, len(edits))
	for i, e := range edits {
		if e.OldText == "" {
			return "", fmt.Errorf("edit %d: oldText must be non-empty", i+1)
		}
		normalized[i] = editSpec{
			OldText: strings.ReplaceAll(e.OldText, "\r\n", "\n"),
			NewText: strings.ReplaceAll(e.NewText, "\r\n", "\n"),
		}
	}

	spans, notFound, err := matchEdits(text, normalized)
	if err != nil {
		return "", err
	}
	if notFound >= 0 {
		ftext := stripTrailingSpace(text)
		fedits := make([]editSpec, len(normalized))
		for i, e := range normalized {
			fedits[i] = editSpec{OldText: stripTrailingSpace(e.OldText), NewText: e.NewText}
		}
		fspans, fNotFound, ferr := matchEdits(ftext, fedits)
		if ferr != nil {
			return "", ferr
		}
		if fNotFound >= 0 {
			return "", fmt.Errorf("edit %d: oldText not found in file: %s", fNotFound+1, firstLine(normalized[fNotFound].OldText))
		}
		text, spans = ftext, fspans
	}

	sort.Slice(spans, func(i, j int) bool { return spans[i].start < spans[j].start })
	for i := 1; i < len(spans); i++ {
		if spans[i].start < spans[i-1].end {
			return "", fmt.Errorf("edits overlap; merge changes to the same region into one edit")
		}
	}

	var sb strings.Builder
	prev := 0
	for _, s := range spans {
		sb.WriteString(text[prev:s.start])
		sb.WriteString(s.newText)
		prev = s.end
	}
	sb.WriteString(text[prev:])

	out := sb.String()
	if crlf {
		out = strings.ReplaceAll(out, "\n", "\r\n")
	}
	return bom + out, nil
}

func matchEdits(text string, edits []editSpec) (spans []span, notFound int, err error) {
	for i, e := range edits {
		switch n := strings.Count(text, e.OldText); n {
		case 0:
			return nil, i, nil
		case 1:
			start := strings.Index(text, e.OldText)
			spans = append(spans, span{start: start, end: start + len(e.OldText), newText: e.NewText})
		default:
			return nil, -1, fmt.Errorf("edit %d: oldText matches %d times, must be unique: %s", i+1, n, firstLine(e.OldText))
		}
	}
	return spans, -1, nil
}

func stripTrailingSpace(s string) string {
	lines := strings.Split(s, "\n")
	for i, line := range lines {
		lines[i] = strings.TrimRight(line, " \t")
	}
	return strings.Join(lines, "\n")
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i] + "…"
	}
	return s
}
