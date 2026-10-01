// Copyright (c) 2025 Reliant Labs
package cliauth

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// ONE LOGIN on a credential that did NOT come from this process's login.
//
// The deposit is keyed by the control-plane origin, and `Login.Run` gets that
// for free because RFC 8414 discovery happens inside it. Every other way a
// credential reaches this machine — Electron minting a daemon PAT itself, a
// managed daemon's mounted Secret, a token pasted with --token — produces a
// token with NO issuer attached, and those are exactly the paths that left
// forge with no prod entry. DiscoverIssuer is how they get one: the same
// question `Login.Run` asks, asked separately.

// TestDiscoverIssuer_ReturnsTheControlPlaneNotTheAPIServer is the whole point.
// forge keys its store by the origin it TALKS to, which in prod is admin.,
// while the daemon's --server is api._ Returning the API server would write an
// entry forge never looks up.
func TestDiscoverIssuer_ReturnsTheControlPlaneNotTheAPIServer(t *testing.T) {
	const controlPlane = "https://admin.reliantapi.com"
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
	defer api.Close()

	got, err := DiscoverIssuer(context.Background(), nil, api.URL)
	if err != nil {
		t.Fatalf("DiscoverIssuer: %v", err)
	}
	if got != controlPlane {
		t.Errorf("DiscoverIssuer = %q, want the control plane %q", got, controlPlane)
	}
}

// TestDiscoverIssuer_SelfHostedHasNoControlPlane: a server that publishes no
// metadata is self-hosted with no control plane. There is nothing to deposit
// for, and that must be a recognizable error rather than a guess — depositing
// under a guessed origin writes a store nothing reads.
func TestDiscoverIssuer_SelfHostedHasNoControlPlane(t *testing.T) {
	api := httptest.NewServer(http.HandlerFunc(http.NotFound))
	defer api.Close()

	if got, err := DiscoverIssuer(context.Background(), nil, api.URL); err == nil {
		t.Fatalf("DiscoverIssuer returned %q for a server with no control plane; want an error", got)
	}
}
