package llm

import (
	"fmt"
	"strings"

	"github.com/retrofilter/rf/eval"
)

// UsageTotals accumulates token counts and cost across a session.
type UsageTotals struct {
	InputTokens      int
	OutputTokens     int
	CacheReadTokens  int
	CacheWriteTokens int
	Cost             float64
}

var modelPricing = []struct {
	match   string
	in, out float64
}{
	{"claude-fable", 10, 50},
	{"claude-mythos", 10, 50},
	{"claude-opus-4-1", 15, 75},
	{"claude-opus-4-0", 15, 75},
	{"claude-3-opus", 15, 75},
	{"claude-opus", 5, 25},
	{"claude-sonnet", 3, 15},
	{"claude-3-5-haiku", 0.8, 4},
	{"claude-3-haiku", 0.25, 1.25},
	{"claude-haiku", 1, 5},
}

const (
	cacheWriteMultiplier = 1.25
	cacheReadMultiplier  = 0.1
)

var modelContextWindows = []struct {
	match  string
	window int
}{
	{"claude-fable", 1000000},
	{"claude-mythos", 1000000},
	{"claude-opus-5", 1000000},
	{"claude-opus-4-8", 1000000},
	{"claude-opus-4-7", 1000000},
	{"claude-opus-4-6", 1000000},
	{"claude-sonnet-5", 1000000},
	{"claude-sonnet-4-6", 1000000},
	{"claude", 200000},
}

const (
	// DefaultContextWindow is assumed for models the table doesn't know — every
	// Ollama model.
	DefaultContextWindow = 64000

	// CompactReserveTokens is the headroom the auto-trigger keeps free:
	// compaction fires when the context exceeds window − reserve (pi
	// reserves 16k too).
	CompactReserveTokens = 16384
)

// ContextWindow returns the context-window size in tokens for a model name.
func ContextWindow(model string) int {
	for _, w := range modelContextWindows {
		if strings.Contains(model, w.match) {
			return w.window
		}
	}
	return DefaultContextWindow
}

// NeedsCompaction reports whether the most recent chat-loop call's context
// size has crossed the model's window minus the reserve — the auto-compaction
// threshold. False until a turn has run.
func (c *Chat) NeedsCompaction() bool {
	_, contextTokens := c.Usage()
	return contextTokens > c.contextWindow()-CompactReserveTokens
}

func (c *Chat) contextWindow() int {
	cfg := c.resolveConfig()
	if cfg.Context > 0 {
		return cfg.Context
	}
	if w := c.ollamaContextWindow(cfg); w > 0 {
		return w
	}
	return ContextWindow(cfg.Model)
}

func costUSD(model string, u Usage) float64 {
	for _, p := range modelPricing {
		if strings.Contains(model, p.match) {
			in := float64(u.InputTokens) +
				cacheWriteMultiplier*float64(u.CacheCreationInputTokens) +
				cacheReadMultiplier*float64(u.CacheReadInputTokens)
			return (in*p.in + float64(u.OutputTokens)*p.out) / 1e6
		}
	}
	return 0
}

const estimatedCharsPerToken = 4

func estimateUsage(req *Request, resp *Response) Usage {
	inChars := len(req.System)
	for _, msg := range req.Messages {
		for _, block := range msg.Content {
			inChars += len(block.Text) + len(block.Content) + len(block.Input)
		}
	}
	outChars := 0
	for _, block := range resp.Content {
		outChars += len(block.Text) + len(block.Input)
	}
	return Usage{
		InputTokens:  inChars / estimatedCharsPerToken,
		OutputTokens: outChars / estimatedCharsPerToken,
	}
}

func (c *Chat) recordUsage(req *Request, resp *Response, turn bool) {
	u := resp.Usage
	if u.InputTokens == 0 && u.OutputTokens == 0 &&
		u.CacheCreationInputTokens == 0 && u.CacheReadInputTokens == 0 {
		u = estimateUsage(req, resp)
	}
	promptTokens := u.InputTokens + u.CacheCreationInputTokens + u.CacheReadInputTokens
	c.usageMu.Lock()
	defer c.usageMu.Unlock()
	c.usage.InputTokens += promptTokens
	c.usage.OutputTokens += u.OutputTokens
	c.usage.CacheReadTokens += u.CacheReadInputTokens
	c.usage.CacheWriteTokens += u.CacheCreationInputTokens
	c.usage.Cost += costUSD(req.Model, u)
	if turn {
		c.contextTokens = promptTokens + u.OutputTokens
	}
}

// ResetContext zeroes the recorded context size — the shell's clear builtin
// drops the transcript and starts a fresh session, so the prompt's context
// percentage and the compaction auto-trigger start from empty again.
func (c *Chat) ResetContext() {
	c.usageMu.Lock()
	defer c.usageMu.Unlock()
	c.contextTokens = 0
}

// Usage returns the session's accumulated totals and the context size (in
// tokens) of the most recent chat-loop call.
func (c *Chat) Usage() (UsageTotals, int) {
	c.usageMu.Lock()
	defer c.usageMu.Unlock()
	return c.usage, c.contextTokens
}

// UsageLine formats the session totals as a status line — "↑12k ↓3k $0.08" —
// or "" when nothing has been spent yet. Cost is omitted while zero (Ollama).
func (c *Chat) UsageLine() string {
	totals, _ := c.Usage()
	if totals.InputTokens == 0 && totals.OutputTokens == 0 {
		return ""
	}
	line := "↑" + FormatTokens(totals.InputTokens) + " ↓" + FormatTokens(totals.OutputTokens)
	if totals.Cost > 0 {
		line += " " + formatCost(totals.Cost)
	}
	return line
}

// FormatTokens renders a token count compactly: 812, 2.4k, 61k.
func FormatTokens(n int) string {
	if n < 1000 {
		return fmt.Sprintf("%d", n)
	}
	if n < 10000 {
		return strings.TrimSuffix(fmt.Sprintf("%.1f", float64(n)/1000), ".0") + "k"
	}
	return fmt.Sprintf("%dk", n/1000)
}

func formatCost(c float64) string {
	if c < 0.01 {
		return fmt.Sprintf("$%.4f", c)
	}
	return fmt.Sprintf("$%.2f", c)
}

func (c *Chat) usageDict() eval.Dictionary {
	totals, contextTokens := c.Usage()
	return eval.Dictionary{
		"input":          eval.Number(totals.InputTokens),
		"output":         eval.Number(totals.OutputTokens),
		"cache-read":     eval.Number(totals.CacheReadTokens),
		"cache-write":    eval.Number(totals.CacheWriteTokens),
		"cost":           eval.Number(totals.Cost),
		"context":        eval.Number(contextTokens),
		"context-window": eval.Number(c.contextWindow()),
	}
}

func (c *Chat) registerUsageBuiltin() {
	eval.Register("usage", "session token totals, cost, and context size", eval.CommandMeta{Command: true})
	c.Env.Set("usage", eval.BuiltinFunc(func(args []eval.Value, env *eval.Environment) (eval.Value, error) {
		if len(args) != 0 {
			return nil, fmt.Errorf("usage takes no arguments")
		}
		return c.usageDict(), nil
	}))
}
