// Copyright (c) 2025 Reliant Labs
package commands

import (
	"strings"
	"testing"
	"time"

	"github.com/reliant-labs/forge/pkg/credentials"

	"github.com/reliant-labs/reliant/internal/cliauth"
)

// isolateCLI points HOME and the shared credentials file at a temp dir and
// clears the RELIANT_* variables the resolver reads, so no test can read or
// write a developer's real login. A shell that has sourced .dev-ports.sh
// exports RELIANT_SERVER_URL, which outranks the compiled defaults tests
// assert on; an empty value reads as unset. Returns the credentials path.
func isolateCLI(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("FORGE_HOME", home)
	t.Setenv(envServerURL, "")
	t.Setenv(envGatewayURL, "")
	t.Setenv(envToken, "")
	path, err := cliauth.CredentialsPath()
	if err != nil || !strings.HasPrefix(path, home) {
		t.Fatalf("credentials path %q escaped temp HOME %q — aborting to protect the real file", path, home)
	}
	return path
}

// loginFor stores token as the reliant CLI's login for server, as
// `reliant auth login --server <server>` would.
func loginFor(t *testing.T, server, token string) {
	t.Helper()
	if _, err := cliauth.Store(server, credentials.Credential{Token: token, CreatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
}
