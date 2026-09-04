package cmd

import (
	"fmt"

	"github.com/retrofilter/rf/console"
	"github.com/spf13/cobra"
)

var tokenCmd = &cobra.Command{
	Use:   "token",
	Short: "print the console sign-in token",
	Long: `Print the current retrofilter console token.

Paste it into the console's /login page to sign a browser in; the browser
then holds an expiring session, not the token. The token rotates weekly on
its own (machine callers — hooks, shell builtins — follow automatically).`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		tok, err := console.LoadToken(false)
		if err != nil {
			return err
		}
		fmt.Println(tok)
		return nil
	},
}

func init() {
	rootCmd.AddCommand(tokenCmd)
}
