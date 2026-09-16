package cmd

import (
	"encoding/json"
	"fmt"
	"io"
	"os"

	"github.com/retrofilter/rf/console"
	"github.com/retrofilter/rf/core"
	"github.com/retrofilter/rf/eval"
	"github.com/spf13/cobra"
)

var (
	hookInstall bool
	hookFire    bool
)

var hookCmd = &cobra.Command{
	Use:   "hook",
	Short: "install or inspect rf's Claude Code hooks",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		cmd.SilenceUsage = true
		if hookFire {
			return fireHook(cmd.InOrStdin())
		}
		path, err := console.ClaudeSettingsPath()
		if err != nil {
			return err
		}
		if hookInstall {
			if _, err := console.InstallClaudeHooks(); err != nil {
				return err
			}
			skillPath, err := console.InstallClaudeTaskSkill()
			if err != nil {
				return err
			}
			fmt.Printf("claude code hooks installed in %s (console status + transcript capture + session-start tasks + usage statusline)\n", path)
			fmt.Printf("task skill installed in %s\n", skillPath)
			return nil
		}
		status, capture, usage, err := console.ClaudeHooksInstalled(path)
		if err != nil {
			return err
		}
		skill := console.ClaudeTaskSkillInstalled()
		state := func(ok bool) string {
			if ok {
				return "installed"
			}
			return "not installed"
		}
		fmt.Printf("console status hooks: %s\ntranscript capture:   %s\nusage statusline:     %s\ntask skill:           %s\n", state(status), state(capture), state(usage), state(skill))
		if !status || !capture || !usage || !skill {
			fmt.Println("install with: rf hook --install")
		}
		return nil
	},
}

func fireHook(stdin io.Reader) error {
	var payload struct {
		Event          string `json:"hook_event_name"`
		SessionID      string `json:"session_id"`
		TranscriptPath string `json:"transcript_path"`
		Cwd            string `json:"cwd"`
	}
	_ = json.NewDecoder(io.LimitReader(stdin, 1<<20)).Decode(&payload)
	if payload.Event != "" && os.Getenv("RF_SESSION") != "" {
		if c, err := console.NewFromEnv(); err == nil {
			_ = c.ReportHook(payload.Event, payload.SessionID)
		}
	}
	switch payload.Event {
	case "Stop":
		if payload.TranscriptPath != "" {
			return syncTranscript(payload.TranscriptPath)
		}
	case "SessionStart":
		sessionStartContext(os.Stdout, payload.Cwd)
	}
	return nil
}

func sessionStartContext(w io.Writer, cwd string) {
	if cwd != "" {
		_ = os.Chdir(cwd)
	}
	db, err := openMainDB()
	if err != nil {
		return
	}
	defer db.Close()
	ev := eval.NewEvaluatorWithEnvironment(db, core.NewGraphStore(db))
	exprs, err := eval.ParseAll("(tasks)")
	if err != nil {
		return
	}
	result, err := ev.EvalAll(exprs, ev.GlobalEnv())
	if err != nil {
		return
	}
	rows, _ := result.([]eval.Value)

	scope := "across all projects"
	if dir, err := os.Getwd(); err == nil {
		if name, _, ok := eval.FindProject(dir); ok {
			scope = fmt.Sprintf("for project %q", name)
		}
	}
	count := "no open tasks"
	switch len(rows) {
	case 0:
	case 1:
		count = "1 open task"
	default:
		count = fmt.Sprintf("%d open tasks", len(rows))
	}
	fmt.Fprintf(w, "rf: %s %s. rf -e '(tasks)' lists them; the /task skill files and completes them.\n", count, scope)
}

func init() {
	hookCmd.Flags().BoolVar(&hookInstall, "install", false,
		"write the hooks into ~/.claude/settings.json (preserving unrelated settings)")
	hookCmd.Flags().BoolVar(&hookFire, "fire", false,
		"handle one hook callback: read the payload from stdin, report status, capture on Stop")
	rootCmd.AddCommand(hookCmd)
}
