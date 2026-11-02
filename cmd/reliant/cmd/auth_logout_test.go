// Copyright (c) 2025 Reliant Labs
package cmd

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/cliauth"
	"github.com/reliant-labs/reliant/internal/daemoninstance"
	"github.com/reliant-labs/reliant/internal/toolexec/daemonstate"
)

// isolateLogout is isolateCLI for a desktop: a cloud machine running these
// tests exports RELIANT_DAEMON_TYPE=managed, which logout must honour.
func isolateLogout(t *testing.T) {
	t.Helper()
	isolateCLI(t)
	t.Setenv("RELIANT_DAEMON_TYPE", "")
}

func writeDaemonSignIn(t *testing.T, server, sub string) {
	t.Helper()
	require.NoError(t, auth.WriteDaemonCredentials(&auth.DaemonCredentials{
		PAT: "rlat_DAEMON_" + sub, ServerURL: server, Sub: sub, RegisteredAt: time.Now().UTC(),
	}))
}

func requireNoDaemonSignIn(t *testing.T, server string) {
	t.Helper()
	creds, err := auth.ListDaemonCredentials()
	require.NoError(t, err)
	for _, c := range creds {
		require.NotEqual(t, daemoninstance.OriginKey(server), daemoninstance.OriginKey(c.ServerURL),
			"daemon sign-in for %s (account %s) survived logout", c.ServerURL, c.Sub)
	}
}

// `reliant auth logout` signs the MACHINE out. A new user onboarding on a
// machine that still held the previous user's daemon credential ran logout,
// was told "Not logged in" (the CLI had no login), and `daemon start` then
// connected silently as the previous account — no pairing code for the new
// user's Add machine sheet, which waited forever.
func TestAuthLogout_RemovesTheDaemonSignInToo(t *testing.T) {
	const other = "http://localhost:9191"

	t.Run("daemon sign-in only", func(t *testing.T) {
		isolateLogout(t)
		writeDaemonSignIn(t, testServerURL, "user-previous")
		writeDaemonSignIn(t, testServerURL, "user-older")
		writeDaemonSignIn(t, other, "user-previous")

		out, err := executeRoot(t, "auth", "logout", "--server", testServerURL)
		require.NoError(t, err, out)
		require.Equal(t, "Logged out of "+testServerURL+"\n"+
			"  Removed this machine's daemon sign-ins (2 accounts).\n"+
			"  The next 'reliant daemon start' prints a pairing code to approve in Reliant.\n", out)
		requireNoDaemonSignIn(t, testServerURL)
		kept, err := auth.ReadDaemonCredentials(other, "user-previous")
		require.NoError(t, err)
		require.NotNil(t, kept, "logout of one server removed another server's daemon sign-in")
	})

	t.Run("CLI login and daemon sign-in", func(t *testing.T) {
		isolateLogout(t)
		loginFor(t, testServerURL, "rlat_CLILOGIN")
		writeDaemonSignIn(t, testServerURL, "user-previous")

		out, err := executeRoot(t, "auth", "logout", "--server", testServerURL)
		require.NoError(t, err, out)
		require.Equal(t, "Logged out of "+testServerURL+"\n"+
			"  Removed the CLI login and this machine's daemon sign-in.\n"+
			"  The next 'reliant daemon start' prints a pairing code to approve in Reliant.\n", out)
		requireNoDaemonSignIn(t, testServerURL)
		_, _, err = cliauth.Lookup(testServerURL)
		require.Error(t, err, "the CLI login survived logout")
	})

	t.Run("neither", func(t *testing.T) {
		isolateLogout(t)
		out, err := executeRoot(t, "auth", "logout", "--server", testServerURL)
		require.NoError(t, err, out)
		require.Equal(t, "Not logged in to "+testServerURL+"\n", out)
	})
}

// A daemon already connected keeps its stream after the credential it read is
// gone, and logout cannot pretend otherwise. It does not stop it either — it
// names it, with a stop command that still reaches it: a no-flag `daemon stop`
// would resolve its instance through the credential just removed.
func TestAuthLogout_NamesADaemonStillRunning(t *testing.T) {
	isolateLogout(t)
	writeDaemonSignIn(t, testServerURL, "user-previous")

	key, err := daemoninstance.Resolve(testServerURL, "user-previous", "workspace-a")
	require.NoError(t, err)
	dataDir, err := key.EnsureDataDir()
	require.NoError(t, err)
	lock, err := daemonstate.Acquire(dataDir)
	require.NoError(t, err)
	t.Cleanup(func() { _ = lock.Release() })

	// Another server's live daemon is not this logout's business.
	otherKey, err := daemoninstance.Resolve("http://localhost:9191", "user-previous", "workspace-a")
	require.NoError(t, err)
	otherDir, err := otherKey.EnsureDataDir()
	require.NoError(t, err)
	otherLock, err := daemonstate.Acquire(otherDir)
	require.NoError(t, err)
	t.Cleanup(func() { _ = otherLock.Release() })

	out, err := executeRoot(t, "auth", "logout", "--server", testServerURL)
	require.NoError(t, err, out)
	require.Contains(t, out, "A daemon is still running and stays connected as the signed-out account. Stop it with:\n"+
		"    reliant daemon stop --data-dir "+shellQuote(dataDir)+"\n")
	require.NotContains(t, out, otherDir)
	require.True(t, instanceLocked(t, dataDir), "logout must not stop a running daemon")
}

func instanceLocked(t *testing.T, dataDir string) bool {
	t.Helper()
	locked, err := daemonstate.ProbeLocked(dataDir)
	require.NoError(t, err)
	return locked
}

// On a Reliant cloud machine the daemon credential is the platform's mounted
// Secret: logout leaves it, and says so.
func TestAuthLogout_LeavesACloudMachinesDaemonSignIn(t *testing.T) {
	isolateLogout(t)
	t.Setenv("RELIANT_DAEMON_TYPE", "managed")
	loginFor(t, testServerURL, "rlat_CLILOGIN")
	writeDaemonSignIn(t, testServerURL, "user-owner")

	out, err := executeRoot(t, "auth", "logout", "--server", testServerURL)
	require.NoError(t, err, out)
	require.Equal(t, "Logged out of "+testServerURL+"\n"+
		"  Removed the CLI login.\n"+
		"  This is a Reliant cloud machine: its daemon keeps the sign-in Reliant manages for it.\n", out)
	kept, err := auth.ReadDaemonCredentials(testServerURL, "user-owner")
	require.NoError(t, err)
	require.NotNil(t, kept, "logout removed a cloud machine's daemon credential")
}

func TestShellQuote(t *testing.T) {
	require.Equal(t, "/home/a/.reliant/instances/x", shellQuote("/home/a/.reliant/instances/x"))
	require.Equal(t, "'/Users/A B/.reliant'", shellQuote("/Users/A B/.reliant"))
	require.Equal(t, `'it'\''s'`, shellQuote("it's"))
	require.Equal(t, "''", shellQuote(""))
}

// A saved credential prints no pairing code, so `daemon start` says it is
// using one, and how to sign in as someone else — the line that would have
// told the new user their Add machine sheet was never going to get a code.
func TestSavedSignInNotice(t *testing.T) {
	saved := time.Date(2026, 9, 12, 12, 0, 0, 0, time.Local)
	notice := savedSignInNotice(&auth.DaemonCredentials{RegisteredAt: saved},
		&connection{ServerURL: testServerURL, ServerSource: sourceFlag})
	require.Equal(t, "Using the daemon sign-in saved on this machine on 12 Sep 2026, so there is no pairing code to approve.\n"+
		"Not you? Run 'reliant auth logout --server "+testServerURL+"', then 'reliant daemon start --server "+testServerURL+"' again.\n", notice)

	notice = savedSignInNotice(&auth.DaemonCredentials{}, &connection{ServerURL: testServerURL})
	require.True(t, strings.HasPrefix(notice, "Using the daemon sign-in saved on this machine, so"), notice)
	require.Contains(t, notice, "Not you? Run 'reliant auth logout', then 'reliant daemon start' again.")
}
