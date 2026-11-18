package svcerr_test

import (
	"testing"

	"connectrpc.com/connect"

	"github.com/reliant-labs/forge/pkg/svcerr"
)

func TestWithInterceptor_AppendsOneInnermostAndKeepsInput(t *testing.T) {
	other := connect.UnaryInterceptorFunc(func(next connect.UnaryFunc) connect.UnaryFunc { return next })
	in := []connect.Interceptor{svcerr.Interceptor(), other, svcerr.Interceptor()}

	out := svcerr.WithInterceptor(in)

	if len(out) != 2 || !svcerr.IsInterceptor(out[1]) || svcerr.IsInterceptor(out[0]) {
		t.Fatalf("out = %v, want [other, svcerr]", out)
	}
	if len(in) != 3 || !svcerr.IsInterceptor(in[0]) {
		t.Fatalf("input slice was modified: %v", in)
	}
	if got := svcerr.WithInterceptor(nil); len(got) != 1 || !svcerr.IsInterceptor(got[0]) {
		t.Fatalf("nil extras: %v, want [svcerr]", got)
	}
}
