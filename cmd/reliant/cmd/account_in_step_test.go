// Copyright (c) 2025 Reliant Labs
package cmd

import (
	"bytes"
	"context"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/forge/pkg/credentials"

	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/cliauth"
	"github.com/reliant-labs/reliant/internal/daemoninstance"
	"github.com/reliant-labs/reliant/internal/toolexec/daemonstate"
)

var (
	adaAccount  = cliauth.Account{ID: "ext-ada", Email: "ada@example.test"}
	bobAccount  = cliauth.Account{ID: "ext-bob", Email: "bob@example.test"}
	carlAccount = cliauth.Account{ID: "ext-carl"}
)

// loginAs stores token as the CLI login for server, signed in as account.
func loginAs(t *testing.T, server, token string, account cliauth.Account) {
	t.Helper()
	_, err := cliauth.Store(server, cliauth.Session{
		Credential: credentials.Credential{Token: token, TokenPrefix: token, CreatedAt: time.Now()},
		Account:    account,
	})
	require.NoError(t, err)
}

// saveDaemonSignIn stores a daemon sign-in under the store account sub,
// recorded as acting for account (zero: not recorded).
func saveDaemonSignIn(t *testing.T, server, sub, pat string, account cliauth.Account, gatewayURL string) {
	t.Helper()
	require.NoError(t, auth.WriteDaemonCredentials(&auth.DaemonCredentials{
		PAT: pat, ServerURL: server, GatewayURL: gatewayURL, Sub: sub, RegisteredAt: time.Now().UTC(),
		AccountID: account.ID, AccountEmail: account.Email,
	}))
}

func daemonSignInsFor(t *testing.T, server string) map[string]*auth.DaemonCredentials {
	t.Helper()
	saved, err := auth.ListDaemonCredentialsForServer(server)
	require.NoError(t, err)
	out := map[string]*auth.DaemonCredentials{}
	for _, c := range saved {
		out[c.Sub] = c
	}
	return out
}

// `reliant auth login` as one account retires a saved daemon sign-in of any
// other: the previous owner's machine sign-in is what made `daemon start`
// connect silently as them while the new user's onboarding waited for a
// pairing code. The login's own account, and one nobody can identify, stay.
func TestAuthLogin_RetiresAnotherAccountsDaemonSignIn(t *testing.T) {
	login := func(t *testing.T, cp *fakeCP) string {
		t.Helper()
		out, err := executeRoot(t, "auth", "login", "--server", cp.srv.URL)
		require.NoError(t, err, out)
		return out
	}

	t.Run("recorded other account, and a daemon still running as it", func(t *testing.T) {
		isolateLogout(t)
		withFakeLoginBrowser(t)
		cp := newFakeCP(t, "rlat_ADALOGIN00000000")
		cp.account = adaAccount
		saveDaemonSignIn(t, cp.srv.URL, "", "rlat_DAEMON_bob", bobAccount, "")
		saveDaemonSignIn(t, cp.srv.URL, adaAccount.ID, "rlat_DAEMON_ada", adaAccount, "")

		// Bob's daemon is running in the instance his sign-in was filed under.
		key, err := daemoninstance.Resolve(cp.srv.URL, "", "workspace-a")
		require.NoError(t, err)
		dataDir, err := key.EnsureDataDir()
		require.NoError(t, err)
		lock, err := daemonstate.Acquire(dataDir)
		require.NoError(t, err)
		t.Cleanup(func() { _ = lock.Release() })

		out := login(t, cp)
		require.Contains(t, out, "Logged in to "+cp.srv.URL+" (from the --server flag)\n  Account:     ada@example.test\n")
		require.Contains(t, out, "  Removed this machine's daemon sign-in for bob@example.test: it was another account. "+
			"The next 'reliant daemon start' pairs this machine as ada@example.test.\n"+
			"  A daemon is still running, connected as the removed account. Stop it with:\n"+
			"    reliant daemon stop --data-dir "+shellQuote(dataDir)+"\n")

		left := daemonSignInsFor(t, cp.srv.URL)
		require.Len(t, left, 1, "only the login's own daemon sign-in stays")
		require.Equal(t, "rlat_DAEMON_ada", left[adaAccount.ID].PAT)
		require.True(t, instanceLocked(t, dataDir), "login must not stop a running daemon")
	})

	t.Run("unrecorded sign-ins are identified by the gateway", func(t *testing.T) {
		isolateLogout(t)
		withFakeLoginBrowser(t)
		cp := newFakeCP(t, "rlat_ADALOGIN00000000")
		cp.account = adaAccount
		gateway := serveDaemonGateway(t, &whoAmIGateway{owners: map[string]string{
			"rlat_DAEMON_carl": carlAccount.ID,
			"rlat_DAEMON_ada":  adaAccount.ID,
		}})
		saveDaemonSignIn(t, cp.srv.URL, "", "rlat_DAEMON_carl", cliauth.Account{}, gateway.url)
		saveDaemonSignIn(t, cp.srv.URL, "paired-ada", "rlat_DAEMON_ada", cliauth.Account{}, gateway.url)
		// The gateway does not know this one: unknown is left alone.
		saveDaemonSignIn(t, cp.srv.URL, "mystery", "rlat_DAEMON_mystery", cliauth.Account{}, gateway.url)

		out := login(t, cp)
		require.Contains(t, out, "  Removed this machine's daemon sign-in for account ext-carl: it was another account.")

		left := daemonSignInsFor(t, cp.srv.URL)
		require.Len(t, left, 2)
		require.Equal(t, adaAccount.ID, left["paired-ada"].AccountID, "the gateway's answer is recorded for next time")
		require.NotNil(t, left["mystery"], "a sign-in nobody can identify is not someone else's")
	})

	t.Run("a control plane that names no account changes nothing", func(t *testing.T) {
		isolateLogout(t)
		withFakeLoginBrowser(t)
		cp := newFakeCP(t, "rlat_OLDCPLOGIN000000")
		saveDaemonSignIn(t, cp.srv.URL, "", "rlat_DAEMON_bob", bobAccount, "")

		out := login(t, cp)
		require.NotContains(t, out, "Account:")
		require.NotContains(t, out, "Removed")
		require.Len(t, daemonSignInsFor(t, cp.srv.URL), 1)
	})

	t.Run("a cloud machine's daemon sign-in is the platform's", func(t *testing.T) {
		isolateLogout(t)
		withFakeLoginBrowser(t)
		cp := newFakeCP(t, "rlat_ADALOGIN00000000")
		cp.account = adaAccount
		saveDaemonSignIn(t, cp.srv.URL, "", "rlat_DAEMON_bob", bobAccount, "")
		t.Setenv("RELIANT_DAEMON_TYPE", "managed")

		out, err := executeRoot(t, "auth", "login", "--server", cp.srv.URL, "--browser")
		require.NoError(t, err, out)
		require.NotContains(t, out, "Removed")
		require.Len(t, daemonSignInsFor(t, cp.srv.URL), 1)
	})
}

// `auth status` names the account, offline, from what the login recorded.
func TestAuthStatus_NamesTheAccount(t *testing.T) {
	isolateCLI(t)

	loginAs(t, testServerURL, "rlat_ADALOGIN00000000", adaAccount)
	out, err := executeRoot(t, "auth", "status", "--server", testServerURL)
	require.NoError(t, err, out)
	require.Contains(t, out, "Logged in to "+testServerURL+" (from the --server flag)\n  Account:     ada@example.test\n")

	out, err = executeRoot(t, "auth", "status", "--server", testServerURL, "--json")
	require.NoError(t, err, out)
	require.Contains(t, out, `"account_email": "ada@example.test"`)
	require.Contains(t, out, `"account_id": "ext-ada"`)

	// A login saved before accounts were recorded says so, and how to fix it.
	loginFor(t, testServerURL, "rlat_OLDLOGIN0000000")
	out, err = executeRoot(t, "auth", "status", "--server", testServerURL)
	require.NoError(t, err, out)
	require.Contains(t, out, "  Account:     not recorded (this login predates it; 'reliant auth login --server "+testServerURL+"' records it)\n")
}

// `daemon start` with a CLI login uses the daemon sign-in that is the login's
// account, or pairs as it — never connects quietly as someone else.
func TestDaemonAccountForCLILogin(t *testing.T) {
	noGateway := func(*auth.DaemonCredentials) (daemonGateway, error) {
		return daemonGateway{}, io.ErrUnexpectedEOF
	}
	run := func(t *testing.T, gatewayFor gatewayForSignIn) (string, cliauth.Account, string) {
		t.Helper()
		var out bytes.Buffer
		cmd := &cobra.Command{}
		cmd.SetOut(&out)
		account, want := daemonAccountForCLILogin(context.Background(), cmd, &connection{ServerURL: testServerURL}, gatewayFor)
		return account, want, out.String()
	}

	t.Run("no CLI login: as before", func(t *testing.T) {
		isolateLogout(t)
		saveDaemonSignIn(t, testServerURL, "", "rlat_DAEMON_bob", bobAccount, "")
		account, want, out := run(t, noGateway)
		require.Empty(t, account)
		require.False(t, want.Known())
		require.Empty(t, out)
	})

	t.Run("a CLI login that names no account: as before", func(t *testing.T) {
		isolateLogout(t)
		loginFor(t, testServerURL, "rlat_OLDLOGIN0000000")
		saveDaemonSignIn(t, testServerURL, "", "rlat_DAEMON_bob", bobAccount, "")
		_, want, out := run(t, noGateway)
		require.False(t, want.Known())
		require.Empty(t, out)
	})

	t.Run("the login's sign-in is used and becomes the default", func(t *testing.T) {
		isolateLogout(t)
		loginAs(t, testServerURL, "rlat_ADALOGIN00000000", adaAccount)
		saveDaemonSignIn(t, testServerURL, "", "rlat_DAEMON_ada", adaAccount, "")
		saveDaemonSignIn(t, testServerURL, "bob", "rlat_DAEMON_bob", bobAccount, "") // most recent: the default

		account, want, out := run(t, noGateway)
		require.Empty(t, account, "a sign-in filed under the default account runs in the default instance")
		require.Equal(t, adaAccount, want)
		require.Empty(t, out, "the saved-sign-in notice says the rest")
		def, err := auth.ReadDaemonCredentials(testServerURL, "")
		require.NoError(t, err)
		require.Equal(t, "rlat_DAEMON_ada", def.PAT, "a no-flag daemon status/stop must land on the same instance")

		// And ensureDaemonCredentials takes it, naming whose it is.
		var notice bytes.Buffer
		cmd := &cobra.Command{}
		cmd.SetOut(&notice)
		creds, err := ensureDaemonCredentials(context.Background(), cmd, &connection{ServerURL: testServerURL}, account, want, false)
		require.NoError(t, err)
		require.Equal(t, "rlat_DAEMON_ada", creds.PAT)
		require.Contains(t, notice.String(), "Using the daemon sign-in saved on this machine for ada@example.test on ")
		require.Contains(t, notice.String(), ", the account you are logged in to the CLI as, so there is no pairing code to approve.\n")
	})

	t.Run("another account's sign-in is passed over, and said so", func(t *testing.T) {
		isolateLogout(t)
		loginAs(t, testServerURL, "rlat_ADALOGIN00000000", adaAccount)
		saveDaemonSignIn(t, testServerURL, "", "rlat_DAEMON_bob", bobAccount, "")

		account, want, out := run(t, noGateway)
		require.Equal(t, adaAccount.ID, account, "the new sign-in is filed under the login's account")
		require.Equal(t, adaAccount, want)
		require.Equal(t, "This machine's saved daemon sign-in is bob@example.test, but you are logged in to the CLI as ada@example.test"+
			" — pairing this machine as ada@example.test instead.\n", out)
	})

	t.Run("a sign-in nobody can identify is not used", func(t *testing.T) {
		isolateLogout(t)
		loginAs(t, testServerURL, "rlat_ADALOGIN00000000", adaAccount)
		saveDaemonSignIn(t, testServerURL, "", "rlat_DAEMON_x", cliauth.Account{}, "")

		account, _, out := run(t, noGateway)
		require.Equal(t, adaAccount.ID, account)
		require.Equal(t, "This machine's saved daemon sign-in could not be confirmed as ada@example.test, the account you are logged in to the CLI as"+
			" — pairing this machine as ada@example.test instead.\n", out)
	})

	t.Run("an unrecorded sign-in the gateway names as the login's is used", func(t *testing.T) {
		isolateLogout(t)
		loginAs(t, testServerURL, "rlat_ADALOGIN00000000", adaAccount)
		gateway := serveDaemonGateway(t, &whoAmIGateway{owners: map[string]string{"rlat_DAEMON_ada": adaAccount.ID}})
		saveDaemonSignIn(t, testServerURL, "", "rlat_DAEMON_ada", cliauth.Account{}, "")

		account, want, out := run(t, func(*auth.DaemonCredentials) (daemonGateway, error) { return gateway, nil })
		require.Empty(t, account)
		require.Equal(t, adaAccount, want)
		require.Empty(t, out)
		saved, err := auth.ReadDaemonCredentials(testServerURL, "")
		require.NoError(t, err)
		require.Equal(t, adaAccount.ID, saved.AccountID, "the gateway's answer is recorded")
	})
}

// A pairing approved as anyone but the CLI login's account is refused and
// not saved: otherwise the machine would connect as the wrong person after
// all, with only a pairing code to show for it.
func TestRegisterDaemon_RefusesAnApprovalAsAnotherAccount(t *testing.T) {
	isolateLogout(t)
	withFakeLoginBrowser(t)
	cp := newFakeCP(t, "rlat_BOBDAEMON0000000")
	cp.account = bobAccount
	cmd := &cobra.Command{}
	cmd.SetOut(io.Discard)

	err := registerDaemon(context.Background(), cmd, &connection{ServerURL: cp.srv.URL}, adaAccount.ID, adaAccount, false)
	require.Error(t, err)
	require.True(t, strings.HasPrefix(err.Error(), "this machine was approved as bob@example.test, but you are logged in to the CLI as ada@example.test"), err.Error())
	require.Empty(t, daemonSignInsFor(t, cp.srv.URL), "a refused approval must not be saved")

	// Approved as the login's account: saved, with whose it is.
	cp.account = adaAccount
	require.NoError(t, registerDaemon(context.Background(), cmd, &connection{ServerURL: cp.srv.URL}, adaAccount.ID, adaAccount, false))
	saved := daemonSignInsFor(t, cp.srv.URL)[adaAccount.ID]
	require.NotNil(t, saved)
	require.Equal(t, adaAccount.ID, saved.AccountID)
	require.Equal(t, adaAccount.Email, saved.AccountEmail)
}
