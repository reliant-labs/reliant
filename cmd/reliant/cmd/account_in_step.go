// Copyright (c) 2025 Reliant Labs
package cmd

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/cliauth"
	"github.com/reliant-labs/reliant/internal/daemoninstance"
	"github.com/reliant-labs/reliant/internal/logging"
	"github.com/reliant-labs/reliant/internal/toolexec/bootstrap"
)

// ── ONE ACCOUNT PER MACHINE ──────────────────────────────────────────────
// A machine holds two Reliant sign-ins: the CLI login (`reliant auth login`,
// a 90-day token in forge's credentials file) and the daemon sign-in (a
// non-expiring daemon:connect token in ~/.reliant/daemon.json). They stay two
// tokens — a CLI login must never be able to mint a non-expiring machine
// credential — but each records the account it belongs to, and the machine
// keeps them to ONE account:
//
//   - `reliant auth login` as X removes a saved daemon sign-in of any other
//     account for that server (retireOtherAccountsDaemonSignIns).
//   - `reliant daemon start` with a CLI login uses the daemon sign-in that is
//     X's, or pairs as X; it never connects quietly as someone else
//     (daemonAccountForCLILogin).
//
// An account that cannot be told — a login or sign-in saved before accounts
// were recorded, a control plane that does not name them, a gateway that
// cannot be reached — is unknown, and unknown never fails a command: it is
// left alone, exactly as before.

// daemonIdentifyTimeout bounds the gateway question "whose is this saved
// daemon sign-in" asked while reconciling. Shorter than daemonWhoAmITimeout:
// a gateway that cannot answer leaves the sign-in unknown, which is safe, and
// a login should not wait on it.
const daemonIdentifyTimeout = 5 * time.Second

// cliLoginAccount is the account the CLI login for server signed in as: zero
// when there is no login, or it names none. Offline.
func cliLoginAccount(server string) cliauth.Account {
	c, _, err := cliauth.Lookup(server)
	if err != nil {
		return cliauth.Account{}
	}
	return cliauth.AccountOf(c)
}

// daemonSignInAccount is who a saved daemon sign-in acts as, as recorded.
func daemonSignInAccount(c *auth.DaemonCredentials) cliauth.Account {
	return cliauth.Account{ID: c.AccountID, Email: c.AccountEmail}
}

// gatewayForSignIn says where to ask about one saved daemon sign-in.
type gatewayForSignIn func(*auth.DaemonCredentials) (daemonGateway, error)

// savedSignInGateway is the gateway a saved sign-in connects to: its own, else
// the one the command resolved for the server.
func savedSignInGateway(conn *connection) gatewayForSignIn {
	return func(c *auth.DaemonCredentials) (daemonGateway, error) {
		raw := c.GatewayURL
		if raw == "" {
			raw = conn.GatewayURL
		}
		url, err := bootstrap.NormalizeGatewayURL(raw)
		if err != nil {
			return daemonGateway{}, err
		}
		mode := bootstrap.TLSModeH2C
		if strings.HasPrefix(url, "https://") {
			mode = bootstrap.TLSModeTLS
		}
		return daemonGateway{url: url, tlsMode: mode}, nil
	}
}

// identifyDaemonSignIn returns who a saved daemon sign-in acts as: what is
// recorded with it, else what the gateway says (WhoAmI), recorded for next
// time. Zero when neither can tell.
func identifyDaemonSignIn(ctx context.Context, server string, c *auth.DaemonCredentials, gatewayFor gatewayForSignIn) cliauth.Account {
	if a := daemonSignInAccount(c); a.Known() {
		return a
	}
	gateway, err := gatewayFor(c)
	if err != nil {
		logging.WarnContext(ctx, "cannot ask the gateway whose saved daemon sign-in this is", "account", c.Sub, "error", err)
		return cliauth.Account{}
	}
	ctx, cancel := context.WithTimeout(ctx, daemonIdentifyTimeout)
	defer cancel()
	owner, err := whoAmI(ctx, gateway, c.PAT)
	if err != nil {
		logging.WarnContext(ctx, "the gateway did not say whose saved daemon sign-in this is", "account", c.Sub, "error", err)
		return cliauth.Account{}
	}
	if err := auth.RecordDaemonAccount(server, c.Sub, owner, ""); err != nil {
		logging.WarnContext(ctx, "could not record whose saved daemon sign-in this is", "account", c.Sub, "error", err)
	}
	c.AccountID = owner
	return cliauth.Account{ID: owner}
}

// instanceAccountOf is the daemon instance account a stored sign-in runs in:
// its store account, with DefaultAccount spelled "" as the instance key spells
// it (daemoninstance.DefaultSubSegment).
func instanceAccountOf(c *auth.DaemonCredentials) string {
	if c.Sub == auth.DefaultAccount {
		return ""
	}
	return c.Sub
}

// isManagedMachine reports a Reliant cloud machine, whose daemon sign-in is a
// Secret the platform mounts read-only: no login of this machine's owns it.
func isManagedMachine() bool {
	return os.Getenv("RELIANT_DAEMON_TYPE") == "managed" //nolint:forbidigo // describes the host this process runs on, as in loopbackUnreachable
}

// retiredSignIns is what `reliant auth login` removed to keep the machine to
// one account.
type retiredSignIns struct {
	accounts []cliauth.Account
	running  []runningDaemon
}

// retireOtherAccountsDaemonSignIns removes every saved daemon sign-in for
// server that belongs to an account other than login's, and names the daemons
// still running as one of them. A sign-in whose account cannot be told is
// left: unknown is not "someone else". A cloud machine's sign-in is the
// platform's and is never touched.
func retireOtherAccountsDaemonSignIns(ctx context.Context, conn *connection, login cliauth.Account) (retiredSignIns, error) {
	var out retiredSignIns
	if !login.Known() || isManagedMachine() {
		return out, nil
	}
	saved, err := auth.ListDaemonCredentialsForServer(conn.ServerURL)
	if err != nil {
		return out, err
	}
	gatewayFor := savedSignInGateway(conn)
	for _, c := range saved {
		a := identifyDaemonSignIn(ctx, conn.ServerURL, c, gatewayFor)
		if !a.Known() || a.Same(login) {
			continue
		}
		if err := auth.DeleteDaemonCredentials(conn.ServerURL, c.Sub); err != nil {
			return out, err
		}
		out.accounts = append(out.accounts, a)
		out.running = append(out.running, runningDaemonsForAccount(conn.ServerURL, instanceAccountOf(c))...)
	}
	return out, nil
}

// writeRetiredSignIns says, in one line, which daemon sign-ins `auth login`
// removed and why, then names each daemon still running as one of them.
func writeRetiredSignIns(w io.Writer, retired retiredSignIns, login cliauth.Account) {
	if len(retired.accounts) == 0 {
		return
	}
	names := make([]string, 0, len(retired.accounts))
	for _, a := range retired.accounts {
		names = append(names, a.String())
	}
	what, was := "sign-in", "it was another account"
	if len(names) > 1 {
		what, was = "sign-ins", "they were other accounts"
	}
	fmt.Fprintf(w, "  Removed this machine's daemon %s for %s: %s. The next 'reliant daemon start' pairs this machine as %s.\n",
		what, strings.Join(names, ", "), was, login)
	for _, d := range retired.running {
		pid := ""
		if d.PID > 0 {
			pid = fmt.Sprintf(" (PID %d)", d.PID)
		}
		fmt.Fprintf(w, "  A daemon is still running%s, connected as the removed account. Stop it with:\n", pid)
		fmt.Fprintf(w, "    reliant daemon stop --data-dir %s\n", shellQuote(d.DataDir))
	}
}

// daemonAccountForCLILogin keeps `daemon start` to the CLI login's account.
//
// With a CLI login for the server that names its account, it returns the
// instance account to run as and that login's account (want): the saved
// daemon sign-in that is the login's account, made the origin's default so a
// no-flag `daemon status`/`stop` lands on the same instance; or, when none
// is, the login's account itself, so `daemon start` pairs a new sign-in filed
// under it. It says which, when a sign-in of another account was passed over.
//
// With no such login want is zero, and the caller resolves the account as it
// always did.
func daemonAccountForCLILogin(ctx context.Context, cmd *cobra.Command, conn *connection, gatewayFor gatewayForSignIn) (account string, want cliauth.Account) {
	login := cliLoginAccount(conn.ServerURL)
	if !login.Known() {
		return "", cliauth.Account{}
	}
	saved, err := auth.ListDaemonCredentialsForServer(conn.ServerURL)
	if err != nil {
		logging.WarnContext(ctx, "could not read this machine's daemon sign-ins", "error", err)
	}
	var others []string
	unconfirmed := false
	for _, c := range saved {
		a := identifyDaemonSignIn(ctx, conn.ServerURL, c, gatewayFor)
		switch {
		case a.Same(login):
			if err := auth.MakeDefaultDaemonAccount(conn.ServerURL, c.Sub); err != nil {
				logging.WarnContext(ctx, "could not make the CLI login's daemon sign-in the default", "error", err)
			}
			return instanceAccountOf(c), login
		case a.Known():
			others = append(others, a.String())
		default:
			unconfirmed = true
		}
	}
	out := cmd.OutOrStdout()
	switch {
	case len(others) > 0:
		fmt.Fprintf(out, "This machine's saved daemon sign-in is %s, but you are logged in to the CLI as %s — pairing this machine as %s instead.\n",
			strings.Join(others, ", "), login, login)
	case unconfirmed:
		fmt.Fprintf(out, "This machine's saved daemon sign-in could not be confirmed as %s, the account you are logged in to the CLI as — pairing this machine as %s instead.\n",
			login, login)
	}
	return login.ID, login
}

// runningDaemonsForAccount is runningDaemonsFor narrowed to the daemons
// running in one account's instances.
func runningDaemonsForAccount(serverURL, account string) []runningDaemon {
	origin := daemoninstance.OriginKey(serverURL)
	if origin == "" {
		return nil
	}
	key := daemoninstance.Key{Origin: origin, Sub: account}
	prefix := key.OriginSlug() + "/" + key.SubSlug() + "/"
	var out []runningDaemon
	for _, d := range runningDaemonsFor(serverURL) {
		if strings.HasPrefix(d.slug, prefix) {
			out = append(out, d)
		}
	}
	return out
}
