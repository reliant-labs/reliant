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

	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/cliauth"
	"github.com/reliant-labs/reliant/internal/daemoninstance"
	"github.com/reliant-labs/reliant/internal/daemonpolicy"
	"github.com/reliant-labs/reliant/internal/forgecred"
	"github.com/reliant-labs/reliant/internal/instanceid"
	"github.com/reliant-labs/reliant/internal/logging"
	"github.com/reliant-labs/reliant/internal/toolexec/daemonstate"
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

ONE ACCOUNT PER MACHINE. The daemon on this machine has its own sign-in
(~/.reliant/daemon.json, see 'reliant daemon start'). Logging in as one
account removes a saved daemon sign-in of any other account for this server,
says so, and names any daemon still running as it; the next 'reliant daemon
start' pairs the machine as you.

For CI, skip login and set RELIANT_TOKEN.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			target, err := resolveServer(cmd)
			if err != nil {
				return err
			}
			flow := chooseLoginFlow(forceDevice, forceBrowser,
				os.Getenv, //nolint:forbidigo // SSH_*, DISPLAY, RELIANT_DAEMON_TYPE and the agent-shell marker describe the host this process runs on, not configuration of it
				runtime.GOOS)
			// ONE LOGIN: the forge scopes ride along as a CEILING the control
			// plane clips to your grants, so forge can later exchange this
			// login for a short-lived deploy token instead of asking for a
			// second login. A token may never grant authority it does not
			// hold, so without them there would be nothing to exchange.
			// ScopeMachines: `reliant machine` manages the user's own
			// machines as this login.
			scopes := append([]string{cliauth.ScopeAPI, cliauth.ScopeMachines}, cliauth.ForgeScopes()...)
			cred, err := runLogin(cmd.Context(), cmd.OutOrStdout(), loginRequest{
				Server: target.ServerURL,
				Scopes: scopes,
				Name:   cliLoginName(),
			}, flow)
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

// loginRequest is what a login asks the control plane for: a token with
// Scopes, named <client>@<Name>, from the authorization server Server names.
type loginRequest struct {
	Server string
	Scopes []string
	Name   string
}

// runLogin gets a token by flow: `reliant auth login`'s own, or an API token
// `reliant auth token create` mints. Both flows ask for the same authority
// under the same name, so where it came from makes no difference to the
// token. out receives the prompts.
func runLogin(ctx context.Context, out io.Writer, req loginRequest, flow loginFlow) (credentials.Credential, error) {
	server, scopes := req.Server, req.Scopes
	if flow.Device {
		if !flow.Forced {
			fmt.Fprintf(out, "Signing in with a device code: %s, so a browser login's callback could not reach your browser (--browser overrides).\n", flow.Remote)
		}
		dev := cliauth.DeviceLogin{
			Server:   server,
			Scopes:   scopes,
			Name:     req.Name,
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
		cred, err := dev.Run(ctx)
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
		Name:    req.Name,
		Out:     out,
		OpenURL: loginOpener,
	}.Run(ctx)
}

// finishLogin stores a fresh login, retires what the previous one left behind
// and reports it — the same for either flow. A saved daemon sign-in of another
// account is part of what is retired: the machine is kept to one account
// (account_in_step.go).
func finishLogin(cmd *cobra.Command, target *connection, cred credentials.Credential) error {
	path, err := cliauth.Store(target.ServerURL, cred)
	if err != nil {
		return fmt.Errorf("saving credentials: %w", err)
	}
	account := cliauth.AccountOf(cred)
	retired, retireErr := retireOtherAccountsDaemonSignIns(cmd.Context(), target, account)
	// A fresh login means fresh exchanges: drop tokens cached from the
	// previous one, and the copies older releases deposited into forge's file.
	if err := newForgeCredentialService(cmd, forgecred.Pin{}).ForgetServer(target.ServerURL); err != nil {
		logging.Warn("could not clear cached forge tokens", "error", err)
	}
	retireLegacyForgeDeposits()
	out := cmd.OutOrStdout()
	fmt.Fprintf(out, "Logged in to %s\n", target.describeServer())
	if account.Known() {
		fmt.Fprintf(out, "  Account:     %s\n", account)
	}
	fmt.Fprintf(out, "  Token:       %s… (%s)\n", cred.TokenPrefix, strings.Join(cred.Scopes, " "))
	if cred.ExpiresAt != nil {
		fmt.Fprintf(out, "  Expires:     %s\n", cred.ExpiresAt.Format(time.RFC3339))
	}
	fmt.Fprintf(out, "  Credentials: %s\n", path)
	writeRetiredSignIns(out, retired, account)
	if retireErr != nil {
		daemonPath, _ := auth.DaemonCredentialsFilePath()
		return fmt.Errorf("logged in, but could not remove another account's daemon sign-in for %s from %s: %w", target.ServerURL, daemonPath, retireErr)
	}
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
			account := cliauth.AccountOf(cred)
			if account.Known() {
				info["account_id"] = account.ID
				if account.Email != "" {
					info["account_email"] = account.Email
				}
			}
			info["token_prefix"] = cred.TokenPrefix
			info["scopes"] = cred.Scopes
			if cred.ExpiresAt != nil {
				info["expires_at"] = cred.ExpiresAt.Format(time.RFC3339)
			}
			if jsonOutput {
				return writeJSON(out, info)
			}
			fmt.Fprintf(out, "Logged in to %s\n", target.describeServer())
			if account.Known() {
				fmt.Fprintf(out, "  Account:     %s\n", account)
			} else {
				fmt.Fprintf(out, "  Account:     not recorded (this login predates it; 'reliant auth login%s' records it)\n", target.serverFlagHint())
			}
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
		Short: "Sign this machine out of the resolved server",
		Long: `Signs this machine out of the resolved server. It removes the CLI's login
from the shared credentials file, and every daemon sign-in saved for that
server in ~/.reliant/daemon.json, so the next 'reliant daemon start' pairs the
machine again (it prints a code to approve in the Reliant app) instead of
quietly connecting as whoever signed in last. Other servers' entries, and
forge's own logins, are untouched.

A daemon that is already running stays connected, as the account it started
with, until it is stopped; logout names any it finds and how to stop them. The
desktop app has its own sign-in: the next time it starts its daemon it saves a
new daemon sign-in for whoever is signed in to the app. On a Reliant cloud
machine the daemon's sign-in belongs to the platform and is left alone.

The tokens stay valid server-side until they expire or you revoke them under
Settings → Access Tokens in the app.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			target, err := resolveServer(cmd)
			if err != nil {
				return err
			}
			loggedIn, _, err := cliauth.Remove(target.ServerURL)
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

			out := cmd.OutOrStdout()
			// A cloud machine's daemon credential is a Secret the workspace
			// operator mounts read-only: it is the platform's, not this
			// login's, and removing it would leave the machine unable to
			// reconnect after its next restart.
			if isManagedMachine() {
				writeLogoutReport(out, target.ServerURL, loggedIn, 0, nil)
				fmt.Fprintln(out, "  This is a Reliant cloud machine: its daemon keeps the sign-in Reliant manages for it.")
				return nil
			}
			removed, delErr := auth.DeleteDaemonCredentialsForServer(target.ServerURL)
			writeLogoutReport(out, target.ServerURL, loggedIn, len(removed), runningDaemonsFor(target.ServerURL))
			if delErr != nil {
				path, _ := auth.DaemonCredentialsFilePath()
				return fmt.Errorf("could not remove this machine's daemon sign-in for %s from %s: %w", target.ServerURL, path, delErr)
			}
			return nil
		},
	}
}

// runningDaemon is a live daemon `auth logout` found for the server it signed
// out of.
type runningDaemon struct {
	// PID is 0 when the daemon has not published its runtime record yet.
	PID     int
	DataDir string
	// slug is the instance key, "<origin>/<account>/<workspace>".
	slug string
}

// runningDaemonsFor lists the daemons alive on this machine for serverURL's
// origin, in any account or workspace. Liveness is the instance lock, exactly
// as `daemon ls` reports it. Best-effort: a sign-out never fails because the
// instances directory could not be read.
func runningDaemonsFor(serverURL string) []runningDaemon {
	origin := daemoninstance.OriginKey(serverURL)
	if origin == "" {
		return nil
	}
	instances, err := daemonstate.ListDefault()
	if err != nil {
		logging.Warn("could not list daemon instances", "error", err)
		return nil
	}
	prefix := daemoninstance.Key{Origin: origin}.OriginSlug() + "/"
	var running []runningDaemon
	for _, instance := range instances {
		if !instance.Locked || !strings.HasPrefix(instance.Slug, prefix) {
			continue
		}
		d := runningDaemon{DataDir: instance.DataDir, slug: instance.Slug}
		if instance.HasRecord {
			d.PID = instance.Record.PID
		}
		running = append(running, d)
	}
	return running
}

// writeLogoutReport says what `auth logout` removed, and what it could not:
// a daemon that is already connected keeps its stream. Each daemon is named
// with a stop command that reaches it by its data directory, because a no-flag
// `daemon stop` finds its instance through the very credential just removed.
func writeLogoutReport(w io.Writer, server string, cliLogin bool, daemonSignIns int, running []runningDaemon) {
	if !cliLogin && daemonSignIns == 0 {
		fmt.Fprintf(w, "Not logged in to %s\n", server)
	} else {
		fmt.Fprintf(w, "Logged out of %s\n", server)
		signIn := "this machine's daemon sign-in"
		if daemonSignIns > 1 {
			signIn = fmt.Sprintf("this machine's daemon sign-ins (%d accounts)", daemonSignIns)
		}
		switch {
		case cliLogin && daemonSignIns > 0:
			fmt.Fprintf(w, "  Removed the CLI login and %s.\n", signIn)
		case cliLogin:
			fmt.Fprintln(w, "  Removed the CLI login.")
		default:
			fmt.Fprintf(w, "  Removed %s.\n", signIn)
		}
		if daemonSignIns > 0 {
			fmt.Fprintln(w, "  The next 'reliant daemon start' prints a pairing code to approve in Reliant.")
		}
	}
	for _, d := range running {
		pid := ""
		if d.PID > 0 {
			pid = fmt.Sprintf(" (PID %d)", d.PID)
		}
		fmt.Fprintf(w, "  A daemon is still running%s and stays connected as the signed-out account. Stop it with:\n", pid)
		fmt.Fprintf(w, "    reliant daemon stop --data-dir %s\n", shellQuote(d.DataDir))
	}
}

// shellQuote single-quotes s for a POSIX shell when it holds anything a shell
// would split or expand, so a printed command can be pasted as is.
func shellQuote(s string) string {
	if s != "" && !strings.ContainsAny(s, " \t\n'\"\\$`!*?[]{}()<>|&;#~") {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// credentialsPathForHelp renders the shared file's location for --help.
func credentialsPathForHelp() string {
	if p, err := cliauth.CredentialsPath(); err == nil {
		return p
	}
	return "~/.config/forge/credentials.json"
}
