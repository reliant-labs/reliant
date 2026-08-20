// Copyright (c) 2025 Reliant Labs
package commands

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/spf13/cobra"

	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/toolexec/daemonstate"
)

// withTempReliantHome redirects ~/.reliant (where daemon credentials and the
// auth session live) to a fresh temp dir, isolating the test from any real
// credentials on the machine running it.
func withTempReliantHome(t *testing.T) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	if runtime.GOOS == "windows" {
		t.Setenv("USERPROFILE", home)
	}
}

// TestRegisterDaemonNonInteractiveNeverOpensBrowser is the reproduction for
// the daemon side of the prod bug: with no auth session on disk and
// nonInteractive=true, registerDaemon must return
// auth.ErrNonInteractiveLoginRequired instead of falling into auth.Login's
// interactive flow, which would open a browser and start a local HTTP server.
func TestRegisterDaemonNonInteractiveNeverOpensBrowser(t *testing.T) {
	withTempReliantHome(t)

	cmd := &cobra.Command{}
	cmd.SetOut(os.Stderr)
	conn := &connection{ServerURL: "https://staging.reliantapi.com"}

	err := registerDaemon(context.Background(), cmd, conn, true)
	if err == nil {
		t.Fatal("registerDaemon(nonInteractive=true) returned nil error, want ErrNonInteractiveLoginRequired wrapped")
	}
	if !errors.Is(err, auth.ErrNonInteractiveLoginRequired) {
		t.Fatalf("registerDaemon(nonInteractive=true) error = %v, want it to wrap ErrNonInteractiveLoginRequired", err)
	}
}

// TestResolveOrAwaitCredentials_IdlesAndPicksUpCredentials exercises the crux
// of the daemon-side fix: with no credentials on disk and nonInteractive=true,
// resolveOrAwaitCredentials must NOT return an error or exit — it must publish
// daemonstate.StreamAwaitingCredentials and then keep polling disk until a
// credential appears (simulating Electron's pre-mint path writing
// ~/.reliant/daemon.json), at which point it returns that credential.
func TestResolveOrAwaitCredentials_IdlesAndPicksUpCredentials(t *testing.T) {
	withTempReliantHome(t)
	t.Setenv("RELIANT_AUTH_URL", "") // ensure ReadAccessTokenFromAuthFile sees no session

	oldInterval := daemonCredentialPollInterval
	daemonCredentialPollInterval = 20 * time.Millisecond
	t.Cleanup(func() { daemonCredentialPollInterval = oldInterval })

	dataDir := t.TempDir()
	cmd := &cobra.Command{}
	cmd.SetOut(os.Stderr)
	const serverURL = "https://staging.reliantapi.com"
	conn := &connection{ServerURL: serverURL}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	resultCh := make(chan struct {
		creds *auth.DaemonCredentials
		err   error
	}, 1)
	go func() {
		creds, err := resolveOrAwaitCredentials(ctx, cmd, conn, dataDir, true)
		resultCh <- struct {
			creds *auth.DaemonCredentials
			err   error
		}{creds, err}
	}()

	// Give the goroutine a moment to reach the idle loop and publish state.
	deadline := time.Now().Add(2 * time.Second)
	for {
		state, err := daemonstate.Read(dataDir)
		if err == nil && state.Stream == daemonstate.StreamAwaitingCredentials {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("daemon never published StreamAwaitingCredentials in %s (last state=%+v err=%v)", dataDir, state, err)
		}
		time.Sleep(10 * time.Millisecond)
	}

	// Now simulate Electron's pre-mint path: write a usable daemon credential
	// for this origin.
	seeded := &auth.DaemonCredentials{
		PAT:       "rlnt_pat_electron-preminted",
		ServerURL: serverURL,
	}
	if err := auth.WriteDaemonCredentials(seeded); err != nil {
		t.Fatalf("seeding daemon credentials: %v", err)
	}

	select {
	case result := <-resultCh:
		if result.err != nil {
			t.Fatalf("resolveOrAwaitCredentials returned error: %v", result.err)
		}
		if result.creds == nil || result.creds.PAT != seeded.PAT {
			t.Fatalf("resolveOrAwaitCredentials returned %+v, want PAT %q", result.creds, seeded.PAT)
		}
	case <-ctx.Done():
		t.Fatal("resolveOrAwaitCredentials did not pick up the seeded credential before the test deadline")
	}
}

// TestWaitForCredentialsNonInteractive_RespectsCancellation ensures the idle
// loop actually returns (rather than blocking forever) when ctx is cancelled
// — the daemon must stay responsive to SIGINT/SIGTERM while idling.
func TestWaitForCredentialsNonInteractive_RespectsCancellation(t *testing.T) {
	withTempReliantHome(t)

	oldInterval := daemonCredentialPollInterval
	daemonCredentialPollInterval = 20 * time.Millisecond
	t.Cleanup(func() { daemonCredentialPollInterval = oldInterval })

	dataDir := t.TempDir()
	cmd := &cobra.Command{}
	cmd.SetOut(os.Stderr)
	conn := &connection{ServerURL: "https://staging.reliantapi.com"}

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()

	done := make(chan error, 1)
	go func() {
		_, err := waitForCredentialsNonInteractive(ctx, cmd, conn, dataDir)
		done <- err
	}()

	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("waitForCredentialsNonInteractive returned %v, want context.Canceled", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("waitForCredentialsNonInteractive did not return after ctx cancellation")
	}

	if p := filepath.Join(dataDir, daemonstate.FileName); true {
		state, err := daemonstate.Read(dataDir)
		if err != nil {
			t.Fatalf("reading daemon state at %s: %v", p, err)
		}
		if state.Stream != daemonstate.StreamAwaitingCredentials {
			t.Fatalf("daemon state stream = %q, want %q", state.Stream, daemonstate.StreamAwaitingCredentials)
		}
	}
}
