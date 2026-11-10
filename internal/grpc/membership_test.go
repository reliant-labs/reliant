// Copyright (c) 2025 Reliant Labs
package grpc

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/reliant-labs/reliant/internal/authz"
	"github.com/reliant-labs/reliant/internal/db/core"
)

type onlyAcme struct{}

func (onlyAcme) IsMember(_ context.Context, _, orgID string) (bool, bool, error) {
	return orgID == "acme", true, nil
}

// Every request through a listener edge decides org resources against the
// listener's membership; without it they would be denied (authz fails closed).
func TestWithMembershipReachesEveryRequest(t *testing.T) {
	acme, globex := "acme", "globex"
	var gotAcme, gotGlobex bool
	h := withMembership(onlyAcme{}, http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		gotAcme = authz.CanReadChat(r.Context(), "u", &core.Chat{UserID: "u", OrgID: &acme})
		gotGlobex = authz.CanReadChat(r.Context(), "u", &core.Chat{UserID: "u", OrgID: &globex})
	}))
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/x", nil))
	if !gotAcme || gotGlobex {
		t.Fatalf("acme=%v globex=%v; want the listener's membership decided both", gotAcme, gotGlobex)
	}

	// No membership configured: a deployment with no orgs, owner-only.
	var owner bool
	withMembership(nil, http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		owner = authz.CanReadChat(r.Context(), "u", &core.Chat{UserID: "u", OrgID: &globex})
	})).ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/x", nil))
	if !owner {
		t.Fatal("with no orgs the owner keeps access")
	}
}
