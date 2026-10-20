// Copyright (c) 2025 Reliant Labs
package grpc

import (
	"context"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"connectrpc.com/connect"
	"connectrpc.com/otelconnect"
	"github.com/reliant-labs/forge/pkg/serverkit"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/emptypb"

	"github.com/reliant-labs/reliant/internal/grpc/interceptors"
)

type testNamedInterceptor struct{}

func (i *testNamedInterceptor) WrapUnary(next connect.UnaryFunc) connect.UnaryFunc {
	return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
		return next(ctx, req)
	}
}
func (i *testNamedInterceptor) WrapStreamingClient(next connect.StreamingClientFunc) connect.StreamingClientFunc {
	return next
}
func (i *testNamedInterceptor) WrapStreamingHandler(next connect.StreamingHandlerFunc) connect.StreamingHandlerFunc {
	return func(ctx context.Context, conn connect.StreamingHandlerConn) error {
		return next(ctx, conn)
	}
}

// userChainInterceptors is the API listener's chain (NewUserChain) with auth
// passed through, for tests that serve a handler behind it.
func userChainInterceptors(t *testing.T) []connect.Interceptor {
	t.Helper()
	chain, err := NewUserChain(UserChainDeps{Auth: &testNamedInterceptor{}})
	require.NoError(t, err)
	return chain.Interceptors()
}

// The API listener's chain, outermost first: forge's layers (the trace span,
// profile labels, recovery, request id, logging, tracing, metrics), the auth
// slot, then reliant's domain whitelist and forge's stream drain innermost.
// There is no error reporter: a failed RPC reaches Sentry once, as forge's
// "rpc failed" log line.
func TestUserChainOrder(t *testing.T) {
	chain, err := NewUserChain(UserChainDeps{Auth: &testNamedInterceptor{}})
	require.NoError(t, err)
	require.Equal(t, serverkit.AudienceUser, chain.Audience())

	got := chain.Interceptors()
	require.Len(t, got, 10)
	require.IsType(t, &otelconnect.Interceptor{}, got[0], "the trace span opens outermost")
	require.IsType(t, &interceptors.DomainWhitelistInterceptor{}, got[8], "the domain whitelist reads the user auth stashed")
	require.Equal(t, reflect.TypeOf(serverkit.StreamDrainInterceptor()), reflect.TypeOf(got[9]),
		"the stream drain is innermost, so logging records the status the client received")
}

// The per-RPC deadline wraps authentication, as it always has: an `rlat_`
// introspection or the token store's query runs inside the RPC's deadline,
// not unbounded ahead of it. forge's chain runs Extras after Auth, which is
// why NewUserChain puts the timeout in the Auth slot.
func TestUserChainBoundsAuthenticationByTheRPCDeadline(t *testing.T) {
	var authHadDeadline bool
	auth := connect.UnaryInterceptorFunc(func(next connect.UnaryFunc) connect.UnaryFunc {
		return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
			_, authHadDeadline = ctx.Deadline()
			return next(ctx, req)
		}
	})
	chain, err := NewUserChain(UserChainDeps{Auth: auth})
	require.NoError(t, err)

	handler := connect.NewUnaryHandler(echoProcedure, func(context.Context, *connect.Request[emptypb.Empty]) (*connect.Response[emptypb.Empty], error) {
		return connect.NewResponse(&emptypb.Empty{}), nil
	}, chain.HandlerOptions()...)
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)

	client := connect.NewClient[emptypb.Empty, emptypb.Empty](http.DefaultClient, server.URL+echoProcedure)
	_, err = client.CallUnary(context.Background(), connect.NewRequest(&emptypb.Empty{}))
	require.NoError(t, err)
	require.True(t, authHadDeadline, "authentication must run inside the RPC's deadline")
}

func TestNewUserChainRequiresAuth(t *testing.T) {
	_, err := NewUserChain(UserChainDeps{})
	require.ErrorContains(t, err, "Auth is required")
}
