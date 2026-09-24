// Copyright (c) 2025 Reliant Labs
package commands

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/reliant-labs/reliant/internal/cliauth"
	"github.com/reliant-labs/reliant/internal/instanceid"
)

func newAuthCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "auth",
		Short: "Manage authentication",
		Long: `Authenticate the CLI with a Reliant server. A login is an rlat_ access token
issued by the control plane, stored in the credentials file shared with
forge (see 'reliant auth login --help'), keyed by the server it is for.`,
	}

	cmd.AddCommand(newAuthLoginCmd())
	cmd.AddCommand(newAuthStatusCmd())
	cmd.AddCommand(newAuthLogoutCmd())
	cmd.AddCommand(newAuthServeCmd())
	cmd.AddCommand(newAuthTokenCmd())

	return cmd
}

// cliLoginName labels the token a plain `reliant auth login` mints. One per
// machine: logging in again from here replaces it server-side.
func cliLoginName() string { return instanceid.Hostname() }

// loginOpener is the browser launcher for `reliant auth login`; a seam so the
// command's test drives the real flow against an httptest control plane.
var loginOpener = cliauth.OpenBrowser

func newAuthLoginCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "login",
		Short: "Log in to a Reliant server",
		Long: `Logs the CLI in to the resolved server (--server, else RELIANT_SERVER_URL,
else the built-in default) with the OAuth 2.0 authorization-code flow + PKCE.

The server names its authorization server (the control plane) at
/.well-known/oauth-authorization-server. Your browser opens there; you sign
in with your normal Reliant account and approve; a one-time code comes back
to a temporary listener on a loopback port, and is exchanged for an rlat_
access token (90 days, scope reliant:api).

The token is stored in the credentials file shared with forge
(` + credentialsPathForHelp() + `), under this server. There is no "current"
server: a later command against another --server finds no login there and
says so, rather than sending this token somewhere it was not issued for.

For CI, skip login and set RELIANT_TOKEN.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			target, err := resolveServer(cmd)
			if err != nil {
				return err
			}
			cred, err := cliauth.Login{
				Server:  target.ServerURL,
				Scopes:  []string{cliauth.ScopeAPI},
				Name:    cliLoginName(),
				Out:     cmd.OutOrStdout(),
				OpenURL: loginOpener,
			}.Run(cmd.Context())
			if err != nil {
				return fmt.Errorf("login failed: %w", err)
			}
			path, err := cliauth.Store(target.ServerURL, cred)
			if err != nil {
				return fmt.Errorf("saving credentials: %w", err)
			}
			out := cmd.OutOrStdout()
			fmt.Fprintf(out, "Logged in to %s\n", target.describeServer())
			fmt.Fprintf(out, "  Token:       %s… (%s)\n", cred.TokenPrefix, strings.Join(cred.Scopes, " "))
			if cred.ExpiresAt != nil {
				fmt.Fprintf(out, "  Expires:     %s\n", cred.ExpiresAt.Format(time.RFC3339))
			}
			fmt.Fprintf(out, "  Credentials: %s\n", path)
			return nil
		},
	}
	return cmd
}

func newAuthStatusCmd() *cobra.Command {
	var jsonOutput bool

	cmd := &cobra.Command{
		Use:   "status",
		Short: "Show the login for the resolved server",
		Long: `Shows which credential the CLI would use for the resolved server (and where
that server came from: the --server flag, RELIANT_SERVER_URL, or the default).
Purely local: no network call.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			target, err := resolveServer(cmd)
			if err != nil {
				return err
			}
			info := map[string]any{
				"server":      target.ServerURL,
				"server_from": target.describeServer(),
			}
			out := cmd.OutOrStdout()
			if os.Getenv(envToken) != "" {
				info["logged_in"] = true
				info["credential"] = envToken
				if jsonOutput {
					return writeJSON(out, info)
				}
				fmt.Fprintf(out, "Using the token in %s\n  Server: %s\n", envToken, target.describeServer())
				return nil
			}
			cred, path, err := cliauth.Lookup(target.ServerURL)
			if err != nil {
				info["logged_in"] = false
				info["reason"] = err.Error()
				if jsonOutput {
					_ = writeJSON(out, info)
				} else {
					fmt.Fprintf(out, "Not logged in (%v)\n  Server: %s\nRun 'reliant auth login%s' to authenticate\n",
						err, target.describeServer(), target.serverFlagHint())
				}
				return fmt.Errorf("not authenticated")
			}
			info["logged_in"] = true
			info["credential"] = path
			info["token_prefix"] = cred.TokenPrefix
			info["scopes"] = cred.Scopes
			if cred.ExpiresAt != nil {
				info["expires_at"] = cred.ExpiresAt.Format(time.RFC3339)
			}
			if jsonOutput {
				return writeJSON(out, info)
			}
			fmt.Fprintf(out, "Logged in to %s\n", target.describeServer())
			fmt.Fprintf(out, "  Token:       %s… (%s)\n", cred.TokenPrefix, strings.Join(cred.Scopes, " "))
			if cred.ExpiresAt != nil {
				fmt.Fprintf(out, "  Expires:     %s\n", cred.ExpiresAt.Format(time.RFC3339))
			}
			fmt.Fprintf(out, "  Credentials: %s\n", path)
			return nil
		},
	}

	cmd.Flags().BoolVar(&jsonOutput, "json", false, "Output in JSON format")
	return cmd
}

func writeJSON(w io.Writer, v any) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

func newAuthLogoutCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "logout",
		Short: "Forget the login for the resolved server",
		Long: `Removes the resolved server's entry from the shared credentials file. Every
other entry (other servers, and forge's logins) is untouched. The token stays
valid server-side until it expires or you revoke it in the web app.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			target, err := resolveServer(cmd)
			if err != nil {
				return err
			}
			existed, path, err := cliauth.Remove(target.ServerURL)
			if err != nil {
				return err
			}
			if !existed {
				fmt.Fprintf(cmd.OutOrStdout(), "Not logged in to %s (nothing in %s)\n", target.ServerURL, path)
				return nil
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Logged out of %s\n", target.ServerURL)
			fmt.Fprintln(cmd.OutOrStdout(), "Note: a running daemon uses its own credential and is unaffected")
			return nil
		},
	}
}

// credentialsPathForHelp renders the shared file's location for --help.
func credentialsPathForHelp() string {
	if p, err := cliauth.CredentialsPath(); err == nil {
		return p
	}
	return "~/.config/forge/credentials.json"
}
