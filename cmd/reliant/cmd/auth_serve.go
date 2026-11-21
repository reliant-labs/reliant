// Copyright (c) 2025 Reliant Labs
package cmd

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/reliant-labs/reliant/internal/auth/oauthhelper"
	"github.com/spf13/cobra"
)

func newAuthServeCmd() *cobra.Command {
	var port int

	cmd := &cobra.Command{
		Use:   "serve",
		Short: "Start the local callback helper for Claude and Codex",
		Long: `Starts a lightweight HTTP server on localhost that handles OAuth
callback flows for Claude and Codex authentication. It does not sign in to
Reliant or start a browser on its own. To sign in to the Reliant CLI, run
'reliant auth login'.

USUALLY YOU DO NOT NEED THIS. ` + "`reliant daemon start`" + ` serves the same
endpoints when it runs on the machine your browser is on, so a local daemon
already covers it. Run this when the daemon is REMOTE (or not running) and you
still want to connect an account from this machine — the OAuth provider
redirects to localhost, which only exists where your browser is.

The server exposes:
  GET  /health       — identity + readiness (service, version), for the web app's probe
  POST /oauth/start  — Start an OAuth flow (opens browser, waits for callback)

Example:
  reliant auth serve
  reliant auth serve --port 19284`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runAuthServe(cmd, port)
		},
	}

	cmd.Flags().IntVar(&port, "port", oauthhelper.DefaultPort, "Port to listen on")

	return cmd
}

func runAuthServe(cmd *cobra.Command, port int) error {
	origins, err := cliWebOrigins(cmd)
	if err != nil {
		return err
	}
	srv, err := oauthhelper.Start(oauthhelper.Options{
		Port:         port,
		Source:       "auth-serve",
		ExtraOrigins: origins,
	})
	if err != nil {
		return err
	}

	fmt.Fprintf(cmd.OutOrStdout(), "OAuth helper server listening on http://%s\n", srv.Addr())
	fmt.Fprintln(cmd.OutOrStdout(), "Waiting for the Reliant web app to start a Claude or Codex connection.")
	fmt.Fprintln(cmd.OutOrStdout(), "To sign in to the Reliant CLI instead, stop this helper and run 'reliant auth login'.")
	fmt.Fprintln(cmd.OutOrStdout(), "Press Ctrl+C to stop")

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	defer signal.Stop(sigCh)
	<-sigCh

	fmt.Fprintln(cmd.OutOrStdout(), "\nShutting down...")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return srv.Shutdown(shutdownCtx)
}
