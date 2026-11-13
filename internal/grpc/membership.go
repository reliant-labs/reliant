// Copyright (c) 2025 Reliant Labs
package grpc

import (
	"net/http"

	"github.com/reliant-labs/reliant/internal/authz"
	"github.com/reliant-labs/reliant/internal/orgpolicy"
)

// withMembership puts m on every request's context, ahead of every route,
// Connect and plain HTTP alike, so each authz decision about an org resource
// asks it (authz.WithMembership). A nil m is a deployment with no orgs.
//
// The org policy resolver p rides beside it (orgpolicy.WithResolver), so an
// enforcement point reads an org's policy where it reads membership. A nil p
// is a deployment with no policies.
func withMembership(m authz.Membership, p *orgpolicy.Resolver, h http.Handler) http.Handler {
	if m == nil {
		m = authz.NoOrgs{}
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := orgpolicy.WithResolver(authz.WithMembership(r.Context(), m), p)
		h.ServeHTTP(w, r.WithContext(ctx))
	})
}
