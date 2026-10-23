package serverkit_test

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
	"github.com/reliant-labs/forge/pkg/serverkit"
)

// pingSvc is a real Connect service built with exactly the options Mount
// hands it.
type pingSvc struct{ procedure string }

func (s pingSvc) Register(mux *http.ServeMux, opts ...connect.HandlerOption) {
	mux.Handle(s.procedure, connect.NewUnaryHandler(s.procedure,
		func(context.Context, *connect.Request[emptypb.Empty]) (*connect.Response[emptypb.Empty], error) {
			return connect.NewResponse(&emptypb.Empty{}), nil
		}, opts...))
}

// Server.Mount gives one service options on top of Server.HandlerOpts: the
// shared chain runs first, then the mount's own (reliant's machine-wake
// interceptor on two of its services), and other mounts are untouched.
func TestServerMount_PerMountHandlerOptionsFollowHandlerOpts(t *testing.T) {
	var (
		mu    sync.Mutex
		calls []string
	)
	record := func(name string) connect.HandlerOption {
		return connect.WithInterceptors(connect.UnaryInterceptorFunc(func(next connect.UnaryFunc) connect.UnaryFunc {
			return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
				mu.Lock()
				calls = append(calls, name)
				mu.Unlock()
				return next(ctx, req)
			}
		}))
	}
	take := func() string {
		mu.Lock()
		defer mu.Unlock()
		out := strings.Join(calls, ",")
		calls = nil
		return out
	}

	srv := serverkit.Server{
		Mux:         http.NewServeMux(),
		HandlerOpts: []connect.HandlerOption{record("recovery"), record("auth")},
	}
	worktree := pingSvc{procedure: "/serverkit.mount.v1.Worktree/Ping"}
	chat := pingSvc{procedure: "/serverkit.mount.v1.Chat/Ping"}
	srv.Mount("serverkit.mount.v1.Worktree", worktree, mountkit.WithHandlerOptions(record("machinewake")))
	srv.Mount("serverkit.mount.v1.Chat", chat)

	hs := httptest.NewServer(srv.Mux)
	defer hs.Close()
	for _, tc := range []struct{ procedure, want string }{
		{worktree.procedure, "recovery,auth,machinewake"},
		{chat.procedure, "recovery,auth"},
	} {
		client := connect.NewClient[emptypb.Empty, emptypb.Empty](hs.Client(), hs.URL+tc.procedure)
		if _, err := client.CallUnary(context.Background(), connect.NewRequest(&emptypb.Empty{})); err != nil {
			t.Fatalf("call %s: %v", tc.procedure, err)
		}
		if got := take(); got != tc.want {
			t.Errorf("%s ran %q, want %q", tc.procedure, got, tc.want)
		}
	}
}
