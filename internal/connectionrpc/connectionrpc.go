// Copyright (c) 2025 Reliant Labs

// Package connectionrpc is the Connect-handler side of the connections
// domain that ConnectionService and CatalogService's integration search
// share: who the caller is, and how an auth method is spelled on the wire.
//
//forge:exclude-contract: pure caller extraction and enum mapping; no I/O, state or time
package connectionrpc

import (
	"context"
	"fmt"

	"connectrpc.com/connect"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/connections"
	"github.com/reliant-labs/reliant/internal/db/core"
)

// Caller is the authenticated user a connection RPC acts for, or
// Unauthenticated.
func Caller(ctx context.Context) (string, error) {
	userID, ok := auth.GetUserIDFromContext(ctx)
	if !ok || userID == "" {
		return "", connect.NewError(connect.CodeUnauthenticated, fmt.Errorf("user ID not found in context"))
	}
	return userID, nil
}

// AuthKind is how a connection's auth method is spelled on the wire.
func AuthKind(k string) reliantv1.ConnectionAuthKind {
	switch k {
	case core.ConnectionAuthOAuth2:
		return reliantv1.ConnectionAuthKind_CONNECTION_AUTH_KIND_OAUTH2
	case connections.MethodDelegated:
		return reliantv1.ConnectionAuthKind_CONNECTION_AUTH_KIND_DELEGATED
	case core.ConnectionAuthAPIKey:
		return reliantv1.ConnectionAuthKind_CONNECTION_AUTH_KIND_API_KEY
	case core.ConnectionAuthBasic:
		return reliantv1.ConnectionAuthKind_CONNECTION_AUTH_KIND_BASIC
	case core.ConnectionAuthNone:
		return reliantv1.ConnectionAuthKind_CONNECTION_AUTH_KIND_NONE
	}
	return reliantv1.ConnectionAuthKind_CONNECTION_AUTH_KIND_UNSPECIFIED
}
