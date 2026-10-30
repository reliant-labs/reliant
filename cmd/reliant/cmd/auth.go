// Copyright (c) 2025 Reliant Labs
package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"runtime"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/reliant-labs/forge/pkg/credentials"

	"github.com/reliant-labs/reliant/internal/cliauth"
	"github.com/reliant-labs/reliant/internal/daemonpolicy"
	"github.com/reliant-labs/reliant/internal/forgecred"
	"github.com/reliant-labs/reliant/internal/instanceid"
	"github.com/reliant-labs/reliant/internal/logging"
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
	cmd.AddCommand(newAuthForgeCredentialCmd())
	cmd.AddCommand(newAuthGitCredentialCmd())

	return cmd
}

// cliLoginName labels the token a plain `reliant auth login` mints. One per
// machine: logging in again from here replaces it server-side.
func cliLoginName() string { return instanceid.Hostname() }

// loginOpener is the browser launcher for `reliant auth login`; a seam so the
// command's test drives the real flow against an httptest control plane.
var loginOpener = cliauth.OpenBrowser

// deviceLoginSleep paces the device flow's polls; nil is a real timer. A seam
// so the command's test does not wait out the five-second poll interval.
var deviceLoginSleep func(context.Context, time.Duration) error

// loginFlow is how `reliant auth login` gets its token.
type loginFlow struct {
	// Device: RFC 8628 device code, approved in a browser on any device.
	// Otherwise the loopback authorization-code flow.
	Device bool
	// Forced: --device or --browser chose, not the environment.
	Forced bool
	// Remote names why no browser can reach a loopback listener on this
	// machine, or "" when nothing says so. A server without device sign-in
	// falls back to the browser flow only when it is "".
	Remote string
}

// chooseLoginFlow picks the flow. The browser flow's callback lands on a
// listener on 127.0.0.1 of THIS machine, so it needs a browser running here;
// where none can be — a Reliant cloud machine, an ssh session, a Linux host
// with no display, an agent's shell — the device flow is the one that can
// finish. getenv is os.Getenv outside tests.
func chooseLoginFlow(forceDevice, forceBrowser bool, getenv func(string) string, goos string) loginFlow {
	remote := loopbackUnreachable(getenv, goos)
	switch {
	case forceBrowser:
		return loginFlow{Forced: true, Remote: remote}
	case forceDevice:
		return loginFlow{Device: true, Forced: true, Remote: remote}
	}
	return loginFlow{Device: remote != "", Remote: remote}
}

// loopbackUnreachable says why a browser cannot reach a loopback listener on
// this machine, or "" when nothing in the environment says so.
func loopbackUnreachable(getenv func(string) string, goos string) string {
	switch {
	case getenv("RELIANT_DAEMON_TYPE") == "managed":
		return "this is a Reliant cloud machine"
	case getenv(daemonpolicy.AgentShellEnv) != "":
		return "this is a Reliant agent's shell"
	case getenv("SSH_CONNECTION") != "" || getenv("SSH_CLIENT") != "" || getenv("SSH_TTY") != "":
		return "this is an ssh session"
	case goos == "linux" && getenv("DISPLAY") == "" && getenv("WAYLAND_DISPLAY") == "":
		return "there is no graphical display ($DISPLAY and $WAYLAND_DISPLAY are unset)"
	}
	return ""
}

func newAuthLoginCmd() *cobra.Command {
	var forceDevice, forceBrowser bool
	cmd := &cobra.Command{
		Use:   "login",
		Short: "Log in to a Reliant server",
		Long: `Logs the CLI in to the resolved server (--server, else RELIANT_SERVER_URL,
else the built-in default) as your Reliant account. The server names its
authorization server (the control plane) at /.well-known/oauth-authorization-server.
Either way you get an rlat_ access token (90 days, scope reliant:api, plus the
deploy, secret and domain permissions your organization grants you — see below).

Two ways to approve it:

  Browser (OAuth 2.0 authorization code + PKCE). Your browser opens on the
  control plane; you sign in and approve; a one-time code comes back to a
  temporary listener on a loopback port of THIS machine.

  Device code (RFC 8628). The CLI prints a short code and a link. Open the link
  on any device — your laptop, your phone — sign in, check the code and
  approve "Sign in the Reliant CLI on <hostname>?". Nothing has to reach back
  to this machine.

The browser flow is the default, except where its callback cannot reach your
browser: a Reliant cloud machine (RELIANT_DAEMON_TYPE=managed), an ssh session,
a Linux host with no display, or a Reliant agent's shell. There the device flow
is used. --device or --browser chooses explicitly.

The token is stored in the credentials file shared with forge
(` + credentialsPathForHelp() + `), under this server. There is no "current"
server: a later command against another --server finds no login there and
says so, rather than sending this token somewhere it was not issued for.

FORGE NEEDS NO SEPARATE LOGIN. 'reliant forge …' (and every shell a Reliant
agent runs) asks 'reliant auth forge-credential' for a control-plane token,
which exchanges this login — or the daemon's credential — for one that holds
only deploy/secret/domain authority, for at most an hour. This login never
leaves this machine's Reliant files.

For CI, skip login and set RELIANT_TOKEN.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			target, err := resolveServer(cmd)
			if err != nil {
				return err
			}
			flow := chooseLoginFlow(forceDevice, forceBrowser,
				os.Getenv, //nolint:forbidigo // SSH_*, DISPLAY, RELIANT_DAEMON_TYPE and the agent-shell marker describe the host this process runs on, not configuration of it
				runtime.GOOS)
			cred, err := runLogin(cmd, target.ServerURL, flow)
			if err != nil {
				return fmt.Errorf("login failed: %w", err)
			}
			return finishLogin(cmd, target, cred)
		},
	}
	cmd.Flags().BoolVar(&forceDevice, "device", false, "Approve with a device code on any device (the default where a browser cannot reach this machine)")
	cmd.Flags().BoolVar(&forceBrowser, "browser", false, "Approve in a browser that calls back to this machine (the default on a desktop)")
	cmd.MarkFlagsMutuallyExclusive("device", "browser")
	return cmd
}

// runLogin gets the CLI's login token by flow. Both flows ask for the same
// authority under the same name, so where it came from makes no difference
// to the token.
func runLogin(cmd *cobra.Command, server string, flow loginFlow) (credentials.Credential, error) {
	out := cmd.OutOrStdout()
	// ONE LOGIN: the forge scopes ride along as a CEILING the control plane
	// clips to your grants, so forge can later exchange this login for a
	// short-lived deploy token instead of asking for a second login. A token
	// may never grant authority it does not hold, so without them there would
	// be nothing to exchange.
	scopes := append([]string{cliauth.ScopeAPI}, cliauth.ForgeScopes()...)
	if flow.Device {
		if !flow.Forced {
			fmt.Fprintf(out, "Signing in with a device code: %s, so a browser login's callback could not reach your browser (--browser overrides).\n", flow.Remote)
		}
		dev := cliauth.DeviceLogin{
			Server:   server,
			Scopes:   scopes,
			Name:     cliLoginName(),
			Hostname: instanceid.Hostname(),
			Platform: runtime.GOOS + "/" + runtime.GOARCH,
			Out:      out,
			Sleep:    deviceLoginSleep,
		}
		// Only where a browser here could show it: on a remote host an
		// opener at best fails and at worst starts a terminal browser over
		// the code this command just printed.
		if flow.Remote == "" {
			dev.OpenURL = loginOpener
		}
		cred, err := dev.Run(cmd.Context())
		if !errors.Is(err, cliauth.ErrNoDeviceFlow) {
			return cred, err
		}
		if flow.Remote != "" {
			return credentials.Credential{}, fmt.Errorf("%s does not offer device-code sign-in, and a browser login cannot work here (%s).\n"+
				"Mint a token in the web app and set RELIANT_TOKEN, or pass --browser if a browser can reach this machine after all", server, flow.Remote)
		}
		fmt.Fprintf(out, "%s does not offer device-code sign-in; approving in your browser instead.\n", server)
	}
	return cliauth.Login{
		Server:  server,
		Scopes:  scopes,
		Name:    cliLoginName(),
		Out:     out,
		OpenURL: loginOpener,
	}.Run(cmd.Context())
}

// finishLogin stores a fresh login, retires what the previous one left behind
// and reports it — the same for either flow.
func finishLogin(cmd *cobra.Command, target *connection, cred credentials.Credential) error {
	path, err := cliauth.Store(target.ServerURL, cred)
	if err != nil {
		return fmt.Errorf("saving credentials: %w", err)
	}
	// A fresh login means fresh exchanges: drop tokens cached from the
	// previous one, and the copies older releases deposited into forge's file.
	if err := newForgeCredentialService(cmd, forgecred.Pin{}).ForgetServer(target.ServerURL); err != nil {
		logging.Warn("could not clear cached forge tokens", "error", err)
	}
	retireLegacyForgeDeposits()
	out := cmd.OutOrStdout()
	fmt.Fprintf(out, "Logged in to %s\n", target.describeServer())
	fmt.Fprintf(out, "  Token:       %s… (%s)\n", cred.TokenPrefix, strings.Join(cred.Scopes, " "))
	if cred.ExpiresAt != nil {
		fmt.Fprintf(out, "  Expires:     %s\n", cred.ExpiresAt.Format(time.RFC3339))
	}
	fmt.Fprintf(out, "  Credentials: %s\n", path)
	return nil
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
			if target.envToken != "" {
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
					fmt.Fprintf(out, "%s\n  Server: %s\nRun 'reliant auth login%s' to authenticate\n",
						notLoggedInHeadline(err), target.describeServer(), target.serverFlagHint())
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

// notLoggedInHeadline is `auth status`'s first line when there is no usable
// login. The reason is added only when it says more than "not logged in" — an
// expired login, or a credentials file that could not be read.
func notLoggedInHeadline(err error) string {
	return "Not logged in" + notLoggedInReason(err)
}

// notLoggedInReason is " (<why>)" when err says more than cliauth's bare "not
// logged in", else "" — so no message reads "not logged in (not logged in)".
func notLoggedInReason(err error) string {
	reason := strings.TrimPrefix(err.Error(), cliauth.ErrNotLoggedIn.Error())
	reason = strings.TrimPrefix(reason, ": ")
	if reason == "" {
		return ""
	}
	return " (" + reason + ")"
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
			// Logging out of Reliant logs forge out of Reliant cloud: the
			// short-lived tokens exchanged from this server's sessions are
			// forgotten, so none outlives the sign-out on this machine. A
			// credential the user created with `forge login` is forge's own
			// and is left alone.
			if err := newForgeCredentialService(cmd, forgecred.Pin{}).ForgetServer(target.ServerURL); err != nil {
				logging.Warn("could not clear cached forge tokens", "error", err)
			}
			retireLegacyForgeDeposits()
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
