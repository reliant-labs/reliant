// Copyright (c) 2025 Reliant Labs
package commands

import (
	"testing"

	"github.com/reliant-labs/forge/pkg/cloudcred"
	"github.com/reliant-labs/forge/pkg/credentials"

	"github.com/reliant-labs/reliant/internal/cliauth"
)

// ONE LOGIN, end to end through the COMMAND. internal/cliauth's own tests pin
// the deposit mechanics; these pin that `reliant auth login` and
// `reliant auth logout` actually call them — the wiring, which is the part
// that silently regresses when someone edits the command.

// TestAuthLogin_LogsForgeInToo: after `reliant auth login`, forge resolves a
// credential for the control plane with no second browser login.
func TestAuthLogin_LogsForgeInToo(t *testing.T) {
	path := isolateCLI(t)
	withFakeLoginBrowser(t)
	cp := newFakeCP(t, "rlat_ONELOGIN00000000")

	if out, err := runRoot(t, "auth", "login", "--server", cp.srv.URL); err != nil {
		t.Fatalf("auth login: %v\n%s", err, out)
	}

	// The fake control plane advertises itself as the issuer, so that is the
	// origin forge's entry must be keyed by.
	got, err := credentials.Lookup(path, cp.srv.URL, cloudcred.HostClientID)
	if err != nil {
		t.Fatalf("forge has no credential after `reliant auth login`: %v", err)
	}
	if got.Token != "rlat_ONELOGIN00000000" {
		t.Errorf("forge got token %q, want the token Reliant just issued", got.Token)
	}

	// `reliant auth logout` takes it away again.
	if _, err := runRoot(t, "auth", "logout", "--server", cp.srv.URL); err != nil {
		t.Fatal(err)
	}
	if _, err := credentials.Lookup(path, cp.srv.URL, cloudcred.HostClientID); err == nil {
		t.Fatal("logging out of Reliant left forge logged in")
	}
}

// TestAuthLogout_KeepsAHumanForgeLogin: a user who ran `forge login` chose that
// credential deliberately. Logging out of Reliant must not take it.
func TestAuthLogout_KeepsAHumanForgeLogin(t *testing.T) {
	path := isolateCLI(t)
	withFakeLoginBrowser(t)
	cp := newFakeCP(t, "rlat_ONELOGIN00000000")

	if err := credentials.Store(path, cp.srv.URL, "forge-cli",
		credentials.Credential{Token: "rlat_HUMANFORGE"}); err != nil {
		t.Fatal(err)
	}
	if out, err := runRoot(t, "auth", "login", "--server", cp.srv.URL); err != nil {
		t.Fatalf("auth login: %v\n%s", err, out)
	}
	if human, _ := credentials.Lookup(path, cp.srv.URL, "forge-cli"); human.Token != "rlat_HUMANFORGE" {
		t.Fatal("the login deposit overwrote a `forge login` credential")
	}
	if _, err := runRoot(t, "auth", "logout", "--server", cp.srv.URL); err != nil {
		t.Fatal(err)
	}
	if human, _ := credentials.Lookup(path, cp.srv.URL, "forge-cli"); human.Token != "rlat_HUMANFORGE" {
		t.Fatal("`reliant auth logout` deleted the user's own `forge login`")
	}
}

// TestAuthLogout_SucceedsWithNoDeposit: a user who logged in before this
// shipped has a reliant entry and no forge deposit. Logout must not fail for
// them, and must still forget the reliant login.
func TestAuthLogout_SucceedsWithNoDeposit(t *testing.T) {
	path := isolateCLI(t)
	const server = "https://api.example.invalid"
	if _, err := cliauth.Store(server, credentials.Credential{
		Token: "rlat_PREEXISTING0000", Issuer: "https://admin.example.invalid",
	}); err != nil {
		t.Fatal(err)
	}
	if out, err := runRoot(t, "auth", "logout", "--server", server); err != nil {
		t.Fatalf("logout with no forge deposit: %v\n%s", err, out)
	}
	if _, err := credentials.Lookup(path, server, cliauth.ClientID); err == nil {
		t.Fatal("logout did not forget the reliant login")
	}
}
