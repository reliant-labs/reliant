package observe_test

import (
	"context"
	"testing"

	"github.com/reliant-labs/forge/pkg/observe"
	"github.com/reliant-labs/forge/pkg/servicecatalog"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

func TestComponentMethodIDsPreserveLegacyAndDoNotLeakToNestedCalls(t *testing.T) {
	recorder := tracetest.NewSpanRecorder()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
	defer provider.Shutdown(context.Background())
	base := observe.NewComponentChain(observe.TraceMiddleware(provider.Tracer("example.com/shop/internal/orders")))
	id := servicecatalog.MethodID("example.com/shop/internal/orders", "Service", "Get")
	ids := map[string]string{"orders.Get": id}
	identified := base.WithMethodIDs(ids)
	ids["orders.Get"] = "mutated"
	_, err := identified.Around(context.Background(), "orders.Get", func(ctx context.Context) (int, error) {
		return 42, base.Run(ctx, "legacy.Nested", func(context.Context) error { return nil })
	})
	if err != nil {
		t.Fatal(err)
	}
	spans := recorder.Ended()
	if len(spans) != 2 {
		t.Fatal(len(spans))
	}
	for _, s := range spans {
		found := ""
		for _, a := range s.Attributes() {
			if string(a.Key) == "forge.method.id" {
				found = a.Value.AsString()
			}
		}
		if s.Name() == "orders.Get" {
			if found != id || s.InstrumentationScope().Name != "example.com/shop/internal/orders" {
				t.Fatalf("identity/legacy scope lost: %s %s", found, s.Name())
			}
		} else if found != "" {
			t.Fatalf("outer identity leaked to nested legacy span: %s", found)
		}
	}
	var nilChain *observe.ComponentChain
	if err := nilChain.WithMethodIDs(ids).Run(context.Background(), "orders.Get", func(context.Context) error { return nil }); err != nil {
		t.Fatal(err)
	}
}
