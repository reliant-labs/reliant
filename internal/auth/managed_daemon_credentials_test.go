// Copyright (c) 2025 Reliant Labs

package auth

import (
	"os"
	"path/filepath"
	"testing"
)

// TestManagedDaemonCredentialsFileIsAccepted pins the file control-plane
// mounts into a managed daemon pod against THIS package's reader.
//
// ## Why a literal fixture rather than an import
//
// control-plane's internal/daemonpat renders these bytes and mounts them at
// $HOME/.reliant/daemon.json (subPath) for every dial-out managed daemon.
// Go forbids importing another module's `internal` package, and reliant does
// not depend on control-plane anyway (the dependency runs the other way), so
// neither repo can call the other's code in a test. The JSON below is
// therefore a hand-mirrored copy of daemonpat.BuildCredentialsJSON's output —
// the same arrangement auth/daemoninstance parity and electron/daemon-creds.js
// already use, for the same reason.
//
// ## What it is defending
//
// This reader is strict by design: readStore discards ANY document whose
// `origins` member is absent, because the format changed several times
// pre-launch and carrying stale entries forward is worse than starting clean
// (see TestReadStore_OldFormatIsDiscarded). That strictness is correct, and it
// means the producer has no margin for error — a near-miss reads as an EMPTY
// store, not as a partial one.
//
// It cost a production outage on 2026-09-30. daemonpat wrote a FLAT
// origin -> credential map, exactly the superseded shape
// TestReadStore_OldFormatIsDiscarded pins as "must not be surfaced". So the
// managed daemon read no credentials at all, decided it was unregistered, and
// fell through to INTERACTIVE browser OAuth inside a headless pod: it printed
// an authorize URL into its container log, waited 5 minutes for a browser that
// does not exist, exited, and crash-looped. Because it never connected,
// nothing refreshed the Workspace's lastActivity, so the idle detector
// suspended the workspace 30 minutes after resume. The owner's UI said only
// "your compute is taking a while to come up" for the entire window.
//
// If this test fails, managed daemons in every cloud environment cannot
// authenticate. Fix the producer to match; do not loosen the reader.
func TestManagedDaemonCredentialsFileIsAccepted(t *testing.T) {
	const (
		serverURL  = "https://api.reliantapi.com"
		gatewayURL = "https://gateway.reliantapi.com"
		pat        = "rlat_managedcontracttoken"
	)

	// Byte-for-byte the shape daemonpat.BuildCredentialsJSON emits: nested
	// origins (origin -> account -> credential) plus the per-origin default
	// account, with no daemon_id on disk.
	const mounted = `{
  "origins": {
    "https://api.reliantapi.com": {
      "_default": {
        "pat": "rlat_managedcontracttoken",
        "server_url": "https://api.reliantapi.com",
        "gateway_url": "https://gateway.reliantapi.com",
        "registered_at": "2026-09-30T19:01:32Z"
      }
    }
  },
  "default_accounts": {
    "https://api.reliantapi.com": "_default"
  }
}`

	seedDaemonFile(t, mounted)

	// A managed pod passes no --account, so the daemon resolves the empty
	// subject. This is the exact call `daemon start` makes.
	got, err := ReadDaemonCredentials(serverURL, "")
	if err != nil {
		t.Fatalf("ReadDaemonCredentials: %v", err)
	}
	if got == nil {
		t.Fatal("read NO credentials from the file control-plane mounts — " +
			"`daemon start` would treat this pod as unregistered and fall " +
			"through to interactive browser OAuth, which cannot complete in a pod")
	}
	if got.PAT != pat {
		t.Errorf("PAT = %q, want %q", got.PAT, pat)
	}
	if got.ServerURL != serverURL {
		t.Errorf("ServerURL = %q, want %q", got.ServerURL, serverURL)
	}
	if got.GatewayURL != gatewayURL {
		t.Errorf("GatewayURL = %q, want %q", got.GatewayURL, gatewayURL)
	}
}

// TestManagedDaemonCredentialsFlatMapIsRejected states the failure mode
// explicitly, so the regression is legible as a test name rather than only as
// a comment on the happy path.
//
// This is the shape that shipped to prod. Asserting it reads back as nil keeps
// the reader honest too: a future "be lenient about old formats" change would
// mask a producer bug instead of surfacing it, and leniency here means loading
// a credential whose account the store never recorded.
func TestManagedDaemonCredentialsFlatMapIsRejected(t *testing.T) {
	const flat = `{
  "https://api.reliantapi.com": {
    "pat": "rlat_flatshape",
    "server_url": "https://api.reliantapi.com",
    "gateway_url": "https://gateway.reliantapi.com",
    "daemon_id": "cda8a89b-d15c-455d-a506-4c40335e4278",
    "registered_at": "2026-09-30T19:01:32Z"
  }
}`

	seedDaemonFile(t, flat)

	got, err := ReadDaemonCredentials("https://api.reliantapi.com", "")
	if err != nil {
		t.Fatalf("ReadDaemonCredentials: %v", err)
	}
	if got != nil {
		t.Fatalf("the flat pre-launch shape must not resolve (it records no account), got %+v", got)
	}
}

// seedDaemonFile writes contents to $HOME/.reliant/daemon.json in a temp HOME,
// which is where the pod's subPath mount lands it.
func seedDaemonFile(t *testing.T, contents string) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)

	path, err := DaemonCredentialsFilePath()
	if err != nil {
		t.Fatalf("DaemonCredentialsFilePath: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
}
