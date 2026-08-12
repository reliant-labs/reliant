// Copyright (c) 2025 Reliant Labs

package mcpserver

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/connectorgrant"
)

// TestBothTransportsAreServed pins the failure that OAuth success hides.
//
// Clients probe /sse to decide which transport a server speaks. ChatGPT's
// connector discovery does exactly this, and a 404 there fails the connection
// with MCP_ACTION_DISCOVERY_FAILED *after* authentication has already
// succeeded — which reads like an auth bug and is not one.
func TestBothTransportsAreServed(t *testing.T) {
	store := newMemStore()
	raw, hash, prefix, err := connectorgrant.GenerateCredential()
	require.NoError(t, err)

	require.NoError(t, store.CreateGrant(context.Background(), &connectorgrant.Grant{
		ID:           "grant-transport",
		UserID:       "user-1",
		DaemonID:     "daemon-1",
		TokenHash:    hash,
		TokenPrefix:  prefix,
		AllowedTools: ReadOnlyToolNames(),
		PathRoot:     "/workspace",
		ExecMode:     connectorgrant.ExecDeny,
	}))

	handler, err := NewHTTPHandler(HTTPDeps{Store: store, Sender: &fakeSender{}})
	require.NoError(t, err)

	srv := httptest.NewServer(handler)
	defer srv.Close()

	// "/" is included because ChatGPT posts its handshake to the base URL the
	// user typed, not to the `resource` from the discovery document.
	for _, path := range []string{MountPath, SSEMountPath, "/"} {
		t.Run(path, func(t *testing.T) {
			req, err := http.NewRequest(http.MethodGet, srv.URL+path, nil)
			require.NoError(t, err)
			req.Header.Set("Accept", "text/event-stream")
			req.Header.Set("Authorization", "Bearer "+raw)

			resp, err := http.DefaultClient.Do(req)
			require.NoError(t, err)
			defer func() { _ = resp.Body.Close() }()

			require.NotEqual(t, http.StatusNotFound, resp.StatusCode,
				"%s must be served — a 404 here fails client discovery after auth succeeds", path)
		})
	}
}

// TestRootMountDoesNotSwallowUnknownPaths guards the risk the root mount
// introduces: registered as a prefix rather than an exact match, "/" would
// absorb every unrouted path and turn honest 404s into MCP protocol errors.
func TestRootMountDoesNotSwallowUnknownPaths(t *testing.T) {
	store := newMemStore()
	raw, hash, prefix, err := connectorgrant.GenerateCredential()
	require.NoError(t, err)

	require.NoError(t, store.CreateGrant(context.Background(), &connectorgrant.Grant{
		ID:           "grant-root-scope",
		UserID:       "user-1",
		DaemonID:     "daemon-1",
		TokenHash:    hash,
		TokenPrefix:  prefix,
		AllowedTools: ReadOnlyToolNames(),
		PathRoot:     "/workspace",
		ExecMode:     connectorgrant.ExecDeny,
	}))

	handler, err := NewHTTPHandler(HTTPDeps{Store: store, Sender: &fakeSender{}})
	require.NoError(t, err)

	srv := httptest.NewServer(handler)
	defer srv.Close()

	for _, path := range []string{"/nope", "/mcp-not-really", "/reliant.v1.ChatService/List"} {
		t.Run(path, func(t *testing.T) {
			req, err := http.NewRequest(http.MethodGet, srv.URL+path, nil)
			require.NoError(t, err)
			req.Header.Set("Authorization", "Bearer "+raw)

			resp, err := http.DefaultClient.Do(req)
			require.NoError(t, err)
			defer func() { _ = resp.Body.Close() }()

			require.Equal(t, http.StatusNotFound, resp.StatusCode,
				"%s must still 404 — the root mount must match only the root", path)
		})
	}
}

// TestUnknownPathsRejectBeforeRouting documents the middleware order: the
// authenticator wraps the transport mux, so an unauthenticated request to any
// path — known or not — is refused with 401 before routing decides whether the
// path exists.
//
// That ordering is deliberate. Answering 404 first would let an unauthenticated
// caller map which endpoints a deployment serves; refusing everything at the
// door tells them nothing.
func TestUnknownPathsRejectBeforeRouting(t *testing.T) {
	store := newMemStore()
	handler, err := NewHTTPHandler(HTTPDeps{Store: store, Sender: &fakeSender{}})
	require.NoError(t, err)

	srv := httptest.NewServer(handler)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/not-an-mcp-path")
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	require.Equal(t, http.StatusUnauthorized, resp.StatusCode)

	// With a valid credential, an unknown path is a genuine 404 — the mux is
	// not a catch-all that would serve MCP on any URL.
	raw, hash, prefix, err := connectorgrant.GenerateCredential()
	require.NoError(t, err)
	require.NoError(t, store.CreateGrant(context.Background(), &connectorgrant.Grant{
		ID: "g", UserID: "u", DaemonID: "d", TokenHash: hash, TokenPrefix: prefix,
		AllowedTools: ReadOnlyToolNames(), PathRoot: "/workspace",
		ExecMode: connectorgrant.ExecDeny,
	}))

	req, err := http.NewRequest(http.MethodGet, srv.URL+"/not-an-mcp-path", nil)
	require.NoError(t, err)
	req.Header.Set("Authorization", "Bearer "+raw)
	authed, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer func() { _ = authed.Body.Close() }()
	require.Equal(t, http.StatusNotFound, authed.StatusCode)
}
