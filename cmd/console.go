package cmd

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/retrofilter/rf/console"
	"github.com/retrofilter/rf/core"
	"github.com/spf13/cobra"
)

var consoleCmd = &cobra.Command{
	Use:   "console",
	Short: "serve the retrofilter console (session dashboard) on localhost",
	RunE: func(cmd *cobra.Command, args []string) error {
		port, _ := cmd.Flags().GetInt("port")
		bind, _ := cmd.Flags().GetString("bind")
		rotate, _ := cmd.Flags().GetBool("new-token")
		noOpen, _ := cmd.Flags().GetBool("no-open")

		token, err := console.LoadToken(rotate)
		if err != nil {
			return err
		}

		db, err := openMainDB()
		if err != nil {
			return err
		}
		defer db.Close()
		ln, err := net.Listen("tcp", net.JoinHostPort(bind, fmt.Sprintf("%d", port)))
		if err != nil {
			return err
		}
		port = ln.Addr().(*net.TCPAddr).Port
		urlHost := "127.0.0.1"
		if ip := net.ParseIP(bind); bind != "127.0.0.1" && bind != "localhost" {
			fmt.Println("warning: serving cleartext HTTP beyond loopback — front with a TLS proxy (see the console guide topic) or keep it on a private network")
			if ip != nil && !ip.IsLoopback() && !ip.IsUnspecified() {
				fmt.Println("warning: this bind excludes 127.0.0.1, so Claude Code hooks and shell builtins on this machine cannot reach the console; use --bind 0.0.0.0 to serve both")
				urlHost = bind
			}
		}

		statePath, err := core.WebStatePath()
		if err != nil {
			return err
		}
		tokenFile, err := core.WebTokenPath()
		if err != nil {
			return err
		}
		sessionsPath, err := core.WebSessionsPath()
		if err != nil {
			return err
		}
		srv := console.NewServer(console.Config{Token: token, TokenPath: tokenFile, SessionsPath: sessionsPath, Port: port, Store: core.NewGraphStore(db), StatePath: statePath})
		if names, err := srv.Recover(); err != nil {
			fmt.Printf("session recovery: %v\n", err)
		} else if len(names) > 0 {
			fmt.Printf("recovered %d session(s): %s\n", len(names), strings.Join(names, ", "))
		}
		fmt.Printf("retrofilter console: http://%s/ (sign in with `rf token`)\n", net.JoinHostPort(urlHost, fmt.Sprintf("%d", port)))
		if !noOpen {
			openBrowser(fmt.Sprintf("http://%s/?token=%s", net.JoinHostPort(urlHost, fmt.Sprintf("%d", port)), srv.MintLaunchNonce()))
		}
		server := &http.Server{Handler: srv.Handler(), ReadHeaderTimeout: 5 * time.Second}
		stop := make(chan os.Signal, 1)
		signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
		go func() {
			<-stop
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			_ = server.Shutdown(ctx)
		}()
		if err := server.Serve(ln); err != nil && err != http.ErrServerClosed {
			return err
		}
		return nil
	},
}

func openBrowser(url string) {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", url)
	default:
		cmd = exec.Command("xdg-open", url)
	}
	if err := cmd.Start(); err == nil {
		go func() { _ = cmd.Wait() }()
	}
}

func init() {
	consoleCmd.Flags().Int("port", console.DefaultPort, "port to listen on")
	consoleCmd.Flags().String("bind", "127.0.0.1", "address to listen on; non-loopback serves cleartext HTTP — front it with a TLS proxy")
	consoleCmd.Flags().Bool("new-token", false, "rotate the persistent console token before starting")
	consoleCmd.Flags().Bool("no-open", false, "don't open the browser automatically")
	rootCmd.AddCommand(consoleCmd)
}
