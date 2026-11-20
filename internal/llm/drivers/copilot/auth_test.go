// Copyright (c) 2025 Reliant Labs
package copilot

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/reliant-labs/reliant/internal/llm"
)

// TestResolveGitHubTokenIgnoresProcessCredentials pins that a Copilot driver
// built with no credential of its own never borrows the process's: neither the
// GITHUB_TOKEN env var nor the GitHub Copilot config files under the home or
// XDG config dir. Driver construction runs on the multi-tenant server roles,
// where either would run one tenant's request on the operator's account.
func TestResolveGitHubTokenIgnoresProcessCredentials(t *testing.T) {
	const sentinel = "gho_PROCESS_CREDENTIAL_MUST_NOT_BE_USED"
	home := t.TempDir()
	cfg := filepath.Join(home, ".config")
	dir := filepath.Join(cfg, "github-copilot")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	hosts := `{"github.com":{"oauth_token":"` + sentinel + `"}}`
	for _, name := range []string{"hosts.json", "apps.json"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(hosts), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", cfg)
	t.Setenv("LOCALAPPDATA", cfg)
	t.Setenv("GITHUB_TOKEN", sentinel)
	t.Setenv("GH_TOKEN", sentinel)

	got, err := resolveGitHubToken(llm.DriverOptions{})
	if !errors.Is(err, errNoCopilotCredential) {
		t.Fatalf("resolveGitHubToken with no credential = (%q, %v), want errNoCopilotCredential", got, err)
	}

	if _, err := NewClient(llm.DriverOptions{}); !errors.Is(err, errNoCopilotCredential) {
		t.Fatalf("NewClient with no credential: err = %v, want errNoCopilotCredential", err)
	}

	got, err = resolveGitHubToken(llm.DriverOptions{ApiKey: " gho_account "})
	if err != nil || got != "gho_account" {
		t.Fatalf("resolveGitHubToken(ApiKey) = (%q, %v), want the account's token", got, err)
	}
	got, err = resolveGitHubToken(llm.DriverOptions{ApiKey: "gho_key", BearerToken: "gho_bearer"})
	if err != nil || got != "gho_bearer" {
		t.Fatalf("resolveGitHubToken(BearerToken) = (%q, %v), want the bearer token first", got, err)
	}
}
