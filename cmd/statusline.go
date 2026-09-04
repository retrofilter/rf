package cmd

import (
	"os"

	"github.com/retrofilter/rf/console"
	"github.com/spf13/cobra"
)

var statuslineWrap string

var statuslineCmd = &cobra.Command{
	Use:   "statusline",
	Short: "render Claude Code's status line and record subscription usage",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		cmd.SilenceUsage = true
		return console.HandleStatusline(cmd.InOrStdin(), cmd.OutOrStdout(), os.Stderr, statuslineWrap)
	},
}

func init() {
	statuslineCmd.Flags().StringVar(&statuslineWrap, "wrap", "",
		"pre-existing statusline command to run for the output, with the payload passed through")
	rootCmd.AddCommand(statuslineCmd)
}
