package cmd

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/mattn/go-runewidth"
	"github.com/retrofilter/rf/llm"
	"github.com/retrofilter/rf/models"
)

const resumeListLimit = 20

const compactTimeout = 2 * time.Minute

func runResume(st *shellState) error {
	sessions, err := models.ListSessions(st.db, resumeListLimit)
	if err != nil {
		return fmt.Errorf("resume: %w", err)
	}

	var rows []pickRow
	var candidates []models.Session
	for _, s := range sessions {
		if s.ID == st.rec.session || s.Entries == 0 {
			continue
		}
		candidates = append(candidates, s)
		rows = append(rows, pickRow{name: sessionRowName(s), note: sessionRowNote(s)})
	}
	if len(candidates) == 0 {
		fmt.Println("no past sessions to resume")
		return nil
	}

	i, ok := pick("resume session", rows, 0)
	if !ok {
		fmt.Println("cancelled")
		return nil
	}
	chosen := candidates[i]

	transcript, err := models.SessionTranscript(st.db, chosen.ID)
	if err != nil {
		return fmt.Errorf("resume: %w", err)
	}

	st.rec.session = chosen.ID
	st.rec.ensured = true // the session row exists; keep its original cwd/project
	st.history = transcript
	st.mode = modeAgent

	fmt.Printf("resumed %s%s%s — %d messages, last active %s\n",
		colorCyan, sessionRowName(chosen), colorReset, len(transcript), relativeAge(chosen.UpdatedAt))
	replayTail(transcript)
	return nil
}

func sessionRowName(s models.Session) string {
	title := s.Title
	if title == "" {
		title = "(untitled)"
	}
	return runewidth.Truncate(title, 48, "…")
}

func sessionRowNote(s models.Session) string {
	parts := []string{}
	if s.Parent != "" {
		parts = append(parts, "agent")
	}
	if s.Project != "" {
		parts = append(parts, s.Project)
	} else if s.Cwd != "" {
		parts = append(parts, shortenHome(s.Cwd))
	}
	parts = append(parts, relativeAge(s.UpdatedAt), fmt.Sprintf("%d msgs", s.Entries))
	return strings.Join(parts, " · ")
}

func shortenHome(path string) string {
	if home, err := os.UserHomeDir(); err == nil && strings.HasPrefix(path, home) {
		return "~" + strings.TrimPrefix(path, home)
	}
	return path
}

func relativeAge(t time.Time) string {
	d := time.Since(t)
	switch {
	case d < 2*time.Minute:
		return "just now"
	case d < 2*time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	}
}

func replayTail(transcript []*models.Message) {
	start := 0
	for i, m := range transcript {
		if m.Role == models.MessageRoleUser && m.Type == models.MessageTypeText {
			start = i
		}
	}
	start = max(start, len(transcript)-6)

	fmt.Println()
	for _, m := range transcript[start:] {
		switch {
		case m.Type == models.MessageTypeTool:
			for _, line := range renderToolCall(m) {
				fmt.Println(line)
			}
			if m.ErrorMessage != "" {
				fmt.Println(resultBlock(colorRed+"Error: "+colorReset+clipLines(m.ErrorMessage, 3)) + "\n")
			} else if m.Result != "" {
				fmt.Println(resultBlock(clipLines(m.Result, 3)) + "\n")
			}
		case m.Role == models.MessageRoleUser:
			fmt.Println(colorGray + "» " + m.Content + colorReset)
		default:
			fmt.Println(bulletize(bulletText, renderMarkdown(m.Content)) + "\n")
		}
	}
}

func runCompact(st *shellState) error {
	if !st.rec.ensured {
		return fmt.Errorf("compact: nothing to compact — no chat turns in this session yet")
	}

	fmt.Println(colorGray + "compacting.." + colorReset)
	result, transcript, err := compactSession(st.chat, st.rec)
	if err != nil {
		return err
	}
	st.history = transcript
	fmt.Printf("compacted: ~%s tokens summarized, %d messages kept\n",
		llm.FormatTokens(result.TokensBefore), len(transcript)-1)
	fmt.Println(bulletize(bulletText, renderMarkdown(result.Summary)))
	return nil
}

func runClear(st *shellState) error {
	fmt.Print("\x1b[H\x1b[2J\x1b[3J")
	st.echoSuppressed = true
	st.history = nil
	st.rec.session = newSessionID()
	st.rec.ensured = false
	st.hist.session = st.rec.session
	st.chat.ResetContext()
	return nil
}

func compactSession(chatInst *llm.Chat, rec *sessionRecorder) (*models.Compaction, []*models.Message, error) {
	ctx, cancel := context.WithTimeout(context.Background(), compactTimeout)
	defer cancel()

	result, err := chatInst.Compact(ctx, rec.db, rec.session, 0)
	if err != nil {
		return nil, nil, err
	}
	transcript, err := models.SessionTranscript(rec.db, rec.session)
	if err != nil {
		return nil, nil, fmt.Errorf("compact: compacted, but failed to rebuild the transcript: %w", err)
	}
	return result, transcript, nil
}

func maybeAutoCompact(st *shellState) {
	if !st.rec.ensured || !st.chat.NeedsCompaction() {
		return
	}
	_, contextTokens := st.chat.Usage()
	fmt.Printf(colorGray+"context %s of %s — compacting.."+colorReset+"\n",
		llm.FormatTokens(contextTokens), llm.FormatTokens(llm.ContextWindow(st.chat.Config().Model)))

	result, transcript, err := compactSession(st.chat, st.rec)
	if err != nil {
		fmt.Println(colorGray + "auto-compact failed: " + err.Error() + colorReset)
		return
	}
	st.history = transcript
	fmt.Printf("compacted: ~%s tokens summarized, %d messages kept\n",
		llm.FormatTokens(result.TokensBefore), len(transcript)-1)
}
