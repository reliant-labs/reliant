// Copyright (c) 2025 Reliant Labs
package commands

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/spf13/cobra"

	"github.com/reliant-labs/forge/pkg/cloudcred"
	"github.com/reliant-labs/forge/pkg/credentials"

	"github.com/reliant-labs/reliant/internal/toolexec/daemonruntime"
)

// ONE LOGIN ON A CLOUD DAEMON — the population the Electron fix did not cover.
//
// A managed workspace pod is the third mint path, and the least like the other
// two. Its PAT is minted by the CONTROL PLANE and handed over as a Kubernetes
// Secret mounted READ-ONLY at ~/.reliant/daemon.json (verified on the owner's
// live prod daemon: volume `daemon-pat` → secret daemon-pat-ws-<id>,
// readOnly: true, with RELIANT_DAEMON_TYPE=managed in the pod env). So:
//
//   - registerDaemon never runs. There is no browser in a pod, and
//     daemonNonInteractiveDefault() forces non-interactive for a managed
//     environment anyway — so the deposit that lived only in registration
//     could never fire here.
//   - the credential file CANNOT be written. Anything that treats persisting
//     as a precondition for depositing fails on EROFS.
//
// Without a deposit, an agent or a terminal on a cloud daemon has no way to
// authenticate forge at all: `forge login` needs a browser loopback a pod does
// not have, and the Deploy button's re-exec inherits only the daemon's
// environment. That is the "a separate forge login was needed" report.
//
// These tests pin the managed shape specifically, because every existing test
// in this package writes the credential file itself and so proves nothing
// about a read-only mount.

// mountedDaemonSecret writes the credential store EXACTLY as control-plane's
// daemonpat renders it into the Secret — nested origins → account → creds,
// which is the shape confirmed on the live prod Secret — and then makes the
// file read-only, the way a Secret mount presents it.
//
// Written through the store format rather than auth.WriteDaemonCredentials on
// purpose: the point is that nothing in reliant wrote this file.
func mountedDaemonSecret(t *testing.T, home, serverURL, gatewayURL, pat string) {
	t.Helper()
	store := map[string]any{
		"origins": map[string]any{
			serverURL: map[string]any{
				"_default": map[string]any{
					"pat":           pat,
					"server_url":    serverURL,
					"gateway_url":   gatewayURL,
					"registered_at": "2026-10-01T00:00:00Z",
				},
			},
		},
		"default_accounts": map[string]string{serverURL: "_default"},
	}
	data, err := json.MarshalIndent(store, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(home, ".reliant", "daemon.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	// A Secret mount is read-only. Any write attempt against it must be
	// tolerated rather than fatal.
	if err := os.Chmod(path, 0o400); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(path, 0o600) })
}

// isolateManagedDaemonHome is isolateDaemonHome plus the pod's own marker, so
// the code under test takes the managed branch (non-interactive by force).
func isolateManagedDaemonHome(t *testing.T) (credPath, home string) {
	t.Helper()
	home = t.TempDir()
	t.Setenv("HOME", home)
	if runtime.GOOS == "windows" {
		t.Setenv("USERPROFILE", home)
	}
	t.Setenv("FORGE_HOME", home)
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv(daemonruntime.DaemonTypeEnvVar, "managed")
	return filepath.Join(home, credentials.FileName), home
}

// TestCloudDaemonStart_MountedSecretLogsForgeIn is the owner's report for the
// cloud case: a managed pod boots on a credential it did not mint and cannot
// write, and `forge` inside that pod must still resolve one for the control
// plane with no login.
func TestCloudDaemonStart_MountedSecretLogsForgeIn(t *testing.T) {
	credPath, home := isolateManagedDaemonHome(t)
	const controlPlane = "https://admin.reliantapi.com"
	api := fakeAPIServer(t, controlPlane)
	const pat = "rlat_CLOUDMOUNTEDPAT0"

	mountedDaemonSecret(t, home, api.URL, "https://gateway.reliantapi.com", pat)

	// Precondition: this really is the managed branch, so nothing can have
	// gone down an interactive registration.
	if !daemonruntime.IsManagedEnvironment() {
		t.Fatal("precondition: the pod marker must put this on the managed path")
	}

	conn := &connection{ServerURL: api.URL}
	cmd := &cobra.Command{}
	cmd.SetOut(io.Discard)

	creds, err := resolveOrAwaitCredentials(context.Background(), cmd, conn, "",
		t.TempDir(), daemonNonInteractiveDefault())
	if err != nil {
		t.Fatalf("a managed daemon failed to resolve its mounted credential: %v", err)
	}
	if creds.PAT != pat {
		t.Fatalf("daemon resolved PAT %q, want the mounted one", creds.PAT)
	}

	got, err := credentials.Lookup(credPath, controlPlane, cloudcred.HostClientID)
	if err != nil {
		t.Fatalf("forge is logged out of %s on a cloud daemon — this is the second `forge login` the owner had to run: %v",
			controlPlane, err)
	}
	if got.Token != pat {
		t.Errorf("forge holds token %q, want the daemon's mounted PAT", got.Token)
	}
}

// TestCloudDaemonStart_ReadOnlyCredentialFileStillDeposits: the deposit must
// not be coupled to persisting. A Secret mount is read-only, so a deposit
// gated on a successful credential-file write would never happen in a pod.
func TestCloudDaemonStart_ReadOnlyCredentialFileStillDeposits(t *testing.T) {
	credPath, home := isolateManagedDaemonHome(t)
	const controlPlane = "https://admin.reliantapi.com"
	api := fakeAPIServer(t, controlPlane)
	const pat = "rlat_READONLYMOUNT000"

	mountedDaemonSecret(t, home, api.URL, "", pat)

	// A gateway URL that differs from the mounted one is what drives
	// ensureDaemonCredentials to try to persist the drift — on a read-only
	// file. The boot, and the deposit, must both survive it.
	conn := &connection{ServerURL: api.URL, GatewayURL: "https://gateway.reliantapi.com"}
	cmd := &cobra.Command{}
	cmd.SetOut(io.Discard)

	if _, err := resolveOrAwaitCredentials(context.Background(), cmd, conn, "",
		t.TempDir(), daemonNonInteractiveDefault()); err != nil {
		t.Fatalf("a read-only credential mount must not fail the boot: %v", err)
	}

	if _, err := credentials.Lookup(credPath, controlPlane, cloudcred.HostClientID); err != nil {
		t.Fatalf("the deposit did not survive a read-only credential mount: %v", err)
	}
}

// TestCloudDaemonStart_DepositIsKeyedByTheControlPlane: in prod the API server
// and the control plane are DIFFERENT origins (api. vs admin.). forge looks up
// the control plane, so a deposit under the API origin is one forge never
// reads — signed in, and still told to log in.
func TestCloudDaemonStart_DepositIsKeyedByTheControlPlane(t *testing.T) {
	credPath, home := isolateManagedDaemonHome(t)
	const controlPlane = "https://admin.reliantapi.com"
	api := fakeAPIServer(t, controlPlane)

	mountedDaemonSecret(t, home, api.URL, "", "rlat_CLOUDKEYEDBYISS0")

	conn := &connection{ServerURL: api.URL}
	cmd := &cobra.Command{}
	cmd.SetOut(io.Discard)
	if _, err := resolveOrAwaitCredentials(context.Background(), cmd, conn, "",
		t.TempDir(), daemonNonInteractiveDefault()); err != nil {
		t.Fatal(err)
	}

	if _, err := credentials.Lookup(credPath, api.URL, cloudcred.HostClientID); err == nil {
		t.Error("the deposit landed under the API server origin; forge never looks there")
	}
	if _, err := credentials.Lookup(credPath, controlPlane, cloudcred.HostClientID); err != nil {
		t.Errorf("nothing was deposited for the control plane: %v", err)
	}
}
