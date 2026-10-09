// Copyright (c) 2025 Reliant Labs
package daemonruntime

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

const testSecretToken = "ghu_TESTSECRET0123456789abcdefghijklmn"

// A clone made with a token must not leave it in the checkout: it used to be
// spliced into the clone URL, which git persists as remote.origin.url.
func TestHandleGitClone_TokenNeverLandsInGitConfig(t *testing.T) {
	if testing.Short() {
		t.Skip("-short: runs real git clone; skipped under -short")
	}
	setGitCloneTestEnv(t)
	t.Setenv("HOME", t.TempDir())
	origin := newTestOriginRepo(t)
	dest := filepath.Join(t.TempDir(), "dest")

	payload, err := json.Marshal(gitCloneRequest{Repo: origin, Branch: "main", Path: dest, Token: testSecretToken})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := handleGitClone(context.Background(), payload); err != nil {
		t.Fatalf("clone: %v", err)
	}

	cfg, err := os.ReadFile(filepath.Join(dest, ".git", "config"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(cfg), testSecretToken) || strings.Contains(string(cfg), "x-access-token") {
		t.Fatalf("token leaked into .git/config:\n%s", cfg)
	}
	out, err := exec.Command("git", "-C", dest, "remote", "get-url", "origin").Output()
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(string(out)); got != origin {
		t.Fatalf("origin url = %q, want the clean %q", got, origin)
	}
}

// The one-shot helper must hand git the token for HTTPS, and nothing from
// inherited helpers.
func TestCloneCredentialEnv_SuppliesTokenToGit(t *testing.T) {
	if testing.Short() {
		t.Skip("-short: runs real git credential; skipped under -short")
	}
	if cloneCredentialEnv("") != nil {
		t.Fatal("no token must yield no credential env")
	}
	setGitCloneTestEnv(t)
	cmd := exec.Command("git", "credential", "fill")
	cmd.Env = append(os.Environ(), cloneCredentialEnv(testSecretToken)...)
	cmd.Stdin = strings.NewReader("protocol=https\nhost=github.com\n\n")
	var stdout bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stdout
	if err := cmd.Run(); err != nil {
		t.Fatalf("git credential fill: %v: %s", err, stdout.String())
	}
	got := stdout.String()
	if !strings.Contains(got, "username=x-access-token") || !strings.Contains(got, "password="+testSecretToken) {
		t.Fatalf("helper did not supply the token:\n%s", got)
	}
}

// The store helper returns the FIRST matching line, so a refreshed token has to
// replace the old one rather than queue behind it.
func TestUpsertGitCredential_ReplacesStaleTokenKeepsOthers(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	credFile := filepath.Join(home, ".git-credentials")
	seed := "https://x-access-token:OLD@github.com\nhttps://alice:pw@gitlab.com\nhttps://x-access-token:OLDLAB@gitlab.com\n"
	if err := os.WriteFile(credFile, []byte(seed), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := upsertGitCredential("github.com", "NEW"); err != nil {
		t.Fatal(err)
	}
	if err := upsertGitCredential("github.com", "NEWER"); err != nil {
		t.Fatal(err)
	}

	got, err := os.ReadFile(credFile)
	if err != nil {
		t.Fatal(err)
	}
	want := "https://alice:pw@gitlab.com\nhttps://x-access-token:OLDLAB@gitlab.com\nhttps://x-access-token:NEWER@github.com\n"
	if string(got) != want {
		t.Fatalf("credentials file:\n%s\nwant:\n%s", got, want)
	}
	if info, err := os.Stat(credFile); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %v, err = %v; want 0600", info.Mode().Perm(), err)
	}
}

func TestUpsertGitCredential_CreatesMissingFile(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	if err := upsertGitCredential("github.com", "T"); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(filepath.Join(home, ".git-credentials"))
	if string(got) != "https://x-access-token:T@github.com\n" {
		t.Fatalf("got %q", got)
	}
}
