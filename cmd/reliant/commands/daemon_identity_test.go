// Copyright (c) 2025 Reliant Labs
package commands

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"connectrpc.com/connect"
	"github.com/spf13/cobra"
	"github.com/stretchr/testify/require"
	"golang.org/x/net/http2"
	"golang.org/x/net/http2/h2c"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/gen/reliant/v1/reliantv1connect"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/toolexec/bootstrap"
)

// whoAmIGateway answers WhoAmI from the bearer, the way the real gateway
// answers from the principal its daemon auth interceptor resolved.
type whoAmIGateway struct {
	reliantv1connect.UnimplementedToolsDaemonServiceHandler
	owners map[string]string // token -> user id
}

func (g *whoAmIGateway) WhoAmI(_ context.Context, req *connect.Request[reliantv1.WhoAmIRequest]) (*connect.Response[reliantv1.WhoAmIResponse], error) {
	token := strings.TrimPrefix(req.Header().Get("Authorization"), "Bearer ")
	owner, ok := g.owners[token]
	if !ok {
		return nil, connect.NewError(connect.CodeUnauthenticated, errors.New("invalid daemon auth token"))
	}
	return connect.NewResponse(&reliantv1.WhoAmIResponse{UserId: owner}), nil
}

func serveDaemonGateway(t *testing.T, handler reliantv1connect.ToolsDaemonServiceHandler) daemonGateway {
	t.Helper()
	mux := http.NewServeMux()
	mux.Handle(reliantv1connect.NewToolsDaemonServiceHandler(handler))
	srv := httptest.NewUnstartedServer(h2c.NewHandler(mux, &http2.Server{}))
	srv.EnableHTTP2 = true
	srv.Start()
	t.Cleanup(srv.Close)
	return daemonGateway{url: srv.URL, tlsMode: bootstrap.TLSModeH2C}
}

func quietCommand() *cobra.Command {
	cmd := &cobra.Command{}
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	return cmd
}

// THE regression. `daemon start --token` with no --account put every account
// on a laptop in one `_default` instance, so they shared one saved daemon id,
// and the second account was refused as "owned by another user".
//
// The pasted token names its account (the gateway says whose it is), and that
// account is the instance key: two accounts, two instances, two identities —
// side by side, even in the same workspace.
func TestPastedTokensFromTwoAccountsGetSeparateIdentities(t *testing.T) {
	isolateInstanceEnv(t)
	worktree := initGitRepo(t)
	gateway := serveDaemonGateway(t, &whoAmIGateway{owners: map[string]string{
		"rlat_token-a": "user-a",
		"rlat_token-b": "user-b",
	}})
	conn := &connection{ServerURL: testServerURL, GatewayURL: gateway.url}

	accountA, verifiedA, err := accountForPastedToken(context.Background(), quietCommand(), conn, gateway, "rlat_token-a", false)
	require.NoError(t, err)
	accountB, verifiedB, err := accountForPastedToken(context.Background(), quietCommand(), conn, gateway, "rlat_token-b", false)
	require.NoError(t, err)

	require.Equal(t, "user-a", accountA, "the token's owner is the instance account")
	require.Equal(t, "user-b", accountB)
	require.True(t, verifiedA && verifiedB, "an identified token was verified by the gateway")

	dirA := resolveFrom(t, worktree, daemonInstanceFlags{account: accountA})
	dirB := resolveFrom(t, worktree, daemonInstanceFlags{account: accountB})
	require.NotEqual(t, dirA, dirB, "two accounts in one workspace must not share a daemon identity")

	require.NoError(t, bootstrap.WriteDaemonID(dirA, "daemon-of-a"))
	require.NoError(t, bootstrap.WriteDaemonID(dirB, "daemon-of-b"))
	require.Equal(t, "daemon-of-a", bootstrap.ReadDaemonID(dirA))
	require.Equal(t, "daemon-of-b", bootstrap.ReadDaemonID(dirB))
}

// A token the gateway refuses is a token problem, and an interactive user
// hears that before anything is written. A non-interactive start (a container)
// proceeds unidentified instead: the runtime retries Unauthenticated, because
// what refuses a good token is usually a gateway mid-rollout.
func TestPastedTokenTheGatewayRefusesIsReportedAsATokenProblem(t *testing.T) {
	isolateInstanceEnv(t)
	gateway := serveDaemonGateway(t, &whoAmIGateway{owners: map[string]string{}})
	conn := &connection{ServerURL: testServerURL, GatewayURL: gateway.url}

	_, _, err := accountForPastedToken(context.Background(), quietCommand(), conn, gateway, "rlat_revoked", false)
	require.Error(t, err)
	require.Contains(t, err.Error(), "token rejected")

	account, verified, err := accountForPastedToken(context.Background(), quietCommand(), conn, gateway, "rlat_revoked", true)
	require.NoError(t, err)
	require.Empty(t, account)
	require.False(t, verified)
}

// A gateway that cannot identify tokens — an older build, or one that is down —
// must not stop the daemon from starting. It falls back to the shared default
// identity, which the runtime's foreign-id recovery keeps from wedging.
func TestPastedTokenUnidentifiedFallsBackToDefaultIdentity(t *testing.T) {
	isolateInstanceEnv(t)
	gateway := serveDaemonGateway(t, &reliantv1connect.UnimplementedToolsDaemonServiceHandler{})
	conn := &connection{ServerURL: testServerURL, GatewayURL: gateway.url}

	account, verified, err := accountForPastedToken(context.Background(), quietCommand(), conn, gateway, "rlat_token-a", false)
	require.NoError(t, err)
	require.Empty(t, account)
	require.False(t, verified)
}

// `daemon start --token` stores the credential under its owner. Every later
// no-flag command — start without --token, status, stop, logs — must land on
// that same instance, or stop reports "No daemon running" over a live one.
func TestDaemonCommandsFollowTheStoredCredentialsAccount(t *testing.T) {
	isolateInstanceEnv(t)
	worktree := initGitRepo(t)

	require.Empty(t, resolveInstanceAccount("", testServerURL), "no credential: the default instance")

	require.NoError(t, auth.WriteDaemonCredentials(&auth.DaemonCredentials{
		PAT: "rlat_token-a", ServerURL: testServerURL, Sub: "user-a",
	}))
	require.Equal(t, "user-a", resolveInstanceAccount("", testServerURL))
	require.Equal(t, "user-z", resolveInstanceAccount("user-z", testServerURL), "an explicit --account wins")

	started := resolveFrom(t, worktree, daemonInstanceFlags{account: "user-a"})
	noFlags := resolveFrom(t, worktree, daemonInstanceFlags{})
	require.Equal(t, started, noFlags, "no-flag commands must address the instance --token started")

	// A credential with no known owner keeps the default instance.
	require.NoError(t, auth.WriteDaemonCredentials(&auth.DaemonCredentials{
		PAT: "rlat_token-unknown", ServerURL: testServerURL,
	}))
	require.Empty(t, resolveInstanceAccount("", testServerURL))
}

// "daemon id is owned by another user" is not a token problem. Calling it one
// sent a user to re-mint a PAT that was fine; and without --token the old
// mapping deleted the good credential and opened a browser login.
func TestDaemonStartFailure_ForeignDaemonIDIsNotATokenProblem(t *testing.T) {
	conn := &connection{ServerURL: "https://api.reliantapi.com", GatewayURL: "https://gateway.reliantapi.com"}
	foreign := fmt.Errorf("daemon connection failed (not retrying): %w",
		connect.NewError(connect.CodePermissionDenied, errors.New("daemon id is owned by another user")))

	for _, useToken := range []bool{true, false} {
		failure, reRegister := daemonStartFailure(foreign, useToken, conn)
		require.False(t, reRegister, "useToken=%v: a foreign daemon id must never discard the credential and re-run login", useToken)
		require.Error(t, failure)
		require.NotContains(t, failure.Error(), "token rejected", "useToken=%v", useToken)
		require.NotContains(t, failure.Error(), "verify the PAT", "useToken=%v", useToken)
		require.Contains(t, failure.Error(), "owned by another user", "useToken=%v: the real cause must reach the user", useToken)
	}

	unauthenticated := connect.NewError(connect.CodeUnauthenticated, errors.New("invalid daemon auth token"))
	failure, reRegister := daemonStartFailure(unauthenticated, true, conn)
	require.False(t, reRegister)
	require.Contains(t, failure.Error(), "token rejected", "a refused --token IS a token problem")

	failure, reRegister = daemonStartFailure(unauthenticated, false, conn)
	require.True(t, reRegister, "a refused stored credential is re-minted")
	require.NoError(t, failure)

	unavailable := connect.NewError(connect.CodeUnavailable, errors.New("gateway restarting"))
	failure, reRegister = daemonStartFailure(unavailable, true, conn)
	require.False(t, reRegister)
	require.NotContains(t, failure.Error(), "token rejected")
}
