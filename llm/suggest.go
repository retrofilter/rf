package llm

import (
	"context"
	"fmt"
	"runtime"
	"strings"
)

const suggestSystemPrompt = "You turn what the user typed into a single shell command line. " +
	"The shell understands ordinary Unix commands and pipelines. " +
	"Reply with only the command line itself — one line, no explanation, no markdown fences."

const suggestBufferCap = 2048

const suggestMaxTokens = 256

// Suggest proposes a shell command for the current input buffer ("suggest,
// don't run" — Ctrl+K in the shell).
func (c *Chat) Suggest(ctx context.Context, buffer, cwd string) (string, error) {
	buffer = strings.TrimSpace(buffer)
	if len(buffer) > suggestBufferCap {
		buffer = buffer[len(buffer)-suggestBufferCap:]
	}
	prompt := fmt.Sprintf("cwd: %s (%s)\n\n%s", cwd, runtime.GOOS, buffer)
	req := &Request{
		Model:     c.resolveConfig().Model,
		MaxTokens: suggestMaxTokens,
		System:    suggestSystemPrompt,
		Messages:  []Message{TextMessage("user", prompt)},
	}
	resp, err := c.messages(ctx, req, nil, false)
	if err != nil {
		return "", fmt.Errorf("suggest: %w", err)
	}
	c.recordUsage(req, resp, false)

	var text strings.Builder
	for _, block := range resp.Content {
		if block.Type == "text" {
			text.WriteString(block.Text)
		}
	}
	return firstCommandLine(text.String()), nil
}

func firstCommandLine(s string) string {
	for line := range strings.SplitSeq(s, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "```") {
			continue
		}
		return line
	}
	return ""
}
