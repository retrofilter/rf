package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/jmoiron/sqlx"
	"github.com/retrofilter/rf/models"
)

// DefaultKeepRecentTokens is how much recent conversation (estimated) a
// compaction tries to keep verbatim after the cut.
const DefaultKeepRecentTokens = 20000

const compactSystemPrompt = "You compact conversation history for an AI Scheme shell. " +
	"Reply with only the summary itself — no preamble, no commentary."

const compactInstruction = "Summarize the conversation below so it can stand in for the full " +
	"history when the conversation continues. Capture: the state of the task, decisions made, " +
	"key values, definitions and paths, and open questions. Use short markdown sections."

// Compact summarizes the session's log up to a cut point and appends the
// result as a compaction entry.
func (c *Chat) Compact(ctx context.Context, db *sqlx.DB, session string, keepTokens int) (*models.Compaction, error) {
	if keepTokens <= 0 {
		keepTokens = DefaultKeepRecentTokens
	}

	entries, err := models.SessionEntries(db, session)
	if err != nil {
		return nil, err
	}

	var prev *models.Compaction
	for i := len(entries) - 1; i >= 0; i-- {
		if entries[i].Kind == models.SessionKindCompaction {
			var parsed models.Compaction
			if err := json.Unmarshal([]byte(entries[i].Payload), &parsed); err != nil {
				return nil, fmt.Errorf("compact: bad compaction payload (entry %d): %w", entries[i].ID, err)
			}
			prev = &parsed
			break
		}
	}
	var live []models.SessionEntry
	for _, e := range entries {
		if e.Kind == models.SessionKindCompaction {
			continue
		}
		if prev != nil && e.ID < prev.FirstKeptEntryID {
			continue
		}
		live = append(live, e)
	}
	if len(live) == 0 {
		return nil, fmt.Errorf("compact: nothing to compact")
	}

	cut := chooseCut(live, keepTokens)
	if cut <= 0 {
		return nil, fmt.Errorf("compact: nothing to compact — only one turn so far")
	}

	prevSummary := ""
	if prev != nil {
		prevSummary = prev.Summary
	}
	summary, err := c.summarize(ctx, prevSummary, live[:cut])
	if err != nil {
		return nil, err
	}

	result := models.Compaction{
		Summary:          summary,
		FirstKeptEntryID: live[cut].ID,
		TokensBefore:     len(prevSummary)/estimatedCharsPerToken + suffixTokens(live, 0),
	}
	if _, err := models.AppendSessionCompaction(db, session, result); err != nil {
		return nil, err
	}
	return &result, nil
}

func chooseCut(live []models.SessionEntry, keepTokens int) int {
	cut := -1
	for i, e := range live {
		if e.Kind == models.SessionKindUser && suffixTokens(live, i) <= keepTokens {
			cut = i
			break
		}
	}
	if cut <= 0 {
		for i := len(live) - 1; i > 0; i-- {
			if live[i].Kind == models.SessionKindUser {
				return i
			}
		}
	}
	return cut
}

func suffixTokens(live []models.SessionEntry, from int) int {
	total := 0
	for _, e := range live[from:] {
		total += len(e.Payload)/estimatedCharsPerToken + 1
	}
	return total
}

func (c *Chat) summarize(ctx context.Context, prevSummary string, entries []models.SessionEntry) (string, error) {
	var sb strings.Builder
	sb.WriteString(compactInstruction)
	if prevSummary != "" {
		sb.WriteString("\n\nAn earlier summary already covers the conversation before these messages; " +
			"carry its still-relevant content forward:\n\n")
		sb.WriteString(prevSummary)
	}
	sb.WriteString("\n\nConversation to summarize:\n")
	for _, e := range entries {
		m, err := models.EntryMessage(e)
		if err != nil {
			return "", err
		}
		sb.WriteString("\n")
		if m.Type == models.MessageTypeTool {
			result := m.Result
			if m.ErrorMessage != "" {
				result = "error: " + m.ErrorMessage
			}
			fmt.Fprintf(&sb, "[tool] %s\n=> %s\n", m.Code, result)
		} else {
			fmt.Fprintf(&sb, "[%s] %s\n", m.Role, m.Content)
		}
	}

	req := &Request{
		Model:     c.resolveConfig().Model,
		MaxTokens: plainMaxTokens,
		System:    compactSystemPrompt,
		Messages:  []Message{TextMessage("user", sb.String())},
	}
	resp, err := c.messages(ctx, req, nil, false)
	if err != nil {
		return "", fmt.Errorf("compact: %w", err)
	}
	c.recordUsage(req, resp, false)

	var text strings.Builder
	for _, block := range resp.Content {
		if block.Type == "text" {
			text.WriteString(block.Text)
		}
	}
	summary := strings.TrimSpace(text.String())
	if summary == "" {
		return "", fmt.Errorf("compact: model returned an empty summary")
	}
	return summary, nil
}
