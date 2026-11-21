package authn

import (
	"context"
	"net/http"
	"testing"

	"connectrpc.com/connect"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	"github.com/reliant-labs/forge/pkg/auth"
)

// TestInterceptorNamesTheUserOnTheSpanAndContext pins NS FG-10: an
// authenticated request's server span carries user.id, and its context
// hands the id to the log handler — even when the project installs its own
// claims stash.
func TestInterceptorNamesTheUserOnTheSpanAndContext(t *testing.T) {
	t.Parallel()
	rec := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(rec))
	type ownKey struct{}
	ic, err := NewInterceptor(Policy{
		ValidatorConfigured: true,
		ValidateContext: func(context.Context, string) (*auth.Claims, error) {
			return &auth.Claims{UserID: "u-42", Email: "a@example.com"}, nil
		},
		ContextWithClaims: func(ctx context.Context, c *Claims) context.Context {
			return context.WithValue(ctx, ownKey{}, c)
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	var sawID string
	wrapped := ic.WrapUnary(func(ctx context.Context, _ connect.AnyRequest) (connect.AnyResponse, error) {
		sawID = UserIDFromContext(ctx)
		return nil, nil
	})
	req := &fakeReq{procedure: "/demo.v1.Service/Method", header: http.Header{}}
	req.header.Set("Authorization", "Bearer t")
	ctx, span := tp.Tracer("test").Start(context.Background(), "rpc")
	if _, err := wrapped(ctx, req); err != nil {
		t.Fatal(err)
	}
	span.End()
	if sawID != "u-42" {
		t.Fatalf("UserIDFromContext = %q, want u-42", sawID)
	}
	var got string
	for _, a := range rec.Ended()[0].Attributes() {
		if string(a.Key) == AttrUserID {
			got = a.Value.AsString()
		}
		if a.Value.AsString() == "a@example.com" {
			t.Errorf("the span carries the email (%s): only the opaque id belongs on every span", a.Key)
		}
	}
	if got != "u-42" {
		t.Fatalf("span %s = %q, want u-42", AttrUserID, got)
	}
	if UserIDFromContext(context.Background()) != "" || ContextWithUserID(context.Background(), "") != context.Background() {
		t.Fatal("no principal must name no user")
	}
}
