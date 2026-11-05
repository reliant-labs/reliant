package observe

import (
	"log/slog"
	"testing"

	"connectrpc.com/connect"

	"github.com/reliant-labs/forge/pkg/svcerr"
)

func TestDefaultMiddlewares_CanonicalOrder(t *testing.T) {
	chain := DefaultMiddlewares(DefaultMiddlewareDeps{})
	// Order matters; this test pins the canonical sequence so future
	// reorderings show up as a deliberate test edit. See the package
	// docstring for the rationale.
	if len(chain) != 6 {
		t.Fatalf("expected 5 canonical + svcerr = 6 interceptors, got %d", len(chain))
	}
	if _, ok := chain[0].(*recoveryInterceptor); !ok {
		t.Errorf("position 0 = %T, want *recoveryInterceptor", chain[0])
	}
	if _, ok := chain[1].(*requestIDInterceptor); !ok {
		t.Errorf("position 1 = %T, want *requestIDInterceptor", chain[1])
	}
	if _, ok := chain[2].(*loggingInterceptor); !ok {
		t.Errorf("position 2 = %T, want *loggingInterceptor", chain[2])
	}
	// positions 3 and 4 are tracer/meter interceptors but they degrade
	// to noopInterceptor when those deps are nil — assert noop here.
	if _, ok := chain[3].(*noopInterceptor); !ok {
		t.Errorf("position 3 (no tracer) = %T, want *noopInterceptor", chain[3])
	}
	if _, ok := chain[4].(*noopInterceptor); !ok {
		t.Errorf("position 4 (no meter) = %T, want *noopInterceptor", chain[4])
	}
	if !svcerr.IsInterceptor(chain[5]) {
		t.Errorf("position 5 = %T, want svcerr.Interceptor innermost", chain[5])
	}
}

func TestDefaultMiddlewares_AppendsExtras(t *testing.T) {
	stub := connect.UnaryInterceptorFunc(func(next connect.UnaryFunc) connect.UnaryFunc { return next })
	chain := DefaultMiddlewares(DefaultMiddlewareDeps{
		Logger: slog.Default(),
		Extras: []connect.Interceptor{stub, stub},
	})
	if len(chain) != 8 {
		t.Fatalf("expected 5 canonical + 2 extras + svcerr = 8, got %d", len(chain))
	}
	// Extras come after the canonical layer, svcerr after them.
	if !svcerr.IsInterceptor(chain[7]) {
		t.Errorf("last position = %T, want svcerr.Interceptor", chain[7])
	}
	if _, ok := chain[5].(connect.UnaryInterceptorFunc); !ok {
		t.Errorf("extra position 5 = %T, want connect.UnaryInterceptorFunc", chain[5])
	}
}

func TestDefaultMiddlewares_NilDepsDontPanic(t *testing.T) {
	chain := DefaultMiddlewares(DefaultMiddlewareDeps{})
	if len(chain) == 0 {
		t.Fatal("empty chain")
	}
}
