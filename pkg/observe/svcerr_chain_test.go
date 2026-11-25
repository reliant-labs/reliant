package observe_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/structpb"

	"github.com/reliant-labs/forge/pkg/observe"
	"github.com/reliant-labs/forge/pkg/svcerr"
)

// callThrough serves a handler that returns handlerErr behind interceptors and
// returns what a real client receives.
func callThrough(t *testing.T, interceptors []connect.Interceptor, handlerErr error) error {
	t.Helper()
	const procedure = "/observe.test.v1.SvcerrService/Do"
	mux := http.NewServeMux()
	mux.Handle(procedure, connect.NewUnaryHandler(procedure,
		func(context.Context, *connect.Request[structpb.Struct]) (*connect.Response[structpb.Struct], error) {
			return nil, handlerErr
		},
		connect.WithInterceptors(interceptors...),
	))
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	client := connect.NewClient[structpb.Struct, structpb.Struct](srv.Client(), srv.URL+procedure)
	_, err := client.CallUnary(t.Context(), connect.NewRequest(&structpb.Struct{}))
	return err
}

func TestChain_SvcerrReachesClientWithCode(t *testing.T) {
	builders := map[string][]connect.Interceptor{
		"Chain":              observe.Chain(observe.Deps{}),
		"DefaultMiddlewares": observe.DefaultMiddlewares(observe.DefaultMiddlewareDeps{}),
	}
	for name, chain := range builders {
		t.Run(name, func(t *testing.T) {
			err := callThrough(t, chain, svcerr.PermissionDenied("admins only"))
			if got := connect.CodeOf(err); got != connect.CodePermissionDenied {
				t.Fatalf("code = %v (err %v), want permission_denied", got, err)
			}
		})
	}
}

func TestChain_SvcerrInstalledTwiceStaysCorrect(t *testing.T) {
	chain := observe.Chain(observe.Deps{Extras: []connect.Interceptor{svcerr.Interceptor(), svcerr.Interceptor()}})
	n := 0
	for _, i := range chain {
		if svcerr.IsInterceptor(i) {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("chain holds %d svcerr interceptors, want exactly 1", n)
	}
	if !svcerr.IsInterceptor(chain[len(chain)-1]) {
		t.Fatalf("svcerr interceptor is not innermost: last = %T", chain[len(chain)-1])
	}

	for name, handlerErr := range map[string]error{
		"sentinel":     svcerr.NotFound("widget"),
		"wrapped":      svcerr.Wrap(svcerr.NotFound("widget")),
		"connect":      connect.NewError(connect.CodeAlreadyExists, errString("dup")),
		"unrecognised": errString("pq: relation does not exist"),
	} {
		t.Run(name, func(t *testing.T) {
			single := callThrough(t, observe.Chain(observe.Deps{}), handlerErr)
			double := callThrough(t, chain, handlerErr)
			if connect.CodeOf(single) != connect.CodeOf(double) {
				t.Fatalf("code differs: single %v, double %v", connect.CodeOf(single), connect.CodeOf(double))
			}
			var s, d *connect.Error
			if !asConnect(single, &s) || !asConnect(double, &d) {
				t.Fatalf("not connect errors: %v / %v", single, double)
			}
			if s.Message() != d.Message() {
				t.Fatalf("message differs: single %q, double %q", s.Message(), d.Message())
			}
		})
	}
}

// Applying the interceptor twice (a hand-built list no builder deduplicates)
// must not change what the client sees: Wrap passes a *connect.Error through.
func TestSvcerrInterceptor_AppliedTwiceIsHarmless(t *testing.T) {
	err := callThrough(t, []connect.Interceptor{svcerr.Interceptor(), svcerr.Interceptor()}, svcerr.PermissionDenied("admins only"))
	if got := connect.CodeOf(err); got != connect.CodePermissionDenied {
		t.Fatalf("code = %v (err %v), want permission_denied", got, err)
	}
	var ce *connect.Error
	if !asConnect(err, &ce) || ce.Message() != "admins only" {
		t.Fatalf("message mangled by double application: %v", err)
	}
}

// The interceptor is innermost: an interceptor outside it sees the wire
// error, not the sentinel.
func TestChain_OuterInterceptorsSeeWireError(t *testing.T) {
	var sawConnect atomic.Bool
	spy := spyInterceptor{onErr: func(err error) {
		if _, ok := err.(*connect.Error); ok {
			sawConnect.Store(true)
		}
	}}
	chain := observe.Chain(observe.Deps{Extras: []connect.Interceptor{spy}})
	_ = callThrough(t, chain, svcerr.PermissionDenied("nope"))
	if !sawConnect.Load() {
		t.Fatal("an Extras interceptor saw the raw sentinel; svcerr must sit inside every Extra")
	}
}

type errString string

func (e errString) Error() string { return string(e) }

func asConnect(err error, target **connect.Error) bool {
	ce, ok := err.(*connect.Error)
	*target = ce
	return ok
}

type spyInterceptor struct{ onErr func(error) }

func (s spyInterceptor) WrapUnary(next connect.UnaryFunc) connect.UnaryFunc {
	return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
		resp, err := next(ctx, req)
		if err != nil {
			s.onErr(err)
		}
		return resp, err
	}
}

func (s spyInterceptor) WrapStreamingClient(next connect.StreamingClientFunc) connect.StreamingClientFunc {
	return next
}

func (s spyInterceptor) WrapStreamingHandler(next connect.StreamingHandlerFunc) connect.StreamingHandlerFunc {
	return next
}
