// Copyright (c) 2025 Reliant Labs
package grpc

import (
	"context"
	"errors"
	"testing"

	"connectrpc.com/connect/v2"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/forge/pkg/svcerr"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/gen/reliant/v1/reliantv1connect"
	"github.com/reliant-labs/reliant/internal/rpcclient"
)

// The forge RPC surface's server-side kill switch (ForgeAPIDisabled, which the
// api root mounts ForgeService with under RELIANT_DISABLE_FORGE_API).
//
// WHY IT EXISTS ALONGSIDE THE FRONTEND FLAG. The web app gates the forge UI on a
// per-browser localStorage flag, which is not a security boundary: anyone who
// can craft a Connect request bypasses it. StartDeploy applies manifests to a
// live cluster, so the operator needs a way to refuse the surface at the
// server no matter what any browser believes.
//
// WHY MOUNTED AND REFUSING, NOT UNMOUNTED. An unmounted service answers 404,
// which the web files as version skew. A FailedPrecondition carrying the
// forge_api_disabled reason is an answer the web can show as "turned off".

func forgeClient(baseURL string) reliantv1connect.ForgeServiceClient {
	return reliantv1connect.NewForgeServiceClient(rpcclient.New(h2cClient(), baseURL, nil))
}

// authed is ctx carrying the test server's API-key caller's credential.
func authed(ctx context.Context) context.Context {
	ctx, info := connect.NewClientContext(ctx)
	info.RequestHeader().Set("Authorization", "Bearer "+drainTestAPIKey)
	return ctx
}

func requireForgeAPIDisabled(t *testing.T, err error) {
	t.Helper()
	var connectErr *connect.Error
	require.True(t, errors.As(err, &connectErr), "want a Connect error, got %v", err)
	require.Equal(t, connect.CodeFailedPrecondition, connectErr.Code(), "got %v", err)
	require.Contains(t, connectErr.Message(), "the forge API is disabled in this deployment")
	require.Equal(t, "the forge API is disabled in this deployment (RELIANT_DISABLE_FORGE_API)", connectErr.Message(),
		"byte-identical: clients from before the reason match on it")
	require.Equal(t, "forge_api_disabled", svcerr.MetaOf(err).Get(svcerr.ReasonHeader),
		"the reason is how the web tells this apart from forge failing")
	require.Empty(t, svcerr.MetaOf(err).Get("X-Reliant-Reason"), "the pre-svcerr header is retired (R3w2)")
}

func TestDisabledForgeAPIAnswersFailedPrecondition(t *testing.T) {
	_, baseURL := startTestServer(t, testForge(t, ForgeAPIDisabled()))
	forge := forgeClient(baseURL)

	t.Run("a unary rpc, before its handler runs", func(t *testing.T) {
		// An empty project_id is refused by the handler (InvalidArgument), so
		// FailedPrecondition proves the handler never ran.
		_, err := forge.GetTopology(authed(context.Background()), &reliantv1.GetForgeTopologyRequest{})
		requireForgeAPIDisabled(t, err)
	})
	t.Run("the deploy path", func(t *testing.T) {
		_, err := forge.StartDeploy(authed(context.Background()), &reliantv1.StartForgeDeployRequest{})
		requireForgeAPIDisabled(t, err)
	})
	t.Run("after authentication", func(t *testing.T) {
		// An anonymous caller learns nothing about how this deployment is
		// configured: it is refused for its missing credential first.
		_, err := forge.GetTopology(context.Background(), &reliantv1.GetForgeTopologyRequest{})
		require.Equal(t, connect.CodeUnauthenticated, svcerr.CodeV2(err), "got %v", err)
	})
}

// ForgeService has no streaming rpc today; the guard refuses one all the same,
// so adding one cannot open a hole in the switch.
func TestDisabledForgeAPIRefusesStreamingHandlers(t *testing.T) {
	called := false
	handler := forgeAPIDisabled{}.intercept(func(context.Context, connect.Spec, connect.ServerStream) error {
		called = true
		return nil
	})
	requireForgeAPIDisabled(t, handler(context.Background(), connect.Spec{StreamType: connect.StreamTypeServer}, nil))
	require.False(t, called, "the wrapped handler must not run")
}

func TestEnabledForgeAPIReachesItsHandlers(t *testing.T) {
	_, baseURL := startTestServer(t, testForge(t))

	_, err := forgeClient(baseURL).GetTopology(authed(context.Background()), &reliantv1.GetForgeTopologyRequest{})
	require.Equal(t, connect.CodeInvalidArgument, svcerr.CodeV2(err),
		"the handler's own answer to an empty project_id; got %v", err)
}
