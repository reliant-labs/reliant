package observe

import (
	"context"
	"net/http"
	"testing"

	"connectrpc.com/connect"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"google.golang.org/protobuf/types/known/emptypb"

	"github.com/reliant-labs/forge/pkg/authn"
)

func TestSessionIDFromBaggage(t *testing.T) {
	for _, tc := range []struct {
		name    string
		baggage []string
		want    string
	}{
		{"absent", nil, ""},
		{"member", []string{"session.id=91b955b313a1a86644ea53730a92deab"}, "91b955b313a1a86644ea53730a92deab"},
		{"among others", []string{"a=1,session.id=abc_DEF-9", "b=2"}, "abc_DEF-9"},
		{"second header", []string{"a=1", "session.id=s2"}, "s2"},
		{"not a session id", []string{"session.id=a%20b"}, ""},
		{"too long", []string{"session.id=" + longID()}, ""},
		{"malformed baggage", []string{"session.id"}, ""},
		{"beside a malformed member", []string{"bad member,session.id=s3", "=oops"}, "s3"},
		{"malformed member after", []string{"session.id=s4,no-equals"}, "s4"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := http.Header{}
			for _, v := range tc.baggage {
				h.Add("baggage", v)
			}
			if got := SessionIDFromBaggage(h); got != tc.want {
				t.Fatalf("SessionIDFromBaggage(%q) = %q, want %q", tc.baggage, got, tc.want)
			}
		})
	}
}

func longID() string {
	b := make([]byte, 129)
	for i := range b {
		b[i] = 'a'
	}
	return string(b)
}

// The server span carries the baggage's session.id and the authenticated
// user's id; the inner span gets the user id too; nothing reads a user id from
// the request.
func TestRequestAndIdentitySpans(t *testing.T) {
	rec := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(rec))
	tracer := tp.Tracer("test")

	var sawCtx context.Context
	handler := connect.UnaryFunc(func(ctx context.Context, _ connect.AnyRequest) (connect.AnyResponse, error) {
		sawCtx = ctx
		return connect.NewResponse(&emptypb.Empty{}), nil
	})
	// server span → RequestSpan → inner span → (auth) → IdentitySpan → handler
	inner := TracingInterceptor(tracer)
	auth := connect.UnaryInterceptorFunc(func(next connect.UnaryFunc) connect.UnaryFunc {
		return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
			return next(authn.ContextWithClaims(ctx, &authn.Claims{UserID: "u-42"}), req)
		}
	})
	call := RequestSpanInterceptor().WrapUnary(inner.WrapUnary(auth.WrapUnary(IdentitySpanInterceptor(nil).WrapUnary(handler))))

	req := connect.NewRequest(&emptypb.Empty{})
	req.Header().Set("baggage", "session.id=91b955b313a1a86644ea53730a92deab")
	req.Header().Set("x-user-id", "spoofed")
	ctx, server := tracer.Start(context.Background(), "server")
	if _, err := call(ctx, req); err != nil {
		t.Fatal(err)
	}
	server.End()
	if sawCtx == nil {
		t.Fatal("handler not reached")
	}

	attrs := map[string]map[string]string{}
	for _, s := range rec.Ended() {
		m := map[string]string{}
		for _, kv := range s.Attributes() {
			m[string(kv.Key)] = kv.Value.Emit()
		}
		attrs[s.Name()] = m
	}
	srv := attrs["server"]
	if srv[AttrSessionID] != "91b955b313a1a86644ea53730a92deab" || srv[AttrUserID] != "u-42" {
		t.Fatalf("server span attributes = %v, want session.id and user.id", srv)
	}
	in := attrs[req.Spec().Procedure]
	if in[AttrUserID] != "u-42" {
		t.Fatalf("inner span attributes = %v, want user.id", in)
	}
	if _, ok := in[AttrSessionID]; ok {
		t.Fatalf("inner span carries session.id; only the server span should: %v", in)
	}
}

// An anonymous request gets no user.id, and a missing baggage no session.id.
func TestIdentitySpanAnonymous(t *testing.T) {
	rec := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(rec))
	ctx, span := tp.Tracer("test").Start(context.Background(), "server")
	handler := connect.UnaryFunc(func(context.Context, connect.AnyRequest) (connect.AnyResponse, error) {
		return connect.NewResponse(&emptypb.Empty{}), nil
	})
	call := RequestSpanInterceptor().WrapUnary(IdentitySpanInterceptor(nil).WrapUnary(handler))
	if _, err := call(ctx, connect.NewRequest(&emptypb.Empty{})); err != nil {
		t.Fatal(err)
	}
	span.End()
	if got := rec.Ended()[0].Attributes(); len(got) != 0 {
		t.Fatalf("anonymous span attributes = %v, want none", got)
	}
}
