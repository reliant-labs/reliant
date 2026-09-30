// Copyright (c) 2025 Reliant Labs
package cliauth

import (
	"os"
	"testing"
	"time"

	"github.com/reliant-labs/forge/pkg/cloudcred"
	"github.com/reliant-labs/forge/pkg/credentials"
)

// One login: being signed in to Reliant must mean forge is signed in to
// Reliant cloud, without forge learning that reliant exists. These pin the
// reliant half of forge/pkg/cloudcred's contract — the deposit is keyed by
// the ISSUER (the control plane, which is what forge talks to), under
// cloudcred.HostClientID, and it never touches an entry a human created with
// `forge login`.

func isolatedDeps(t *testing.T) Deps {
	t.Helper()
	home := t.TempDir()
	t.Setenv("FORGE_HOME", home)
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("HOME", home)
	return DepsFromEnv()
}

// TestDepositForForge_KeysOnTheIssuerNotTheAPIServer is the whole point of
// the deposit. reliant's own entry is keyed by the API server the CLI talks
// to; forge talks to the CONTROL PLANE, a different origin in prod
// (api. vs admin.). Depositing under the API server would write a credential
// forge never looks for, and the symptom would be "I am logged in to Reliant
// and forge still says log in".
func TestDepositForForge_KeysOnTheIssuerNotTheAPIServer(t *testing.T) {
	deps := isolatedDeps(t)
	svc := New(deps)
	path, err := svc.CredentialsPath()
	if err != nil {
		t.Fatal(err)
	}

	const apiServer = "https://api.reliantlabs.io"
	const issuer = "https://admin.reliantapi.com"
	exp := time.Now().Add(time.Hour).UTC()
	cred := credentials.Credential{
		Token:       "rlat_DEPOSIT0000000000000",
		TokenPrefix: "rlat_DEPOSIT0",
		Scopes:      []string{"reliant:api", "deploy:write"},
		Issuer:      issuer,
		ExpiresAt:   &exp,
	}

	if err := svc.DepositForForge(cred); err != nil {
		t.Fatalf("DepositForForge: %v", err)
	}

	got, err := credentials.Lookup(path, issuer, cloudcred.HostClientID)
	if err != nil {
		t.Fatalf("forge finds no host credential at the issuer %s: %v", issuer, err)
	}
	if got.Token != cred.Token {
		t.Errorf("deposited token = %q, want %q", got.Token, cred.Token)
	}
	if got.TokenPrefix != cred.TokenPrefix {
		t.Errorf("deposited prefix = %q, want %q", got.TokenPrefix, cred.TokenPrefix)
	}
	if len(got.Scopes) != 2 || got.Scopes[0] != "reliant:api" {
		t.Errorf("deposited scopes = %v, want the issued set", got.Scopes)
	}
	if got.ExpiresAt == nil || !got.ExpiresAt.Equal(exp) {
		t.Errorf("deposited expiry = %v, want %v", got.ExpiresAt, exp)
	}

	// Nothing was written under the API server: that origin is reliant's
	// key, not forge's.
	if _, err := credentials.Lookup(path, apiServer, cloudcred.HostClientID); err == nil {
		t.Error("deposited a host credential under the API server; forge keys by the issuer")
	}
}

// TestDepositForForge_PermanentTokenStaysPermanent — a daemon's rlat_ has no
// expiry (control-plane migration 00110). Inventing one here would make forge
// stop using a credential that is still perfectly valid.
func TestDepositForForge_PermanentTokenStaysPermanent(t *testing.T) {
	svc := New(isolatedDeps(t))
	path, _ := svc.CredentialsPath()
	const issuer = "https://admin.reliantapi.com"

	if err := svc.DepositForForge(credentials.Credential{
		Token:  "rlat_PERMANENT000000000000",
		Issuer: issuer,
	}); err != nil {
		t.Fatalf("DepositForForge: %v", err)
	}
	got, err := credentials.Lookup(path, issuer, cloudcred.HostClientID)
	if err != nil {
		t.Fatal(err)
	}
	if got.ExpiresAt != nil {
		t.Errorf("a permanent token gained an expiry %v", got.ExpiresAt)
	}
}

// TestDepositForForge_LeavesAHumanForgeLoginAlone is the safety property that
// makes depositing acceptable at all: `forge login` and the host deposit are
// separate entries under separate client ids, so logging in to Reliant cannot
// silently repoint a forge the user configured deliberately.
func TestDepositForForge_LeavesAHumanForgeLoginAlone(t *testing.T) {
	svc := New(isolatedDeps(t))
	path, _ := svc.CredentialsPath()
	const issuer = "https://admin.reliantapi.com"

	if err := credentials.Store(path, issuer, "forge-cli",
		credentials.Credential{Token: "rlat_HUMANFORGELOGIN"}); err != nil {
		t.Fatal(err)
	}
	if err := svc.DepositForForge(credentials.Credential{
		Token: "rlat_HOSTDEPOSIT0000", Issuer: issuer,
	}); err != nil {
		t.Fatal(err)
	}
	if human, _ := credentials.Lookup(path, issuer, "forge-cli"); human.Token != "rlat_HUMANFORGELOGIN" {
		t.Fatal("the deposit clobbered a credential a human created with `forge login`")
	}

	// And withdrawing on logout removes only the deposit.
	existed, err := svc.WithdrawFromForge(issuer)
	if err != nil || !existed {
		t.Fatalf("WithdrawFromForge: existed=%v err=%v", existed, err)
	}
	if human, _ := credentials.Lookup(path, issuer, "forge-cli"); human.Token != "rlat_HUMANFORGELOGIN" {
		t.Fatal("logging out of Reliant logged the user out of forge")
	}
	if _, err := credentials.Lookup(path, issuer, cloudcred.HostClientID); err == nil {
		t.Fatal("WithdrawFromForge left the host deposit behind")
	}
}

// TestDepositForForge_IsIdempotent — it runs on every login and every daemon
// registration, so re-depositing must replace rather than accumulate or fail.
func TestDepositForForge_IsIdempotent(t *testing.T) {
	svc := New(isolatedDeps(t))
	path, _ := svc.CredentialsPath()
	const issuer = "https://admin.reliantapi.com"

	for _, tok := range []string{"rlat_FIRST00000000000", "rlat_SECOND0000000000"} {
		if err := svc.DepositForForge(credentials.Credential{Token: tok, Issuer: issuer}); err != nil {
			t.Fatalf("deposit %s: %v", tok, err)
		}
	}
	got, err := credentials.Lookup(path, issuer, cloudcred.HostClientID)
	if err != nil || got.Token != "rlat_SECOND0000000000" {
		t.Fatalf("second deposit did not replace the first: %+v %v", got, err)
	}
}

// TestDepositForForge_WithoutAnIssuerIsRefused. A credential with no issuer
// gives no origin to key by, and guessing one would write where forge does
// not look. Refusing names the bug instead of hiding it.
func TestDepositForForge_WithoutAnIssuerIsRefused(t *testing.T) {
	svc := New(isolatedDeps(t))
	if err := svc.DepositForForge(credentials.Credential{Token: "rlat_NOISSUER0000"}); err == nil {
		t.Fatal("a credential with no issuer must not be deposited")
	}
}

// TestWithdrawFromForge_AbsentIsNotAnError — logout must succeed for a user
// who never had a deposit (logged in before this shipped, or a self-hosted
// server with no control plane).
func TestWithdrawFromForge_AbsentIsNotAnError(t *testing.T) {
	svc := New(isolatedDeps(t))
	existed, err := svc.WithdrawFromForge("https://admin.reliantapi.com")
	if err != nil {
		t.Fatalf("WithdrawFromForge on a clean file: %v", err)
	}
	if existed {
		t.Error("reported a withdrawal that could not have happened")
	}
}

// TestDepositForForge_UsesForgesOwnLocation pins that the deposit lands in
// the file forge itself resolves, not a second location. A path this package
// invented would be a credential store nothing reads.
func TestDepositForForge_UsesForgesOwnLocation(t *testing.T) {
	home := t.TempDir()
	t.Setenv("FORGE_HOME", home)
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("HOME", home)

	svc := New(DepsFromEnv())
	path, err := svc.CredentialsPath()
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.DepositForForge(credentials.Credential{
		Token: "rlat_LOCATION00000", Issuer: "https://admin.reliantapi.com",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("deposit did not write forge's own credentials file %s: %v", path, err)
	}
}
