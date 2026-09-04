package cmd

import (
	"fmt"
	"os"

	"github.com/joho/godotenv"
	"github.com/retrofilter/rf/core"
	"github.com/retrofilter/rf/eval"
	"github.com/retrofilter/rf/llm"
	"github.com/spf13/cobra"
)

var evalExpr string
var evalPrelude bool

var rootCmd = &cobra.Command{
	Use:   "rf [script.scm [args...]]",
	Short: "retrofilter - a programmable, persistent shell",
	Long:  `retrofilter - a programmable, persistent shell. Lines starting with '(' eval as Scheme, Ctrl+Space toggles between command and agent mode. With a file argument, evaluates the file and exits; further arguments reach the script via (command-line). With -e, evaluates one form against the shared database and exits.`,
	Args:  cobra.ArbitraryArgs,
	// --version prints Version() (cmd/version.go): tag, commit, platform.
	Version: Version(),
	Run: func(cmd *cobra.Command, args []string) {
		if evalExpr != "" {
			if len(args) != 0 {
				fmt.Fprintln(os.Stderr, "rf -e takes no positional arguments")
				os.Exit(1)
			}
			os.Exit(runEval(evalExpr, evalPrelude))
		}
		if evalPrelude {
			fmt.Fprintln(os.Stderr, "rf -i requires -e (the shell and scripts ignore it)")
			os.Exit(1)
		}
		if len(args) >= 1 {
			runScript(args[0])
			return
		}
		runShell(cmd, args)
	},
}

func runScript(path string) {
	src, err := os.ReadFile(path)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	forms, err := eval.ParseAll(string(src))
	if err != nil {
		fmt.Fprintf(os.Stderr, "%s: %v\n", path, err)
		os.Exit(1)
	}
	ev := llm.NewChatForScript(llm.Config{}).Eval
	if _, err := ev.EvalAll(forms, ev.GlobalEnv()); err != nil {
		fmt.Fprintf(os.Stderr, "%s: %v\n", path, err)
		os.Exit(1)
	}
}

func Execute() error {
	return rootCmd.Execute()
}

func init() {
	// ~/.rf.env is optional; ignore if absent
	if path, err := core.EnvPath(); err == nil {
		_ = godotenv.Load(path)
	}
	rootCmd.Flags().StringVarP(&evalExpr, "eval", "e", "",
		"evaluate one Scheme form against ~/.rf/main.db and print the result")
	rootCmd.Flags().BoolVarP(&evalPrelude, "interactive", "i", false,
		"with -e: evaluate the prelude ~/.rf.scm first, like an interactive shell")
}
