package cmd

import (
	"fmt"
	"os"

	"github.com/retrofilter/rf/core"
	"github.com/retrofilter/rf/eval"
	"github.com/retrofilter/rf/llm"
	"golang.org/x/term"
)

func agentCaller() bool {
	return os.Getenv("CLAUDECODE") != ""
}

func loadEvalPrelude(chat *llm.Chat) {
	path, err := core.PreludePath()
	if err != nil {
		return
	}
	src, err := os.ReadFile(path)
	if err != nil {
		return
	}
	forms, err := eval.ParseAll(string(src))
	if err != nil {
		fmt.Fprintln(os.Stderr, "~/.rf.scm:", err)
		return
	}
	for _, form := range forms {
		if _, err := chat.Eval.Eval(form, chat.Env); err != nil {
			fmt.Fprintln(os.Stderr, "~/.rf.scm:", err)
			return
		}
	}
}

func runEval(src string, withPrelude bool) int {
	db, err := openMainDB()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	defer db.Close()
	gs := core.NewGraphStore(db)
	chat, err := llm.NewChat(gs)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	registerConsoleBuiltins(chat)
	if withPrelude {
		loadEvalPrelude(chat)
	}
	if agentCaller() {
		chat.Env.Set("agent-allow-commands", []eval.Value{})
		chat.Eval.SetCaller(eval.CallerAssistant)
		chat.Eval.SetApprover(func(action string) bool {
			fmt.Fprintf(os.Stderr, "rf -e: %s requires approval, which is not available to agents here — use your own file tools, or ask the user to run it in the rf shell\n", action)
			return false
		})
	} else if term.IsTerminal(int(os.Stdin.Fd())) {
		chat.Eval.SetConfirmer(confirmPrompt)
	}

	exprs, err := eval.ParseAll(src)
	if err != nil {
		fmt.Fprintln(os.Stderr, "parse error:", err)
		return 1
	}
	result, err := chat.Eval.EvalAll(exprs, chat.Env)
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 1
	}
	if s, isStream := result.(*eval.Stream); isStream {
		if _, err := drainStream(s); err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			return 1
		}
		return 0
	}
	switch v := result.(type) {
	case nil:
	case eval.String:
		// Raw like command mode: cat, pwd, ... — and agents parse this.
		fmt.Println(string(v))
	default:
		if term.IsTerminal(int(os.Stdout.Fd())) {
			if tbl, ok := renderTable(result); ok {
				fmt.Println(tbl)
				return 0
			}
		} else if tbl, ok := eval.FormatTable(result); ok {
			fmt.Println(tbl)
			return 0
		}
		fmt.Println(eval.PrintValue(result))
	}
	return 0
}
