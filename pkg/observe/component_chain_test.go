package observe_test

import (
	"context"
	"errors"
	"testing"

	"github.com/reliant-labs/forge/pkg/observe"
)

// markerComponentMW records, on a shared trace slice, that it ran (both on
// the way in and out), so a test can assert ordering through the in-process
// chain.
type markerComponentMW struct {
	name  string
	trace *[]string
}

func (m markerComponentMW) WrapComponent(ctx context.Context, method string, next observe.ComponentOp) error {
	*m.trace = append(*m.trace, m.name+":in")
	err := next(ctx)
	*m.trace = append(*m.trace, m.name+":out")
	return err
}

func TestComponentChain_OuterToInnerOrder(t *testing.T) {
	var trace []string
	chain := observe.NewComponentChain(
		markerComponentMW{name: "a", trace: &trace},
		markerComponentMW{name: "b", trace: &trace},
	)
	err := chain.Run(context.Background(), "pkg.Do", func(ctx context.Context) error {
		trace = append(trace, "handler")
		return nil
	})
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	// a is outermost: a:in, b:in, handler, b:out, a:out.
	want := []string{"a:in", "b:in", "handler", "b:out", "a:out"}
	if len(trace) != len(want) {
		t.Fatalf("trace = %v, want %v", trace, want)
	}
	for i := range want {
		if trace[i] != want[i] {
			t.Fatalf("trace = %v, want %v", trace, want)
		}
	}
}

func TestComponentChain_AroundReturnsValueAndError(t *testing.T) {
	sentinel := errors.New("boom")
	chain := observe.NewComponentChain(
		markerComponentMW{name: "a", trace: new([]string)},
	)
	// Value path.
	got, err := chain.Around(context.Background(), "pkg.Get", func(ctx context.Context) (int, error) {
		return 42, nil
	})
	if got != 42 || err != nil {
		t.Fatalf("Around value path: got (%d, %v), want (42, nil)", got, err)
	}
	// Error path — value zero, error threaded back through the chain.
	got, err = chain.Around(context.Background(), "pkg.Get", func(ctx context.Context) (int, error) {
		return 7, sentinel
	})
	if got != 7 || !errors.Is(err, sentinel) {
		t.Fatalf("Around error path: got (%d, %v), want (7, boom)", got, err)
	}
}

func TestComponentChain_NilChainPassThrough(t *testing.T) {
	// A nil *ComponentChain must still run the operation (decorator wired
	// without a chain, e.g. a test harness).
	var chain *observe.ComponentChain
	ran := false
	err := chain.Run(context.Background(), "pkg.Do", func(ctx context.Context) error {
		ran = true
		return nil
	})
	if err != nil || !ran {
		t.Fatalf("nil chain: ran=%v err=%v, want ran=true err=nil", ran, err)
	}
	got, err := chain.Around(context.Background(), "pkg.Get", func(ctx context.Context) (string, error) {
		return "ok", nil
	})
	if got != "ok" || err != nil {
		t.Fatalf("nil chain Around: got (%q, %v), want (ok, nil)", got, err)
	}
}

func TestComponentChain_NilMiddlewareDropped(t *testing.T) {
	var trace []string
	// A nil middleware in the variadic list is dropped, not invoked.
	chain := observe.NewComponentChain(
		nil,
		markerComponentMW{name: "a", trace: &trace},
		nil,
	)
	if err := chain.Run(context.Background(), "pkg.Do", func(ctx context.Context) error {
		trace = append(trace, "handler")
		return nil
	}); err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	want := []string{"a:in", "handler", "a:out"}
	if len(trace) != len(want) {
		t.Fatalf("trace = %v, want %v", trace, want)
	}
}

func TestRecoverMiddleware_ConvertsPanicToError(t *testing.T) {
	// RecoverMiddleware (nil logger → slog.Default) turns a panic into an
	// error instead of unwinding the stack.
	chain := observe.NewComponentChain(observe.RecoverMiddleware(nil))
	err := chain.Run(context.Background(), "pkg.Boom", func(ctx context.Context) error {
		panic("kaboom")
	})
	if err == nil {
		t.Fatal("expected a recovered panic to surface as an error")
	}
	// Around's value path must also recover and return the zero value.
	got, err := chain.Around(context.Background(), "pkg.Boom", func(ctx context.Context) (int, error) {
		panic("kaboom")
	})
	if err == nil || got != 0 {
		t.Fatalf("Around recover: got (%d, %v), want (0, non-nil)", got, err)
	}
}

func TestTraceMiddleware_NilTracerPassThrough(t *testing.T) {
	// nil tracer must not panic and must still run the inner op.
	chain := observe.NewComponentChain(observe.TraceMiddleware(nil), observe.MetricsMiddleware(nil, "pkg"))
	ran := false
	err := chain.Run(context.Background(), "pkg.Do", func(ctx context.Context) error {
		ran = true
		return nil
	})
	if err != nil || !ran {
		t.Fatalf("nil tracer/meter: ran=%v err=%v, want ran=true err=nil", ran, err)
	}
}
