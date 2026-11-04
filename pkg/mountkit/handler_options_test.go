package mountkit_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/emptypb"

	"github.com/reliant-labs/forge/pkg/mountkit"
)

// callLog records the order interceptors ran in.
type callLog struct {
	mu    sync.Mutex
	calls []string
}

func (l *callLog) interceptor(name string) connect.HandlerOption {
	return connect.WithInterceptors(connect.UnaryInterceptorFunc(func(next connect.UnaryFunc) connect.UnaryFunc {
		return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
			l.mu.Lock()
			l.calls = append(l.calls, name)
			l.mu.Unlock()
			return next(ctx, req)
		}
	}))
}

func (l *callLog) take() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	out := strings.Join(l.calls, ",")
	l.calls = nil
	return out
}

// pingService is a real Connect service: Register builds its handler with
// exactly the options it is handed, so a call through it runs them.
type pingService struct{ procedure string }

func (s pingService) Register(mux *http.ServeMux, opts ...connect.HandlerOption) {
	mux.Handle(s.procedure, connect.NewUnaryHandler(s.procedure,
		func(context.Context, *connect.Request[emptypb.Empty]) (*connect.Response[emptypb.Empty], error) {
			return connect.NewResponse(&emptypb.Empty{}), nil
		}, opts...))
}

func ping(t *testing.T, srv *httptest.Server, procedure string) {
	t.Helper()
	client := connect.NewClient[emptypb.Empty, emptypb.Empty](srv.Client(), srv.URL+procedure)
	if _, err := client.CallUnary(context.Background(), connect.NewRequest(&emptypb.Empty{})); err != nil {
		t.Fatalf("call %s: %v", procedure, err)
	}
}

// WithHandlerOptions adds options to ONE mount, after the shared chain: the
// shared interceptors run first (outermost), then the mount's own, and a
// service mounted without it runs the shared chain alone. The shared slice
// has spare capacity, so a mount that appended in place would leak its
// options into the next mount.
func TestRegisterService_HandlerOptionsFollowTheSharedChain(t *testing.T) {
	log := &callLog{}
	shared := make([]connect.HandlerOption, 0, 8)
	shared = append(shared, log.interceptor("shared-1"), log.interceptor("shared-2"))

	mux := http.NewServeMux()
	wake := pingService{procedure: "/test.v1.WorktreeService/Ping"}
	plain := pingService{procedure: "/test.v1.ChatService/Ping"}
	both := pingService{procedure: "/test.v1.FileSystemService/Ping"}
	mountkit.RegisterService(mux, wake, shared, mountkit.WithHandlerOptions(log.interceptor("wake")))
	mountkit.RegisterService(mux, plain, shared)
	mountkit.RegisterService(mux, both, shared,
		mountkit.WithHandlerOptions(log.interceptor("mount-1")),
		mountkit.WithHandlerOptions(log.interceptor("mount-2")))
	srv := httptest.NewServer(mux)
	defer srv.Close()

	for _, tc := range []struct{ procedure, want string }{
		{wake.procedure, "shared-1,shared-2,wake"},
		{plain.procedure, "shared-1,shared-2"},
		{both.procedure, "shared-1,shared-2,mount-1,mount-2"},
	} {
		ping(t, srv, tc.procedure)
		if got := log.take(); got != tc.want {
			t.Errorf("%s ran %q, want %q", tc.procedure, got, tc.want)
		}
	}
}

// Resolve is the same rule for a caller that registers through its own
// methods (the generated mounts): shared then per-mount, a fresh slice,
// and the default stack unless WithHTTPStack replaced it.
func TestResolve(t *testing.T) {
	log := &callLog{}
	shared := make([]connect.HandlerOption, 1, 4)
	shared[0] = log.interceptor("shared")
	defaultStack := func(h http.Handler) http.Handler { return h }

	r := mountkit.Resolve(shared, defaultStack, mountkit.WithHandlerOptions(log.interceptor("mount")))
	if len(r.HandlerOptions) != 2 || len(shared) != 1 {
		t.Fatalf("Resolve: %d options (shared now %d), want 2 (shared untouched)", len(r.HandlerOptions), len(shared))
	}
	if r2 := mountkit.Resolve(shared, defaultStack); len(r2.HandlerOptions) != 1 {
		t.Errorf("a mount with no options must get the shared chain alone, got %d", len(r2.HandlerOptions))
	}

	marked := false
	custom := func(h http.Handler) http.Handler { marked = true; return h }
	r3 := mountkit.Resolve(shared, defaultStack, mountkit.WithHTTPStack(custom), nil)
	r3.HTTPStack(http.NotFoundHandler())
	if !marked {
		t.Error("WithHTTPStack must replace the default stack")
	}
	if r4 := mountkit.Resolve(nil, nil); r4.HTTPStack != nil || len(r4.HandlerOptions) != 0 {
		t.Errorf("Resolve(nil, nil) = %+v, want empty", r4)
	}
}
