package serverkit_test

import (
	"context"
	"testing"

	"connectrpc.com/connect"
	"github.com/reliant-labs/forge/pkg/serverkit"
)

type streamPrelude struct{ calls *int }

func (streamPrelude) WrapUnary(next connect.UnaryFunc) connect.UnaryFunc { return next }
func (streamPrelude) WrapStreamingClient(next connect.StreamingClientFunc) connect.StreamingClientFunc {
	return next
}
func (p streamPrelude) WrapStreamingHandler(next connect.StreamingHandlerFunc) connect.StreamingHandlerFunc {
	return func(ctx context.Context, conn connect.StreamingHandlerConn) error {
		*p.calls++
		return next(context.WithValue(ctx, outerContextKey{}, true), conn)
	}
}
func TestChainOuterStreamingPreparesContextOnce(t *testing.T) {
	calls := 0
	chain, err := serverkit.NewChain(serverkit.ChainDeps{Auth: serverkit.AuthFor(serverkit.AudienceUser, tokenAuth{token: "t"}), Outer: []connect.Interceptor{streamPrelude{&calls}}})
	if err != nil {
		t.Fatal(err)
	}
	// Exercise the actual first registered interceptor's streaming branch.
	// No fake stream protocol needed: the prelude only forwards the connection.
	wrapped := chain.Interceptors()[0].WrapStreamingHandler(func(ctx context.Context, _ connect.StreamingHandlerConn) error {
		if ctx.Value(outerContextKey{}) != true {
			t.Error("stream context missing")
		}
		return nil
	})
	if err := wrapped(t.Context(), nil); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatalf("stream prelude calls=%d", calls)
	}
}
