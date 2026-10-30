// Copyright (c) 2025 Reliant Labs
package cmd

import (
	"strings"
	"testing"
	"time"

	"github.com/reliant-labs/forge/pkg/credentials"

	"github.com/reliant-labs/reliant/internal/cliauth"
	"github.com/reliant-labs/reliant/internal/memtemp"
)

// isolateCLI points HOME and the shared credentials file at a temp dir and
// clears the RELIANT_* variables the resolver reads, so no test can read or
// write a developer's real login. A dev shell often exports
// RELIANT_SERVER_URL, which outranks the compiled defaults tests assert on; an
// empty value reads as unset. Returns the credentials path.
func isolateCLI(t *testing.T) string {
	t.Helper()
	// In memory: every login these tests store is an fsync (internal/memtemp).
	home := memtemp.Dir(t)
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("FORGE_HOME", home)
	// Every CLIConfig variable, and the config file that could set them.
	for _, name := range []string{
		envServerURL, envGatewayURL, envToken, "RELIANT_SKIP_TLS_VERIFY", "RELIANT_WEB_ORIGIN", "FRONTEND_PORT",
		"RELIANT_INSTANCE_ID", "RELIANT_USER_CONFIG_DIR", "RELIANT_APP_DATA_DIR", "RELIANT_CONFIG",
	} {
		t.Setenv(name, "")
	}
	presentDesktop(t)
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
