package readguard

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"
	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/stretchr/testify/require"
)

func TestFlightPanicCompletesAndReleasesSlot(t *testing.T) {
	g := New(Deps{PerUser: 1}).(*guard)
	proc := Read[pb.ListChatsResponse]("list")
	_, err := g.do(context.Background(), "user", "key", proc, func(context.Context) (connect.AnyResponse, error) {
		panic("secret handler detail")
	})
	require.Equal(t, connect.CodeInternal, connect.CodeOf(err))
	require.NotContains(t, err.Error(), "secret handler detail")
	resp, err := g.do(context.Background(), "user", "key", proc, func(context.Context) (connect.AnyResponse, error) {
		return connect.NewResponse(&pb.ListChatsResponse{Total: 1}), nil
	})
	require.NoError(t, err)
	require.Equal(t, int32(1), resp.Any().(*pb.ListChatsResponse).Total)
	g.mu.Lock()
	defer g.mu.Unlock()
	require.Empty(t, g.users)
}

// Outer interceptors may mutate a result immediately. Even the first waiter
// must own a clone, and empty Connect metadata must be initialized before
// concurrent cloning begins. Run under -race to exercise that boundary.
func TestEveryWaiterOwnsResponseAndErrorMetadata(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(map[bool]string{false: "response", true: "error"}[fail], func(t *testing.T) {
			g := New(Deps{}).(*guard)
			proc := Read[pb.ListChatsResponse]("list")
			const count = 30
			release := make(chan struct{})
			var wg sync.WaitGroup
			for range count {
				wg.Add(1)
				go func() {
					defer wg.Done()
					resp, err := g.do(context.Background(), "user", "key", proc, func(context.Context) (connect.AnyResponse, error) {
						<-release
						if fail {
							return nil, connect.NewError(connect.CodeUnavailable, errors.New("retry"))
						}
						return connect.NewResponse(&pb.ListChatsResponse{Total: 1}), nil
					})
					// Model an outer interceptor modifying its response while other
					// waiters are still copying the shared flight's result.
					if fail {
						err.(*connect.Error).Meta().Set("X-Caller", "owned")
					} else {
						resp.Header().Set("X-Caller", "owned")
						resp.Trailer().Set("X-Caller", "owned")
						resp.Any().(*pb.ListChatsResponse).Total++
					}
				}()
			}
			require.Eventually(t, func() bool { return callers(g, "user") == count }, time.Second, time.Millisecond)
			close(release)
			wg.Wait()
		})
	}
}
