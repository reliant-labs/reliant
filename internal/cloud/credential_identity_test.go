package cloud

import (
	"context"
	"net/http"
	"strings"
	"testing"
)

// A credential is named by its display prefix and never by its value.
func TestTokenIdentity_NeverTheSecret(t *testing.T) {
	const secret = "rlat_A3f9Kd2pxxxxxxxxxxxxxxxxxxxxxxxx"
	got := TokenIdentity(secret)
	if got != "rlat_A3f9Kd2p…" {
		t.Fatalf("identity = %q, want the 13-character display prefix and an ellipsis", got)
	}
	for _, tok := range []string{"eyJhbGciOiJIUzI1NiJ9.payload.sig", "short", ""} {
		got := TokenIdentity(tok)
		if tok != "" && strings.Contains(got, tok[:min(len(tok), 6)]) {
			t.Fatalf("identity of a non-rlat token %q leaks its head: %q", tok, got)
		}
	}
}

// The 401/403 message names WHICH credential was refused by prefix and points
// at `forge cloud status`, so nobody opens the credentials file to find out —
// and it never carries the token itself.
func TestCall_RejectedCredentialNamesItsPrefixNotItsValue(t *testing.T) {
	const secret = "rlat_EXAMPLE1NOTAREALTOKENxxxxxxxxxx"
	srv := connectErrorServer(t, http.StatusForbidden, map[string]any{
		"code": "permission_denied", "message": "this credential does not carry the obs:read scope",
	}, "")
	ep, err := ResolveEndpoint("prod", &Declaration{Endpoint: srv.URL, TokenEnv: "ACME_TOKEN"})
	if err != nil {
		t.Fatal(err)
	}
	client := NewClient(ep, Credential{Token: secret, Source: SourceLogin, From: "/home/u/.config/forge/credentials.json"})
	err = client.Call(context.Background(), "controlplane.v1.ObservabilityService/GetReleaseHealth", map[string]any{}, &struct{}{})
	if err == nil {
		t.Fatal("expected a refusal")
	}
	msg := err.Error()
	if strings.Contains(msg, secret) || strings.Contains(msg, "NOTAREALTOKEN") {
		t.Fatalf("the refusal prints the secret:\n%s", msg)
	}
	for _, want := range []string{"credential: rlat_EXAMPLE1… from /home/u/.config/forge/credentials.json", "forge cloud status prod"} {
		if !strings.Contains(msg, want) {
			t.Fatalf("refusal lacks %q:\n%s", want, msg)
		}
	}
}
