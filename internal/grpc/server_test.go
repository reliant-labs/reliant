// Copyright (c) 2025 Reliant Labs
package grpc

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"reflect"
	"regexp"
	"runtime"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect/v2"
	"github.com/reliant-labs/forge/pkg/serverkit"
	"github.com/reliant-labs/forge/pkg/svcerr"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"

	"github.com/reliant-labs/reliant/internal/grpc/interceptors"
	"github.com/reliant-labs/reliant/internal/handlers/forge"
)

// userChainInterceptors is the API listener's chain (NewUserChain) with auth
// passed through, for tests that serve a handler behind it.
func userChainInterceptors(t *testing.T) []connect.ServerInterceptor {
	t.Helper()
	chain, err := NewUserChain(UserChainDeps{Auth: passAuth})
	require.NoError(t, err)
	return chain.ServerInterceptors()
}

// funcName names the function an interceptor is: a v2 interceptor is a func,
// which has no type of its own to tell one from another, but every
// interceptor a constructor returns is a closure named after it. The
// closure's own suffix (".func1", or ".1" for a copy the compiler inlined
// elsewhere) is dropped, so two closures of one constructor compare equal.
func funcName(f any) string {
	name := runtime.FuncForPC(reflect.ValueOf(f).Pointer()).Name()
	return closureSuffix.ReplaceAllString(name, "")
}

var closureSuffix = regexp.MustCompile(`(\.func\d+|\.\d+)+$`)

// The API listener's chain, outermost first: forge's layers (the trace span,
// profile labels, the traceresponse header, the request span that carries the
// browser's session.id, recovery, request id, logging, tracing, metrics),
// reliant's budgets in forge's Deadline slot, the authenticator and forge's
// identity stamp after it (a no-op here: reliant's auth binds user.id itself,
// through interceptors.RequestScope), then reliant's domain whitelist, forge's
// activity stream, its stream drain, and svcerr's interceptor innermost. There is no error reporter: a failed RPC reaches
// Sentry once, as forge's "rpc failed" log line.
func TestUserChainOrder(t *testing.T) {
	chain, err := NewUserChain(UserChainDeps{Auth: passAuth})
	require.NoError(t, err)
	require.Equal(t, serverkit.AudienceUser, chain.Audience())

	got := chain.ServerInterceptors()
	// 17 since forge #2068 (via #2065): observe.Chain installs the resumed
	// sign-in heartbeat (auth_signed_in{kind=resumed}) right after auth and
	// its identity span, ahead of the domain whitelist.
	require.Len(t, got, 17)
	require.Equal(t, funcName(interceptors.RequestScope(passAuth)), funcName(got[0]), "the trace span opens outermost, inside a request scope")
	require.Equal(t, funcName(serverkit.Deadline{}.InterceptorV2()), funcName(got[9]), "the deadline wraps authentication")
	require.Equal(t, funcName(passAuth), funcName(got[10]), "the authenticator runs inside the deadline")
	require.Equal(t, funcName((&interceptors.DomainWhitelistInterceptor{}).Intercept), funcName(got[13]), "the domain whitelist reads the user auth stashed")
	require.Equal(t, funcName(serverkit.StreamDrainInterceptorV2()), funcName(got[15]),
		"the stream drain sits outside svcerr, so it judges the code the client receives")
	require.Equal(t, funcName(svcerr.InterceptorV2()), funcName(got[16]),
		"svcerr is innermost, so logging records the status the client received")
}

// The user chain's budgets are reliant's, not forge's defaults: 10s for an
// unnamed unary RPC (forge's would be 30s), reliant's per-procedure table
// merged with the forge daemon commands' dispatch budgets, and reliant's
// slow-RPC reporter.
func TestUserChainDeadlineIsReliants(t *testing.T) {
	d := userChainDeadline()
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
	for procedure, budget := range userChainDeadline().Procedures {
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
	url := serveUnary(t, echoProcedure, func(ctx context.Context) error {
		deadline, _ := ctx.Deadline()
		given <- time.Until(deadline)
		return nil
	}, userChainInterceptors(t)...)

	ctx, cancel := context.WithTimeout(context.Background(), time.Hour)
	defer cancel()
	require.NoError(t, callUnary(ctx, http.DefaultClient, url, echoProcedure))
	require.LessOrEqual(t, <-given, interceptors.UnaryTimeout)
}

// The per-RPC deadline wraps authentication, as it always has: an `rlat_`
// introspection or the token store's query runs inside the RPC's deadline,
// not unbounded ahead of it. forge's chain runs Extras after Auth, which is
// why NewUserChain puts the timeout in the Auth slot.
func TestUserChainBoundsAuthenticationByTheRPCDeadline(t *testing.T) {
	var authHadDeadline bool
	auth := func(next connect.ServerFunc) connect.ServerFunc {
		return func(ctx context.Context, spec connect.Spec, stream connect.ServerStream) error {
			_, authHadDeadline = ctx.Deadline()
			return next(ctx, spec, stream)
		}
	}
	chain, err := NewUserChain(UserChainDeps{Auth: auth})
	require.NoError(t, err)

	url := serveUnary(t, echoProcedure, func(context.Context) error { return nil }, chain.ServerInterceptors()...)
	require.NoError(t, callUnary(context.Background(), http.DefaultClient, url, echoProcedure))
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
		"connect error":   {connect.NewError(connect.CodeFailedPrecondition, "chat has not started; call StartChat"), connect.CodeFailedPrecondition, "chat has not started; call StartChat"},
		"wrapped connect": {fmt.Errorf("send: %w", connect.NewError(connect.CodeNotFound, "chat not found")), connect.CodeNotFound, "chat not found"},
		"unrecognised":    {errors.New(`pq: relation "chats" does not exist`), connect.CodeInternal, svcerr.InternalMessage},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			url := serveUnary(t, echoProcedure, func(context.Context) error { return tc.err }, userChainInterceptors(t)...)
			err := callUnary(context.Background(), http.DefaultClient, url, echoProcedure)
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
