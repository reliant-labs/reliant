// Copyright (c) 2025 Reliant Labs
package interceptors

import (
	"testing"
	"time"

	"github.com/reliant-labs/reliant/internal/auth"
)

func TestSignInHeartbeatOncePerUserPerUTCDay(t *testing.T) {
	now := time.Date(2026, 11, 11, 23, 59, 0, 0, time.UTC)
	h := newSignInHeartbeat()
	h.now = func() time.Time { return now }

	if !h.first("u-1") || h.first("u-1") {
		t.Fatal("a user is recorded once a day")
	}
	if !h.first("u-2") {
		t.Fatal("each user is recorded")
	}
	now = now.Add(2 * time.Minute) // past midnight UTC
	if !h.first("u-1") {
		t.Fatal("the next UTC day records the user again")
	}
}

func TestOnlyAPersonsSessionCountsAsSignedIn(t *testing.T) {
	cases := []struct {
		name   string
		claims *auth.JWTClaims
		want   bool
	}{
		{"session JWT", &auth.JWTClaims{Sub: "u", Credential: auth.CredentialSession}, true},
		{"anonymous Supabase session", &auth.JWTClaims{Sub: "u", Credential: auth.CredentialSession, IsAnonymous: true}, false},
		{"rlat_ access token", &auth.JWTClaims{Sub: "u", Credential: auth.CredentialAccessToken}, false},
		{"self-host shared key", &auth.JWTClaims{Sub: "u", Credential: auth.CredentialSharedKey}, false},
		{"OAuth client token", &auth.JWTClaims{Sub: "u", Credential: auth.CredentialOAuthClient}, false},
		{"no subject", &auth.JWTClaims{Credential: auth.CredentialSession}, false},
		{"no claims", nil, false},
	}
	for _, c := range cases {
		if got := countsAsSignedIn(c.claims); got != c.want {
			t.Errorf("%s: countsAsSignedIn = %v, want %v", c.name, got, c.want)
		}
	}
}
