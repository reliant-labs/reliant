// Copyright (c) 2025 Reliant Labs
package commands

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/spf13/cobra"

	"github.com/reliant-labs/reliant/internal/auth"
)

// TestEnsureDaemonCredentials_ReadOnlyFileTolerated simulates the managed-daemon
// dial-out mode: credentials arrive via a read-only mounted file and the gateway
// URL has drifted (RELIANT_GATEWAY_URL differs from what's persisted), which
// triggers the drift-rewrite path. On a read-only mount the persist must fail
// best-effort and the daemon must still boot with the provided credentials.
func TestEnsureDaemonCredentials_ReadOnlyFileTolerated(t *testing.T) {
	const serverURL = "https://staging.reliantapi.com"

	// Redirect ~/.reliant to a temp dir we control.
	home := t.TempDir()
	t.Setenv("HOME", home)
	if runtime.GOOS == "windows" {
		t.Setenv("USERPROFILE", home)
	}

	reliantDir := filepath.Join(home, ".reliant")
	if err := os.MkdirAll(reliantDir, 0o700); err != nil {
		t.Fatalf("creating .reliant dir: %v", err)
	}

	// Seed a valid credentials file as if mounted from a Secret. Note the
	// gateway URL deliberately differs from the one we pass in below so the
	// drift-rewrite branch fires.
	seed := &auth.DaemonCredentials{
		PAT:        "rlnt_pat_seeded-managed-token",
		ServerURL:  serverURL,
		GatewayURL: "https://gateway-staging.reliantapi.com",
	}
	if err := auth.WriteDaemonCredentials(seed); err != nil {
		t.Fatalf("seeding daemon credentials: %v", err)
	}

	// Make both the file and its directory read-only so the rewrite of
	// daemon.json fails with a permission error — the signature of a read-only
	// mounted Secret. The file mode matters: rewriting an existing file with an
	// otherwise-writable mode would succeed even under a read-only dir.
	credsPath, err := auth.DaemonCredentialsFilePath()
	if err != nil {
		t.Fatalf("resolving creds path: %v", err)
	}
	if err := os.Chmod(credsPath, 0o400); err != nil {
		t.Fatalf("chmod creds file read-only: %v", err)
	}
	if err := os.Chmod(reliantDir, 0o500); err != nil {
		t.Fatalf("chmod read-only: %v", err)
	}
	t.Cleanup(func() { // allow TempDir cleanup
		_ = os.Chmod(reliantDir, 0o700)
		_ = os.Chmod(credsPath, 0o600)
	})

	cmd := &cobra.Command{}
	cmd.SetOut(os.Stderr)

	// Pass a different gateway URL to force the drift-rewrite (persist) path.
	const driftedGateway = "https://gateway-staging.reliantapi.com:8443"
	target := &connection{ServerURL: serverURL, GatewayURL: driftedGateway}
	creds, err := ensureDaemonCredentials(context.Background(), cmd, target, false)
	if err != nil {
		t.Fatalf("ensureDaemonCredentials returned error on read-only creds file: %v", err)
	}
	if creds == nil {
		t.Fatal("expected non-nil credentials")
	}
	if creds.PAT != seed.PAT {
		t.Fatalf("expected PAT %q, got %q", seed.PAT, creds.PAT)
	}
	// In-memory creds reflect the drifted gateway even though the persist was skipped.
	if creds.GatewayURL != driftedGateway {
		t.Fatalf("expected in-memory gateway %q, got %q", driftedGateway, creds.GatewayURL)
	}
}

// TestPersistDaemonCredentials_ReadOnlyDoesNotPanic exercises the best-effort
// persist helper directly against a read-only credentials directory.
func TestPersistDaemonCredentials_ReadOnlyDoesNotPanic(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	if runtime.GOOS == "windows" {
		t.Setenv("USERPROFILE", home)
	}

	reliantDir := filepath.Join(home, ".reliant")
	if err := os.MkdirAll(reliantDir, 0o700); err != nil {
		t.Fatalf("creating .reliant dir: %v", err)
	}
	if err := os.Chmod(reliantDir, 0o500); err != nil {
		t.Fatalf("chmod read-only: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(reliantDir, 0o700) })

	// Must not panic and must return (void) even though the write fails.
	persistDaemonCredentials(&auth.DaemonCredentials{
		PAT:       "rlnt_pat_x",
		ServerURL: "https://staging.reliantapi.com",
	})
}
