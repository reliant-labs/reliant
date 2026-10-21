// Copyright (c) 2025 Reliant Labs
package grpc

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	"connectrpc.com/otelconnect"
	"github.com/reliant-labs/forge/pkg/serverkit"
	"github.com/reliant-labs/forge/pkg/svcerr"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"
	"google.golang.org/protobuf/types/known/emptypb"

	"github.com/reliant-labs/reliant/internal/grpc/interceptors"
	"github.com/reliant-labs/reliant/internal/handlers/forge"
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
// profile labels, the traceresponse header, recovery, request id, logging, tracing, metrics), reliant's
// budgets in forge's Deadline slot, the authenticator, then reliant's domain
// whitelist, forge's activity stream, its stream drain, and svcerr's
// interceptor innermost. There is no error reporter: a failed RPC reaches
// Sentry once, as forge's "rpc failed" log line.
func TestUserChainOrder(t *testing.T) {
	chain, err := NewUserChain(UserChainDeps{Auth: &testNamedInterceptor{}})
	require.NoError(t, err)
	require.Equal(t, serverkit.AudienceUser, chain.Audience())

	got := chain.Interceptors()
	require.Len(t, got, 14)
	require.IsType(t, &otelconnect.Interceptor{}, got[0], "the trace span opens outermost")
	require.IsType(t, serverkit.Deadline{}, got[8], "the deadline wraps authentication")
	require.IsType(t, &interceptors.DomainWhitelistInterceptor{}, got[10], "the domain whitelist reads the user auth stashed")
	require.Equal(t, reflect.TypeOf(serverkit.StreamDrainInterceptor()), reflect.TypeOf(got[12]),
		"the stream drain sits outside svcerr, so it judges the code the client receives")
	require.Equal(t, reflect.TypeOf(svcerr.Interceptor()), reflect.TypeOf(got[13]),
		"svcerr is innermost, so logging records the status the client received")
}

// userChainDeadline is the Deadline slot NewUserChain configured.
func userChainDeadline(t *testing.T) serverkit.Deadline {
	t.Helper()
	chain, err := NewUserChain(UserChainDeps{Auth: &testNamedInterceptor{}})
	require.NoError(t, err)
	for _, ic := range chain.Interceptors() {
		if d, ok := ic.(serverkit.Deadline); ok {
			return d
		}
	}
	t.Fatal("the user chain has no Deadline slot")
	return serverkit.Deadline{}
}

// The user chain's budgets are reliant's, not forge's defaults: 10s for an
// unnamed unary RPC (forge's would be 30s), reliant's per-procedure table
// merged with the forge daemon commands' dispatch budgets, and reliant's
// slow-RPC reporter.
func TestUserChainDeadlineIsReliants(t *testing.T) {
	d := userChainDeadline(t)
	require.Equal(t, interceptors.UnaryTimeout, d.Unary)
	require.Equal(t, 10*time.Second, d.Unary)
	require.NotNil(t, d.OnSlow, "slow RPCs report as [SlowRPC] and reliant_grpc_slow_rpc_total")
	require.Equal(t, interceptors.ProcedureTimeouts(forge.ForgeRPCDeadlines()), d.Procedures)
	for procedure, budget := range forge.ForgeRPCDeadlines() {
		require.Equal(t, budget, d.Procedures[procedure], procedure)
	}
}

// A positive Procedures entry bounds a streaming rpc too, so every entry of
// the user chain's table must name a unary rpc, or be 0 (no deadline). It
// must also name an rpc that exists: serverkit only checks a key's shape.
func TestUserChainDeadlineTableNamesUnaryRPCs(t *testing.T) {
	for procedure, budget := range userChainDeadline(t).Procedures {
		name := protoreflect.FullName(strings.ReplaceAll(strings.TrimPrefix(procedure, "/"), "/", "."))
		desc, err := protoregistry.GlobalFiles.FindDescriptorByName(name)
		require.NoError(t, err, "%s names no rpc", procedure)
		method, ok := desc.(protoreflect.MethodDescriptor)
		require.True(t, ok, "%s is not an rpc", procedure)
		if method.IsStreamingClient() || method.IsStreamingServer() {
			require.LessOrEqual(t, budget, time.Duration(0), "%s is a streaming rpc: a positive budget would cut every stream", procedure)
		}
	}
}

// A caller's longer deadline does not lift the server's budget: a handler
// called with an hour's client deadline on an unnamed unary RPC is given the
// user chain's 10s, not the hour (the timeout reliant had before applied its
// budget only when the caller sent none).
func TestUserChainBudgetIsACeilingOverTheCallersDeadline(t *testing.T) {
	given := make(chan time.Duration, 1)
	chain, err := NewUserChain(UserChainDeps{Auth: &testNamedInterceptor{}})
	require.NoError(t, err)
	handler := connect.NewUnaryHandler(echoProcedure, func(ctx context.Context, _ *connect.Request[emptypb.Empty]) (*connect.Response[emptypb.Empty], error) {
		deadline, _ := ctx.Deadline()
		given <- time.Until(deadline)
		return connect.NewResponse(&emptypb.Empty{}), nil
	}, chain.HandlerOptions()...)
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)

	ctx, cancel := context.WithTimeout(context.Background(), time.Hour)
	defer cancel()
	client := connect.NewClient[emptypb.Empty, emptypb.Empty](http.DefaultClient, server.URL+echoProcedure)
	_, err = client.CallUnary(ctx, connect.NewRequest(&emptypb.Empty{}))
	require.NoError(t, err)
	require.LessOrEqual(t, <-given, interceptors.UnaryTimeout)
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

// What a client reads for each kind of error a handler returns, now that
// forge's chain ends in svcerr.Interceptor: a bare svcerr sentinel gets its
// own code (connect-go alone sent it as unknown), a *connect.Error passes
// through untouched, and an error nothing recognises is internal with
// svcerr.InternalMessage rather than unknown with its raw text.
func TestUserChainWireCodes(t *testing.T) {
	cases := map[string]struct {
		err      error
		wantCode connect.Code
		wantMsg  string
	}{
		"bare svcerr":     {svcerr.PermissionDenied("admins only"), connect.CodePermissionDenied, "admins only"},
		"connect error":   {connect.NewError(connect.CodeFailedPrecondition, errors.New("chat has not started; call StartChat")), connect.CodeFailedPrecondition, "chat has not started; call StartChat"},
		"wrapped connect": {fmt.Errorf("send: %w", connect.NewError(connect.CodeNotFound, errors.New("chat not found"))), connect.CodeNotFound, "chat not found"},
		"unrecognised":    {errors.New(`pq: relation "chats" does not exist`), connect.CodeInternal, svcerr.InternalMessage},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			handler := connect.NewUnaryHandler(echoProcedure, func(context.Context, *connect.Request[emptypb.Empty]) (*connect.Response[emptypb.Empty], error) {
				return nil, tc.err
			}, connect.WithInterceptors(userChainInterceptors(t)...))
			server := httptest.NewServer(handler)
			t.Cleanup(server.Close)

			client := connect.NewClient[emptypb.Empty, emptypb.Empty](http.DefaultClient, server.URL+echoProcedure)
			_, err := client.CallUnary(context.Background(), connect.NewRequest(&emptypb.Empty{}))
			var ce *connect.Error
			require.ErrorAs(t, err, &ce)
			require.Equal(t, tc.wantCode, ce.Code())
			require.Equal(t, tc.wantMsg, ce.Message())
		})
	}
}

func TestNewUserChainRequiresAuth(t *testing.T) {
	_, err := NewUserChain(UserChainDeps{})
	require.ErrorContains(t, err, "Auth is required")
}
