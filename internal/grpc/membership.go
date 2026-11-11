// Copyright (c) 2025 Reliant Labs
package grpc

import (
	"net/http"

	"github.com/reliant-labs/reliant/internal/authz"
)

// withMembership puts m on every request's context, ahead of every route,
// Connect and plain HTTP alike, so each authz decision about an org resource
// asks it (authz.WithMembership). A nil m is a deployment with no orgs.
func withMembership(m authz.Membership, h http.Handler) http.Handler {
	if m == nil {
		m = authz.NoOrgs{}
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h.ServeHTTP(w, r.WithContext(authz.WithMembership(r.Context(), m)))
	})
}
