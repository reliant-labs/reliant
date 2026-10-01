// Copyright (c) 2025 Reliant Labs
package cliauth

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/reliant-labs/forge/pkg/cloudcred"
	"github.com/reliant-labs/forge/pkg/credentials"
)

// DepositTokenForServer is the entry point for every credential that did NOT
// come from this process's own browser login: Electron's own mint, a managed
// daemon's mounted Secret, a pasted --token. Those paths hold a bare token and
// a --server URL and nothing else, so the issuer has to be discovered here.
//
// This is the bug these tests pin. The deposit existed and was wired to
// `reliant auth login` and to interactive `daemon register` — neither of which
// an Electron user ever runs — so a laptop signed in to prod through the app
// had no prod entry in forge's store at all.

// fakeAPIAdvertising returns an API server whose RFC 8414 metadata names
// controlPlane as the issuer, exactly as reliant-api-server does in prod.
func fakeAPIAdvertising(t *testing.T, controlPlane string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/.well-known/oauth-authorization-server" {
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]string{
			"issuer":                 controlPlane,
			"authorization_endpoint": controlPlane + "/oauth/authorize",
			"token_endpoint":         controlPlane + "/oauth/token",
		})
	}))
	t.Cleanup(srv.Close)
	return srv
}

// TestDepositTokenForServer_KeysOnTheDiscoveredControlPlane is the fix for the
// reported symptom. Given only the token and the --server the daemon talks to,
// the deposit must land under the origin forge resolves for
// `forge.ControlPlane {}` — https://admin.reliantapi.com — not under --server.
func TestDepositTokenForServer_KeysOnTheDiscoveredControlPlane(t *testing.T) {
	deps := isolatedDeps(t)
	svc := New(deps)
	path, err := svc.CredentialsPath()
	if err != nil {
		t.Fatal(err)
	}

	const controlPlane = "https://admin.reliantapi.com"
	api := fakeAPIAdvertising(t, controlPlane)
	const token = "rlat_DAEMONDEPOSIT000000"

	if err := svc.DepositTokenForServer(context.Background(), api.URL, token, nil); err != nil {
		t.Fatalf("DepositTokenForServer: %v", err)
	}

	got, err := credentials.Lookup(path, controlPlane, cloudcred.HostClientID)
	if err != nil {
		t.Fatalf("forge finds no credential at the control plane %s: %v", controlPlane, err)
	}
	if got.Token != token {
		t.Errorf("deposited token = %q, want %q", got.Token, token)
	}

	// And NOT under the API server, which is the mistake that produces a
	// store forge never reads.
	if _, err := credentials.Lookup(path, api.URL, cloudcred.HostClientID); err == nil {
		t.Error("deposit also landed under the API server origin; forge never looks there")
	}
}

// TestDepositTokenForServer_DaemonPATStaysPermanent: a daemon PAT does not
// expire (control-plane migration 00110). Inventing an expiry would make forge
// abandon a token that is still good.
func TestDepositTokenForServer_DaemonPATStaysPermanent(t *testing.T) {
	deps := isolatedDeps(t)
	svc := New(deps)
	path, _ := svc.CredentialsPath()

	const controlPlane = "https://admin.reliantapi.com"
	api := fakeAPIAdvertising(t, controlPlane)

	if err := svc.DepositTokenForServer(context.Background(), api.URL, "rlat_PERMANENT0000000000", nil); err != nil {
		t.Fatal(err)
	}
	got, err := credentials.Lookup(path, controlPlane, cloudcred.HostClientID)
	if err != nil {
		t.Fatal(err)
	}
	if got.ExpiresAt != nil {
		t.Errorf("deposited a daemon PAT with expiry %v; daemon PATs never expire", got.ExpiresAt)
	}
}

// TestDepositTokenForServer_IsIdempotent: it runs on every daemon start, so
// calling it twice must leave one good entry rather than failing the second
// time.
func TestDepositTokenForServer_IsIdempotent(t *testing.T) {
	deps := isolatedDeps(t)
	svc := New(deps)
	path, _ := svc.CredentialsPath()

	const controlPlane = "https://admin.reliantapi.com"
	api := fakeAPIAdvertising(t, controlPlane)

	if err := svc.DepositTokenForServer(context.Background(), api.URL, "rlat_FIRST000000000000", nil); err != nil {
		t.Fatal(err)
	}
	if err := svc.DepositTokenForServer(context.Background(), api.URL, "rlat_SECOND00000000000", nil); err != nil {
		t.Fatalf("second deposit failed: %v", err)
	}
	got, err := credentials.Lookup(path, controlPlane, cloudcred.HostClientID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Token != "rlat_SECOND00000000000" {
		t.Errorf("token = %q, want the most recent deposit", got.Token)
	}
}

// TestDepositTokenForServer_LeavesAHumanForgeLoginAlone: entries are keyed by
// (endpoint, client), and the deposit uses HostClientID. A credential a human
// chose deliberately with `forge login` must survive.
func TestDepositTokenForServer_LeavesAHumanForgeLoginAlone(t *testing.T) {
	deps := isolatedDeps(t)
	svc := New(deps)
	path, _ := svc.CredentialsPath()

	const controlPlane = "https://admin.reliantapi.com"
	api := fakeAPIAdvertising(t, controlPlane)

	if err := credentials.Store(path, controlPlane, "forge-cli",
		credentials.Credential{Token: "rlat_HUMANFORGELOGIN0"}); err != nil {
		t.Fatal(err)
	}
	if err := svc.DepositTokenForServer(context.Background(), api.URL, "rlat_DEPOSITED00000000", nil); err != nil {
		t.Fatal(err)
	}
	human, err := credentials.Lookup(path, controlPlane, "forge-cli")
	if err != nil || human.Token != "rlat_HUMANFORGELOGIN0" {
		t.Fatalf("the deposit disturbed a `forge login` credential: %v (%q)", err, human.Token)
	}
}

// TestDepositTokenForServer_SelfHostedIsNotAnError: a self-hosted server
// publishes no RFC 8414 metadata, so there is no control plane to deposit for.
// That is an ordinary configuration, not a failure — and the daemon calling
// this on every start must not be made noisy by it.
func TestDepositTokenForServer_SelfHostedIsNotAnError(t *testing.T) {
	deps := isolatedDeps(t)
	svc := New(deps)

	selfHosted := httptest.NewServer(http.HandlerFunc(http.NotFound))
	defer selfHosted.Close()

	if err := svc.DepositTokenForServer(context.Background(), selfHosted.URL, "rlat_SELFHOSTED000000", nil); err != nil {
		t.Errorf("DepositTokenForServer on a self-hosted server returned %v; want nil", err)
	}
}

// TestDepositTokenForServer_RefusesAnEmptyToken: an empty token would overwrite
// a good entry with an unusable one.
func TestDepositTokenForServer_RefusesAnEmptyToken(t *testing.T) {
	deps := isolatedDeps(t)
	svc := New(deps)
	api := fakeAPIAdvertising(t, "https://admin.reliantapi.com")

	if err := svc.DepositTokenForServer(context.Background(), api.URL, "", nil); err == nil {
		t.Error("DepositTokenForServer accepted an empty token")
	}
}
