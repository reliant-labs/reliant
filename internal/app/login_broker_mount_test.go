// Copyright (c) 2025 Reliant Labs
package app

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/db"
	grpcserver "github.com/reliant-labs/reliant/internal/grpc"
	"github.com/reliant-labs/reliant/pkg/config"
)

// serveAPIListener runs the api root's listener (NewAPIListener, as the graph
// builds it) with the user routes under API-key auth and signIn as the
// sign-in session node, returning its base URL.
func serveAPIListener(t *testing.T, api *config.ApiServerConfig, signIn *SignInSessions) string {
	t.Helper()
	authn, err := grpcserver.NewUserAuth(auth.Settings{Mode: auth.ModeAPIKey, APIKey: routesTestAPIKey}, nobodysTokens{})
	require.NoError(t, err)
	chain, err := grpcserver.NewUserChain(grpcserver.UserChainDeps{Auth: authn})
	require.NoError(t, err)
	listener, err := NewAPIListener(APIListenerDeps{
		API:    api,
		TLS:    &TLSFiles{},
		Chain:  chain,
		Routes: testUserRoutes(t, apiMountOptions(false)),
		HTTP:   &grpcserver.APIHTTPRoutes{},
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		SignIn: signIn,
	})
	require.NoError(t, err)
	require.NoError(t, listener.Listen(context.Background()))
	ctx, stop := context.WithCancel(context.Background())
	served := make(chan error, 1)
	go func() { served <- listener.Run(ctx) }()
	t.Cleanup(func() {
		stop()
		require.NoError(t, <-served)
	})
	return "http://" + listener.Addr().String()
}

func getSession(t *testing.T, base string) int {
	t.Helper()
	resp, err := http.Get(base + "/auth/session")
	require.NoError(t, err)
	_ = resp.Body.Close()
	return resp.StatusCode
}

// The native sign-in is mounted exactly when the identity is configured: the
// sign-in session node (NewSignInSessions) is absent without one, and the
// listener then serves no /auth/* route; with one, /auth/session answers.
func TestAPIListenerMountsSignInOnlyWithAnIdentity(t *testing.T) {
	off := &config.ApiServerConfig{BindAddress: "127.0.0.1"}
	signIn, err := NewSignInSessions(SignInSessionsDeps{API: off, DB: db.NewTestRepo(t)})
	require.NoError(t, err)
	require.Nil(t, signIn, "no identity: the sign-in session node is absent")
	require.Equal(t, http.StatusNotFound, getSession(t, serveAPIListener(t, off, signIn)), "no identity: no /auth/* route")

	on := &config.ApiServerConfig{BindAddress: "127.0.0.1", OidcClientId: "native-broker-client", IdpBrokerToken: "broker-token"}
	signIn, err = NewSignInSessions(SignInSessionsDeps{API: on, DB: db.NewTestRepo(t)})
	require.NoError(t, err)
	require.NotNil(t, signIn, "a configured identity: the sign-in session node exists")
	require.Equal(t, http.StatusOK, getSession(t, serveAPIListener(t, on, signIn)), "a configured identity mounts /auth/session")
}
