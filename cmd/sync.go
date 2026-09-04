package cmd

import (
	"fmt"

	"github.com/retrofilter/rf/agent"
	"github.com/retrofilter/rf/agent/claude"
	"github.com/retrofilter/rf/eval"
	"github.com/spf13/cobra"
)

var syncCmd = &cobra.Command{
	Use:   "sync",
	Short: "import Claude Code transcripts into the rf database",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		cmd.SilenceUsage = true
		db, err := openMainDB()
		if err != nil {
			return err
		}
		defer db.Close()
		res, err := agent.Sync(db, agent.Options{Project: syncProject})
		fmt.Printf("synced %d new messages from %d files (%d sessions)\n",
			res.Messages, res.Files, res.Sessions)
		return err
	},
}

func syncProject(cwd string) string {
	if name, _, ok := eval.FindProject(cwd); ok {
		return name
	}
	return ""
}

func syncTranscript(path string) error {
	db, err := openMainDB()
	if err != nil {
		return err
	}
	defer db.Close()
	_, err = claude.Source{}.SyncFile(db, path, agent.Options{Project: syncProject})
	return err
}

func init() {
	rootCmd.AddCommand(syncCmd)
}
