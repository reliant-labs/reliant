// Copyright (c) 2025 Reliant Labs
package grpc

import (
	"maps"
	"slices"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/pkg/middleware"
)

// TestUnauthenticatedProceduresArePinned pins the procedures the API's auth
// interceptor lets through with no credentials. The set is projected by forge
// generate from every rpc whose proto declares
// (forge.v1.method) = {auth_required: false}, so making an rpc public is a
// one-line proto edit; this test makes it a deliberate one too. Update the
// list in the same change as the proto.
//
// The entries are wire paths, not reliantv1connect constants, so renaming a
// public rpc (which changes what clients reach unauthenticated) fails here
// rather than following along silently.
func TestUnauthenticatedProceduresArePinned(t *testing.T) {
	want := []string{
		// forge always lists the standard gRPC health service. reliant does
		// not mount it, so these two admit nothing.
		"/grpc.health.v1.Health/Check",
		"/grpc.health.v1.Health/Watch",

		// Probes and version info.
		"/reliant.v1.SystemService/Health",
		"/reliant.v1.SystemService/Ready",
		"/reliant.v1.SystemService/Info",
		"/reliant.v1.SystemService/Version",
		// Sign-in runs before the caller holds a token.
		"/reliant.v1.SystemService/StartOAuthSignIn",
		// Browser dev mode, before a JWT is available.
		"/reliant.v1.SystemService/DevAuthLoad",
		"/reliant.v1.SystemService/DevAuthSave",
		"/reliant.v1.SystemService/DevAuthClear",
		// These authenticate their own bearer (a daemon credential the
		// interceptor refuses by design): not gated here, never anonymous.
		"/reliant.v1.TokenService/ExchangeToken",
		"/reliant.v1.TokenService/GetGitToken",
		"/reliant.v1.TokenService/GetGitHubBotToken",
	}
	slices.Sort(want)

	got := slices.Sorted(maps.Keys(middleware.UnauthenticatedProcedures))
	require.Equal(t, want, got)
}
