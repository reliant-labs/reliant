// Copyright (c) 2025 Reliant Labs
package grpc

import (
	"context"

	"github.com/reliant-labs/forge/pkg/activity"

	"github.com/reliant-labs/reliant/internal/auth"
)

// Principal names the caller and scope of each activity event the API
// listener's chain writes (serverkit.ChainDeps.Activity). forge's default
// reads forge claims (authn.ClaimsFromContext), which nothing on reliant's
// serving path sets: reliant's AuthInterceptor stashes the caller with
// auth.WithUser, so the default would name every event system:anonymous.
//
//   - Actor is the user the request authenticated as. A machine credential
//     acting for a user (an `rlat_` access token) resolves to that user,
//     because the auth interceptor installs the acting user's id.
//   - Scope is that user's id. reliant has no orgs, and every record an rpc
//     touches belongs to one user; projects are the natural sub-scope later.
//
// An unauthenticated call (an auth_required: false rpc) is
// system:anonymous with no scope, as forge's own principal reports it.
type Principal struct{}

var _ activity.Principal = Principal{}

// Actor is the authenticated user on ctx, by email when the credential
// carries one.
func (Principal) Actor(ctx context.Context) activity.Actor {
	id, ok := auth.GetUserIDFromContext(ctx)
	if !ok || id == "" {
		return activity.System("anonymous")
	}
	name, _ := auth.GetUserEmailFromContext(ctx)
	if name == "" {
		name = id
	}
	return activity.User(id, name)
}

// Scope is the authenticated user's id, or "" for an unauthenticated call.
func (Principal) Scope(ctx context.Context) string {
	id, _ := auth.GetUserIDFromContext(ctx)
	return id
}
