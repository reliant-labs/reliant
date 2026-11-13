package serverkit_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"connectrpc.com/connect"
	"github.com/reliant-labs/forge/pkg/serverkit"
	"google.golang.org/protobuf/types/known/emptypb"
)

type outerContextKey struct{}
type contextPrelude struct{ calls *int }

func (p contextPrelude) WrapUnary(next connect.UnaryFunc) connect.UnaryFunc {
	return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
		*p.calls++
		return next(context.WithValue(ctx, outerContextKey{}, true), req)
	}
}
func (contextPrelude) WrapStreamingClient(next connect.StreamingClientFunc) connect.StreamingClientFunc {
	return next
}
func (contextPrelude) WrapStreamingHandler(next connect.StreamingHandlerFunc) connect.StreamingHandlerFunc {
	return next
}

func TestChainOuterPrecedesTraceAndAuthOnce(t *testing.T) {
	var order []string
	calls := 0
	check := connect.UnaryInterceptorFunc(func(next connect.UnaryFunc) connect.UnaryFunc {
		return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
			if ctx.Value(outerContextKey{}) != true {
				t.Error("trace/auth could not see prepared context")
			}
			order = append(order, "trace")
			return next(ctx, req)
		}
	})
	outer := []connect.Interceptor{contextPrelude{&calls}}
	chain, err := serverkit.NewChain(serverkit.ChainDeps{
		Auth: serverkit.AuthFor(serverkit.AudienceUser, tokenAuth{token: "t"}), Outer: outer, Trace: check,
	})
	if err != nil {
		t.Fatal(err)
	}
	outer[0] = nil // construction snapshots the slice, not a caller's mutable registration
	const procedure = "/test.v1.PingService/Ping"
	mux := http.NewServeMux()
	mux.Handle(procedure, connect.NewUnaryHandler(procedure, func(context.Context, *connect.Request[emptypb.Empty]) (*connect.Response[emptypb.Empty], error) {
		order = append(order, "handler")
		return connect.NewResponse(&emptypb.Empty{}), nil
	}, chain.HandlerOptions()...))
	srv := httptest.NewServer(mux)
	defer srv.Close()
	client := connect.NewClient[emptypb.Empty, emptypb.Empty](srv.Client(), srv.URL+procedure)
	req := connect.NewRequest(&emptypb.Empty{})
	req.Header().Set("Authorization", "Bearer t")
	if _, err := client.CallUnary(t.Context(), req); err != nil {
		t.Fatal(err)
	}
	if _, err := client.CallUnary(t.Context(), connect.NewRequest(&emptypb.Empty{})); connect.CodeOf(err) != connect.CodeUnauthenticated {
		t.Fatalf("outer bypassed auth: %v", err)
	}
	if calls != 2 || !reflect.DeepEqual(order, []string{"trace", "handler", "trace"}) {
		t.Fatalf("calls=%d order=%v", calls, order)
	}
}

func TestChainOuterCannotReplaceRequiredAuth(t *testing.T) {
	calls := 0
	if _, err := serverkit.NewChain(serverkit.ChainDeps{Outer: []connect.Interceptor{contextPrelude{&calls}}}); err == nil {
		t.Fatal("outer bypassed missing auth")
	}
	for _, x := range []connect.Interceptor{nil, serverkit.NoTrace} {
		if _, err := serverkit.NewChain(serverkit.ChainDeps{Auth: serverkit.AuthFor(serverkit.AudienceUser, tokenAuth{token: "t"}), Outer: []connect.Interceptor{x}}); err == nil {
			t.Fatalf("invalid outer accepted: %v", x)
		}
	}
}
