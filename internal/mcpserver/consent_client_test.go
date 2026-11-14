// Copyright (c) 2025 Reliant Labs

package mcpserver

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/connectorgrant"
)

// The workspace endpoint identifies the calling application only by its
// token's client_id claim, as the forge endpoint does. These tests pin that,
// and that a client whose bound grant lapsed never inherits another grant.

// resolveWorkspace authenticates one OAuth request to the workspace endpoint
// the way its middleware does, with headers the caller chose.
func resolveWorkspace(t *testing.T, store *memStore, validator OAuthTokenValidator, headers map[string]string) (*Session, error) {
	t.Helper()
	a := &connectorAuth{
		store:       store,
		credentials: store.creds,
		oauth:       testOAuthConfig(),
		tokens:      validator,
		bindings:    store,
		consentBase: "https://app.example.com",
		logger:      slog.Default(),
	}
	r := httptest.NewRequest(http.MethodPost, MountPath, nil)
	for k, v := range headers {
		r.Header.Set(k, v)
	}
	return a.resolve(r, "user-jwt")
}

func bindWorkspace(t *testing.T, store *memStore, client, grant string) {
	t.Helper()
	require.NoError(t, store.PutBinding(context.Background(), &connectorgrant.ClientBinding{
		ID: "b-" + client, UserID: "user-1", ClientID: connectorgrant.BindingClientID(connectorgrant.KindWorkspace, client), GrantID: grant,
	}))
}

// Client A, holding the user's bearer, names client B in a header and still
// gets only its own grant; a token that names no client cannot borrow B's
// binding through a header either.
func TestWorkspaceClientIsTheTokensNotTheHeaders(t *testing.T) {
	store := newMemStore()
	addGrant(t, store, "user-1", "narrow")
	addGrant(t, store, "user-1", "broad")
	bindWorkspace(t, store, "client-a", "narrow")
	bindWorkspace(t, store, "client-b", "broad")

	for _, header := range []string{"X-MCP-Client-Id", "X-Client-Name"} {
		sess, err := resolveWorkspace(t, store, &stubClientTokenValidator{userID: "user-1", clientID: "client-a"},
			map[string]string{header: "client-b"})
		require.NoError(t, err, header)
		assert.Equal(t, "narrow", sess.GrantID, "%s: client A got client B's grant through a header", header)

		sess, err = resolveWorkspace(t, store, &stubTokenValidator{userID: "user-1"}, map[string]string{header: "client-b"})
		var consent *ConsentError
		require.ErrorAs(t, err, &consent, "%s: a token with no client got %+v through a header", header, sess)
		assert.NotContains(t, consent.ConsentURL, "client-b", "%s: the consent URL names the header's client", header)
	}

	// The same through the endpoint: a 403, not client B's grant.
	handler, err := NewHTTPHandler(HTTPDeps{
		Store: store, Credentials: store.creds, Sender: &fakeSender{}, OAuth: testOAuthConfig(),
		TokenValidator: &stubTokenValidator{userID: "user-1"}, Bindings: store,
	})
	require.NoError(t, err)
	srv := httptest.NewServer(handler)
	defer srv.Close()
	req, err := http.NewRequest(http.MethodPost, srv.URL+MountPath, nil)
	require.NoError(t, err)
	req.Header.Set("Authorization", "Bearer user-jwt")
	req.Header.Set("X-MCP-Client-Id", "client-b")
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	assert.Equal(t, http.StatusForbidden, resp.StatusCode)
}

// A client whose bound grant is revoked, expired or gone is asked again; it
// never falls back to the user's one other live grant. A client never bound
// keeps the single-connector default.
func TestWorkspaceInvalidatedBindingRequiresConsent(t *testing.T) {
	past := time.Now().Add(-time.Hour)
	for name, invalidate := range map[string]func(store *memStore, g *connectorgrant.Grant){
		"revoked": func(_ *memStore, g *connectorgrant.Grant) { g.RevokedAt = &past },
		"expired": func(_ *memStore, g *connectorgrant.Grant) { g.ExpiresAt = &past },
		"deleted": func(store *memStore, g *connectorgrant.Grant) { delete(store.grants, g.ID) },
	} {
		t.Run(name, func(t *testing.T) {
			store := newMemStore()
			bound := addGrant(t, store, "user-1", "bound")
			addGrant(t, store, "user-1", "other")
			bindWorkspace(t, store, "client-a", "bound")
			clientA := &stubClientTokenValidator{userID: "user-1", clientID: "client-a"}

			sess, err := resolveWorkspace(t, store, clientA, nil)
			require.NoError(t, err)
			require.Equal(t, "bound", sess.GrantID)

			store.mu.Lock()
			invalidate(store, bound)
			store.mu.Unlock()

			sess, err = resolveWorkspace(t, store, clientA, nil)
			var consent *ConsentError
			require.ErrorAs(t, err, &consent, "the client inherited %+v", sess)
			assert.Contains(t, consent.ConsentURL, "client_id=client-a")

			// Backwards compatible: a client that was never bound still
			// resolves to the user's one live connector.
			sess, err = resolveWorkspace(t, store, &stubClientTokenValidator{userID: "user-1", clientID: "client-new"}, nil)
			require.NoError(t, err)
			assert.Equal(t, "other", sess.GrantID)
		})
	}
}

// A token that names no OAuth client cannot be bound to a choice, so no
// binding is consulted (#2716's rule) and the caller gets the user's default:
// their one live connector, or consent when there is a choice to make.
func TestWorkspaceTokenWithNoClientGetsTheUsersDefault(t *testing.T) {
	store := newMemStore()
	addGrant(t, store, "user-1", "only")
	noClient := &stubTokenValidator{userID: "user-1"}

	sess, err := resolveWorkspace(t, store, noClient, nil)
	require.NoError(t, err)
	assert.Equal(t, "only", sess.GrantID)

	addGrant(t, store, "user-1", "second")
	bindWorkspace(t, store, "", "second") // a binding keyed by the empty client must never match
	_, err = resolveWorkspace(t, store, noClient, nil)
	var consent *ConsentError
	require.ErrorAs(t, err, &consent)
}
