package llm

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/retrofilter/rf/eval"
)

const llmSystemPrompt = "You are called as a function from inside a Scheme shell. " +
	"Reply with only the answer itself — no preamble, no commentary, no markdown fences."

const llmCallTimeout = 2 * time.Minute

// JoinPromptArgs assembles an instruction from Scheme call arguments: the
// first is the instruction, the rest are appended as context (strings
// verbatim, other values in Scheme form, streams materialized).
func JoinPromptArgs(name string, args []eval.Value) (string, error) {
	prompt, ok := args[0].(eval.String)
	if !ok {
		return "", fmt.Errorf("%s: prompt must be a string, got %s", name, eval.PrintValue(args[0]))
	}
	parts := []string{string(prompt)}
	for _, arg := range args[1:] {
		if s, ok, err := eval.AsString(arg); err != nil {
			return "", err
		} else if ok {
			parts = append(parts, s)
			continue
		}
		if lst, ok, err := eval.AsList(arg); err != nil {
			return "", err
		} else if ok {
			parts = append(parts, eval.PrintValue(lst))
			continue
		}
		parts = append(parts, eval.PrintValue(arg))
	}
	return strings.Join(parts, "\n\n"), nil
}

func (c *Chat) plainCompletion(system, prompt string) (string, error) {
	return c.completionWithModel("", system, prompt)
}

func (c *Chat) completionWithModel(model, system, prompt string) (string, error) {
	if model == "" {
		model = c.resolveConfig().Model
	}
	base, stop := eval.InterruptContext(context.Background())
	defer stop()
	ctx, cancel := context.WithTimeout(base, llmCallTimeout)
	defer cancel()

	req := &Request{
		Model:     model,
		MaxTokens: plainMaxTokens,
		System:    system,
		Messages:  []Message{TextMessage("user", prompt)},
	}
	resp, err := c.messages(ctx, req, nil, false)
	if err != nil {
		return "", err
	}
	c.recordUsage(req, resp, false)

	var text strings.Builder
	for _, block := range resp.Content {
		if block.Type == "text" {
			text.WriteString(block.Text)
		}
	}
	return strings.TrimSpace(text.String()), nil
}

func (c *Chat) registerLLMBuiltin() {
	eval.Register("llm", "one plain LLM completion — the piped value threads in as context",
		eval.CommandMeta{Command: true, Stage: true, Instruction: true, MinArgs: 1, MaxArgs: -1, Usage: "instruction ..."})
	c.Env.Set("llm", eval.BuiltinFunc(func(args []eval.Value, env *eval.Environment) (eval.Value, error) {
		if len(args) == 0 {
			return nil, fmt.Errorf("llm requires a prompt: (llm \"instruction\" [context ...])")
		}
		prompt, err := JoinPromptArgs("llm", args)
		if err != nil {
			return nil, err
		}
		reply, err := c.plainCompletion(llmSystemPrompt, prompt)
		if err != nil {
			return nil, fmt.Errorf("llm: %w", err)
		}
		return eval.String(reply), nil
	}))
}
