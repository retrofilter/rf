package cmd

import (
	"context"
	"errors"
	"fmt"
	"os"

	"github.com/mattn/go-runewidth"
	"github.com/retrofilter/rf/eval"
	"github.com/retrofilter/rf/llm"
	"github.com/retrofilter/rf/models"
	"golang.org/x/term"
)

func registerAgentBuiltin(st *shellState) {
	eval.Register("agent", "run a sub-agent chat turn — the rest of the line is the instruction",
		eval.CommandMeta{Command: true, MinArgs: 1, MaxArgs: -1, Stage: true, Instruction: true, Usage: "instruction ..."})
	st.chat.Env.Set("agent", eval.BuiltinFunc(func(args []eval.Value, env *eval.Environment) (eval.Value, error) {
		if len(args) == 0 {
			return nil, errors.New("agent requires an instruction: (agent \"instruction\" [context ...])")
		}
		instruction, err := llm.JoinPromptArgs("agent", args)
		if err != nil {
			return nil, err
		}
		text, err := runAgentTurn(st, instruction)
		if err != nil {
			return nil, fmt.Errorf("agent: %w", err)
		}
		st.lastAgentReply = text
		return eval.String(text), nil
	}))
}

func runAgentTurn(st *shellState, instruction string) (string, error) {
	rec := &sessionRecorder{db: st.db, session: newSessionID(), parent: st.rec.session}

	if parentCtx := st.ctrl.activeContext(); parentCtx != nil {
		ctx, cancel := context.WithCancel(parentCtx)
		defer cancel()
		return agentMessage(st, ctx, rec, instruction)
	}

	fd := int(os.Stdin.Fd())
	if oldState, err := term.MakeRaw(fd); err == nil {
		st.printer.raw = true
		defer func() {
			st.printer.raw = false
			_ = term.Restore(fd, oldState)
		}()
	}
	ctx, cancel := context.WithCancel(context.Background())
	st.ctrl.begin(ctx, cancel)
	done := make(chan struct{})
	go watchTurnKeys(fd, done, st.chat, st.ctrl)
	defer func() {
		close(done)
		st.ctrl.end()
		cancel()
	}()

	text, err := agentMessage(st, ctx, rec, instruction)
	for _, s := range st.chat.TakeSteers() {
		st.printer.print(colorGray + "» " + s + " (arrived after the agent finished — ignored)" + colorReset + "\n")
	}
	return text, err
}

func agentMessage(st *shellState, ctx context.Context, rec *sessionRecorder, instruction string) (string, error) {
	p := st.printer
	p.beginBlock()
	p.print(bulletText + " Agent " + colorGray + runewidth.Truncate(firstLine(instruction), 80, "…") + colorReset + "\n")
	p.showStatus()

	replies, err := st.chat.Message(ctx, instruction, nil, "")
	p.hideStatus()
	// Canceled mid-stream: newline-terminate the partial raw text.
	if p.stream.Len() > 0 {
		p.print("\n")
		p.stream.Reset()
	}

	rec.record(&models.Message{
		Role:    models.MessageRoleUser,
		Type:    models.MessageTypeText,
		Content: instruction,
	})
	rec.record(replies...)

	if err != nil {
		if errors.Is(err, context.Canceled) {
			return "", errors.New("interrupted")
		}
		return "", err
	}
	for i := len(replies) - 1; i >= 0; i-- {
		if replies[i].Type == models.MessageTypeText && replies[i].Role == models.MessageRoleAssistant {
			return replies[i].Content, nil
		}
	}
	return "", errors.New("the sub-agent finished without a text reply")
}

func firstLine(s string) string {
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' {
			return s[:i]
		}
	}
	return s
}
