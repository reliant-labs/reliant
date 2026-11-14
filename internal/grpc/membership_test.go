// Copyright (c) 2025 Reliant Labs
package grpc

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	controlplanev1 "github.com/reliant-labs/reliant/gen/controlplane/controlplane/v1"
	"github.com/reliant-labs/reliant/internal/accesstokenclient"
	"github.com/reliant-labs/reliant/internal/authz"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/orgpolicy"
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
	h := withMembership(onlyAcme{}, nil, http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		gotAcme = authz.CanReadChat(r.Context(), "u", &core.Chat{UserID: "u", OrgID: &acme})
		gotGlobex = authz.CanReadChat(r.Context(), "u", &core.Chat{UserID: "u", OrgID: &globex})
	}))
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/x", nil))
	if !gotAcme || gotGlobex {
		t.Fatalf("acme=%v globex=%v; want the listener's membership decided both", gotAcme, gotGlobex)
	}

	// No membership configured: a deployment with no orgs, owner-only.
	var owner bool
	withMembership(nil, nil, http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		owner = authz.CanReadChat(r.Context(), "u", &core.Chat{UserID: "u", OrgID: &globex})
	})).ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/x", nil))
	if !owner {
		t.Fatal("with no orgs the owner keeps access")
	}
}

type acmePolicy struct{}

func (acmePolicy) ResolveOrgPolicy(_ context.Context, orgID string) (accesstokenclient.OrgPolicy, error) {
	return accesstokenclient.OrgPolicy{Version: 4, Policy: &controlplanev1.OrgPolicy{Machines: &controlplanev1.MachinesPolicy{ForceCredentialsNone: true}}}, nil
}

// The org policy resolver rides beside membership on every request through a
// listener edge, so an enforcement point reads the org's policy (and its
// version) where it reads membership.
func TestWithMembershipCarriesTheOrgPolicyResolver(t *testing.T) {
	t.Cleanup(func() { orgpolicy.SetProcessResolver(nil) })
	orgpolicy.SetProcessResolver(nil)
	r := orgpolicy.New(acmePolicy{})
	var got orgpolicy.Policy
	var err error
	withMembership(onlyAcme{}, r, http.HandlerFunc(func(_ http.ResponseWriter, req *http.Request) {
		got, err = orgpolicy.For(req.Context(), "acme")
	})).ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/x", nil))
	if err != nil || got.Version != 4 || got.None() {
		t.Fatalf("policy on the request = %+v, %v; want acme's v4", got, err)
	}

	// No resolver on the listener: no policies.
	withMembership(onlyAcme{}, nil, http.HandlerFunc(func(_ http.ResponseWriter, req *http.Request) {
		got, err = orgpolicy.For(req.Context(), "acme")
	})).ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/x", nil))
	if err != nil || !got.None() {
		t.Fatalf("without a resolver = %+v, %v; want no policy", got, err)
	}
}
