// Copyright (c) 2026 Reliant Labs

package daemonpolicy_test

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/reliant-labs/reliant/internal/daemon"
	"github.com/reliant-labs/reliant/internal/daemonpolicy"
)

// A file the chat composer sent to the machine's uploads folder
// (~/.reliant/uploads/<chat>/<name>, outside the worktree) must stay readable
// by the agent: its file tools refuse only the credential files, and a
// credentials-none shell hides only the protected stores. This pins that the
// uploads folder is neither, so a later addition to either list cannot take
// the agent's uploaded files away silently.
func TestUserUploadsAreReadableByTheAgent(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the home directory is not $HOME on Windows")
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	daemonpolicy.SetDaemonDataDir(filepath.Join(home, ".reliant", "instances", "o", "s", "w"))
	t.Cleanup(func() { daemonpolicy.SetDaemonDataDir("") })

	uploads, err := daemon.UserUploadsRoot()
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(home, ".reliant", "uploads"); uploads != want {
		t.Fatalf("UserUploadsRoot = %q, want %q", uploads, want)
	}
	file := filepath.Join(uploads, "chat-1", "report.xlsx")
	if err := os.MkdirAll(filepath.Dir(file), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, []byte("PK"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := daemonpolicy.DenyCredentialFile(file); err != nil {
		t.Fatalf("the file tools refuse an uploaded file: %v", err)
	}
	if _, err := daemonpolicy.ResolveFile(t.Context(), file); err != nil {
		t.Fatalf("an unconfined caller cannot resolve an uploaded file: %v", err)
	}
	for _, c := range append(daemonpolicy.ProtectedCredentials(), daemonpolicy.DeprecatedReadableCredentials()...) {
		if c.Path == file || c.Path == uploads || (c.Dir && strings.HasPrefix(file, c.Path+string(filepath.Separator))) {
			t.Errorf("a credentials-none shell would hide the uploaded file: %s is protected", c.Path)
		}
	}
}
